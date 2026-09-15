// loop.go 自研 agent 循环（AGENTS.md 第 6 节路线 C）。
//
// 与 react.Agent 的区别：**循环是我们自己的**，step 是一等公民（每步是一次模型调用 + 它触发的工具），
// Eino 只贡献"零件"（模型适配缝 + 工具接口 + 流式消息拼接）。这样才可能做 P3-1 的 step 级 checkpoint。
//
// 循环形状（对齐讲义 lessons/01-agent-loop.md 与 mini-loop 的 run/turn/step）：
//
//	每次模型调用 = 一个 step
//	  有工具调用 → 执行工具 → 落 tool/call + tool/result 事件 → 交棒（再调一次模型）
//	  无工具调用 → 退出（这一轮结束）
//
// 六条硬契约（从现有 58 个用例倒推，见 AGENT-ENGINEER-PLAN.md 阶段 2）都在这里落实：
//  1. 建流失败同步返回 error           —— Stream 里同步做第一次模型调用
//  2. 正常结束 io.EOF、中途断非 EOF      —— drain 区分，非 EOF 原样透传给调用方
//  3. usage 透传                       —— 所有 chunk 原样转发给调用方
//  4. 自己落 tool/call + tool/result   —— executeTools（不再依赖 MessageModifier 的下标差分）
//  5. 流式 tool_call 分片拼装           —— schema.ConcatMessages 按 index 合并
//  6. 重试仍由 retry.Wrap 承担          —— NewOwnLoop 里和 react 路径用同一份包装
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/retry"
	"go_im_gateway/internal/harness/session"
)

// defaultMaxSteps 一轮最多跑多少步（防止模型/工具互相喂结果导致无限循环）。
const defaultMaxSteps = 10

// ownLoop 就是"推倒黑盒"之后的循环。
type ownLoop struct {
	model    model.ChatModel                  // 已包一层重试的模型
	tools    []tool.InvokableTool             // 已套 guard 流水线的工具（保序）
	byName   map[string]tool.InvokableTool    // 按名字查工具
	maxSteps int
}

// NewOwnLoop 组装自研循环。
//
// 模型与工具的装配刻意和 BuildEinoAgent（react 路线）保持一致：
// 同一份重试包装、同一份 AllTools() 流水线、同一份初始重试预算 —— 这样 AGENT_LOOP 切换前后可比。
func NewOwnLoop(ctx context.Context) (Loop, error) {
	chatModel, err := newChatModel(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := getUserID(ctx)
	if err != nil {
		return nil, err
	}

	chatModel = retry.Wrap(chatModel, retry.Config{
		Policy:         retryPolicyFromEnv(),
		InitialAttempt: retryAttemptsSoFar(ctx, userID),
		Observer:       retryLogger(ctx, userID),
	})

	tools := make([]tool.InvokableTool, 0, 8)
	byName := make(map[string]tool.InvokableTool)
	for _, t := range AllTools() {
		if t == nil {
			continue
		}
		it, ok := t.(tool.InvokableTool)
		if !ok {
			continue // 只有 Info 的 BaseTool 不可调用，跳过（流水线也不假装拦得住它）
		}
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name == "" {
			continue
		}
		tools = append(tools, it)
		byName[info.Name] = it
	}

	return &ownLoop{
		model:    chatModel,
		tools:    tools,
		byName:   byName,
		maxSteps: maxStepsFromEnv(),
	}, nil
}

// maxStepsFromEnv 允许用 AGENT_MAX_STEPS 覆盖步数上限；没配用默认。
func maxStepsFromEnv() int {
	if n, ok := envInt("AGENT_MAX_STEPS"); ok && n > 0 {
		return n
	}
	return defaultMaxSteps
}

// Stream 实现 agent.Loop。
//
// 关键：**第一次模型调用同步做** —— 建流失败要在这里直接返回 error（契约 1），
// 之后的行进放到 goroutine 里，通过 schema.Pipe 把 chunk 流给调用方。
func (l *ownLoop) Stream(ctx context.Context, input []*schema.Message, _ ...einoagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	bound, err := l.bindTools(ctx)
	if err != nil {
		return nil, err
	}

	first, err := bound.Stream(ctx, input)
	if err != nil {
		return nil, err // 契约 1：同步返回（对齐 react，TestNonRetryableErrorFailsFast 靠它）
	}

	out, sw := schema.Pipe[*schema.Message](8)
	go func() {
		defer sw.Close() // 正常结束 → 调用方 Recv 得到 io.EOF（契约 2）
		l.run(ctx, input, bound, first, sw)
	}()
	return out, nil
}

// bindTools 把工具的 schema 绑到模型上（等价 react 内部的 ChatModelWithTools）。
// 不绑的话请求里没有 tools，模型根本不知道有哪些工具可用。
func (l *ownLoop) bindTools(ctx context.Context) (model.BaseChatModel, error) {
	infos := make([]*schema.ToolInfo, 0, len(l.tools))
	for _, it := range l.tools {
		info, err := it.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("读取工具 schema 失败: %w", err)
		}
		infos = append(infos, info)
	}
	return einoagent.ChatModelWithTools(l.model, nil, infos)
}

// run 是循环本体（在一个 goroutine 里跑），逐步推进直到退出或失败。
func (l *ownLoop) run(ctx context.Context, input []*schema.Message, m model.BaseChatModel, reader *schema.StreamReader[*schema.Message], sw *schema.StreamWriter[*schema.Message]) {
	userID, _ := getUserID(ctx) // best-effort：写事件用，取不到就写不了（不影响对话）
	messages := append([]*schema.Message(nil), input...)

	for step := 0; step < l.maxSteps; step++ {
		chunks, err := drain(reader, sw)
		if err != nil {
			sw.Send(nil, err) // 契约 2：中断原样透传（调用方据此标 interrupted）
			return
		}

		full, err := schema.ConcatMessages(chunks) // 契约 5：流式分片拼成完整消息（含 tool_call 合并）
		if err != nil {
			sw.Send(nil, fmt.Errorf("拼接模型回复失败: %w", err))
			return
		}
		messages = append(messages, full)

		if len(full.ToolCalls) == 0 {
			return // 退出：模型不再要工具，这一轮说完
		}

		messages = append(messages, l.executeTools(ctx, userID, full.ToolCalls)...)

		next, err := m.Stream(ctx, messages) // 交棒：把工具结果喂回去再调一次模型
		if err != nil {
			sw.Send(nil, err)
			return
		}
		reader = next
	}

	sw.Send(nil, fmt.Errorf("ownLoop：超过最大步数 %d，已停止", l.maxSteps))
}

// drain 读干一次模型流：把每个 chunk 原样转发给调用方（文本→emit、usage→记账，契约 3），
// 同时收集起来供 ConcatMessages 拼接。正常结束返回 nil error。
func drain(reader *schema.StreamReader[*schema.Message], sw *schema.StreamWriter[*schema.Message]) ([]*schema.Message, error) {
	defer reader.Close()

	var chunks []*schema.Message
	for {
		msg, err := reader.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return chunks, nil
			}
			return chunks, err // 非 EOF：上游断流/畸形帧
		}
		chunks = append(chunks, msg)
		if sw.Send(msg, nil) {
			return chunks, errors.New("调用方已停止读取输出流")
		}
	}
}

// executeTools 执行这一步的全部工具调用，并落 tool/call + tool/result 事件（契约 4）。
// 事件顺序：先 tool/call（一条 DTO 带全部调用），再按执行顺序逐条 tool/result。
// 不变量校验（session.prepareForWrite）在此把关：tool/result 缺 ToolCallID 会被拒写。
func (l *ownLoop) executeTools(ctx context.Context, userID uint, calls []schema.ToolCall) []*schema.Message {
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: calls}
	pending := []session.MemoryDTO{memoryDTOFromToolCall(assistant)}

	out := make([]*schema.Message, 0, len(calls))
	for _, tc := range calls {
		msg := schema.ToolMessage(l.runOne(ctx, tc), tc.ID)
		msg.ToolName = tc.Function.Name
		out = append(out, msg)
		pending = append(pending, memoryDTOFromToolResult(ctx, msg))
	}

	if userID != 0 {
		session.WriteMemoryEvents(ctx, userID, pending)
	}
	return out
}

// runOne 执行单个工具调用。工具不存在或执行失败都**不炸对话** ——
// 包成"工具结果"交给模型，让它决定下一步（和 guard 流水线的失败语义一致）。
func (l *ownLoop) runOne(ctx context.Context, tc schema.ToolCall) string {
	it, ok := l.byName[tc.Function.Name]
	if !ok {
		return fmt.Sprintf("⚠️ 未知工具 %s，未执行。", tc.Function.Name)
	}
	out, err := it.InvokableRun(ctx, tc.Function.Arguments)
	if err != nil {
		return fmt.Sprintf("⚠️ 工具 %s 执行失败：%v", tc.Function.Name, err)
	}
	return out
}
