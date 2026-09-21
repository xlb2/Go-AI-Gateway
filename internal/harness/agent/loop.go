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
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/retry"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/subagent"
)

// defaultMaxSteps 一轮最多跑多少步（防止模型/工具互相喂结果导致无限循环）。
const defaultMaxSteps = 10

var (
	ErrOutputTruncated = errors.New("模型输出达到长度上限，回复不完整")
	ErrMaxSteps        = errors.New("达到最大步数，任务未完成")
)

// StepEndReason 一个 step 为什么结束 —— **结构化**，写进 log-only 的 `step/end` 事件。
//
// 为什么必须结构化（对应讲义 01 的 Q3）：一个 `bool` 只能表达"有没有被截断"，
// 表达不了"**为什么**停"；而"重试 / 恢复 / 续跑"的决策恰恰需要知道原因 ——
// token 上限该压缩后继续，用户取消绝不该自动重试，审批挂起要等批准。
type StepEndReason string

const (
	StepHandoff   StepEndReason = "handoff"    // 有工具调用，交棒给下一个 step
	StepCompleted StepEndReason = "completed"  // 模型不再要工具，这一轮说完
	StepMaxTokens StepEndReason = "max-tokens" // 输出被 token 上限截断
	StepAborted   StepEndReason = "aborted"    // 上下文被取消（用户 / 上层中止）
	StepError     StepEndReason = "error"      // 模型调用或流本身出错
	StepMaxSteps  StepEndReason = "max-steps"  // 达到步数上限，工具已执行但未继续请求模型
)

// ownLoop 就是"推倒黑盒"之后的循环。
type ownLoop struct {
	logNested   bool
	model       model.ChatModel               // 已包一层重试的模型
	tools       []tool.InvokableTool          // 已套 guard 流水线的工具（保序）
	byName      map[string]tool.InvokableTool // 按名字查工具
	maxSteps    int
	writeEvents func(context.Context, uint, []session.MemoryDTO) error
	toolResult  func(context.Context, *schema.Message) session.MemoryDTO
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
	}

	return NewConfiguredLoop(ctx, LoopConfig{
		Model: chatModel, Tools: tools, MaxSteps: maxStepsFromEnv(),
		WriteEvents: session.WriteMemoryEvents, ToolResult: memoryDTOFromToolResult,
	})
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
	// 取消错误使用独立通道，不能排在已满的文本缓冲后面等待消费者。
	canceled, cancelWriter := schema.Pipe[*schema.Message](1)
	out.SetAutomaticClose()
	canceled.SetAutomaticClose()
	done := make(chan struct{})
	go func() {
		defer cancelWriter.Close()
		select {
		case <-ctx.Done():
			cancelWriter.Send(nil, ctx.Err())
			out.Close() // 释放正在等待输出空间的 Send。
		case <-done:
			if err := ctx.Err(); err != nil {
				cancelWriter.Send(nil, err)
			}
		}
	}()
	go func() {
		defer close(done)
		defer sw.Close() // 正常结束 → 调用方 Recv 得到 io.EOF（契约 2）
		l.run(ctx, input, bound, first, sw)
	}()
	return schema.MergeStreamReaders([]*schema.StreamReader[*schema.Message]{out, canceled}), nil
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
//
// P3-1：每一步都落一对 `step/start` / `step/end`（log-only），step/end 带**结构化原因**。
// 这样"跑到第几步、最后一步为什么结束"可从日志重建 —— 断点续跑的地基。
func (l *ownLoop) run(ctx context.Context, input []*schema.Message, m model.BaseChatModel, reader *schema.StreamReader[*schema.Message], sw *schema.StreamWriter[*schema.Message]) {
	defer func() {
		if reader != nil {
			reader.Close()
		}
	}()
	userID, _ := getUserID(ctx) // best-effort：写事件用，取不到就写不了（不影响对话）
	if subagent.IsNested(ctx) && !l.logNested {
		// 子 agent 的痕迹不写进父会话日志：既是隔离（中间过程不该回流），
		// 也避免父子两股 step 事件交错、把父日志的 step 序列搞乱。
		userID = 0
	}
	messages := append([]*schema.Message(nil), input...)

	for step := 1; step <= l.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			// 调用方取消了：不再开新 step（避免"开了 step 却没人收尾"），把取消原样透传。
			sw.Send(nil, err)
			return
		}
		if err := l.logStepEvent(ctx, userID, session.EventStepStart, ""); err != nil {
			sw.Send(nil, err)
			return
		}

		chunks, err := drain(ctx, reader, sw)
		reader = nil // drain 拥有并关闭本步 reader。
		if err != nil {
			logErr := l.logStepEvent(ctx, userID, session.EventStepEnd, string(reasonForError(ctx)))
			sw.Send(nil, errors.Join(err, logErr))
			return
		}

		full, err := schema.ConcatMessages(chunks) // 契约 5：流式分片拼成完整消息（含 tool_call 合并）
		if err != nil {
			logErr := l.logStepEvent(ctx, userID, session.EventStepEnd, string(StepError))
			sw.Send(nil, errors.Join(fmt.Errorf("拼接模型回复失败: %w", err), logErr))
			return
		}
		messages = append(messages, full)

		if isTruncated(full) {
			logErr := l.logStepEvent(ctx, userID, session.EventStepEnd, string(StepMaxTokens))
			sw.Send(nil, errors.Join(ErrOutputTruncated, logErr))
			return // 被 token 上限截断：以非 EOF 错误结束
		}
		if len(full.ToolCalls) == 0 {
			if err := l.logStepEvent(ctx, userID, session.EventStepEnd, string(StepCompleted)); err != nil {
				sw.Send(nil, err)
			}
			return // 退出：模型不再要工具，这一轮说完
		}

		results, err := l.executeTools(ctx, userID, full.ToolCalls)
		if err != nil {
			logErr := l.logStepEvent(ctx, userID, session.EventStepEnd, string(StepError))
			sw.Send(nil, errors.Join(err, logErr))
			return
		}
		messages = append(messages, results...)
		if step == l.maxSteps {
			logErr := l.logStepEvent(ctx, userID, session.EventStepEnd, string(StepMaxSteps))
			sw.Send(nil, errors.Join(fmt.Errorf("%w: %d", ErrMaxSteps, l.maxSteps), logErr))
			return
		}
		if err := l.logStepEvent(ctx, userID, session.EventStepEnd, string(StepHandoff)); err != nil {
			sw.Send(nil, err)
			return
		}

		next, err := m.Stream(ctx, messages) // 交棒：把工具结果喂回去再调一次模型
		if err != nil {
			// 失败发生在**下一个 step 的模型调用**上 —— 上一个 step 已闭合，
			// 所以断点续跑会从"已闭合的 handoff step"之后重试这次模型调用，不重放工具副作用。
			sw.Send(nil, err)
			return
		}
		reader = next
	}

	sw.Send(nil, fmt.Errorf("ownLoop：超过最大步数 %d，已停止", l.maxSteps))
}

// logStepEvent 把 step 边界写进日志（log-only，不喂模型）。user_id 取不到就跳过（best-effort）。
func (l *ownLoop) logStepEvent(ctx context.Context, userID uint, kind, content string) error {
	if userID == 0 {
		return nil
	}
	write := l.writeEvents
	if write == nil {
		write = session.WriteMemoryEvents
	}
	if err := write(ctx, userID, []session.MemoryDTO{{
		Type: kind, Role: "system", Content: content,
	}}); err != nil {
		return fmt.Errorf("step event %s: %w", kind, err)
	}
	return nil
}

// isTruncated 这次模型输出是不是被 token 上限截断了（OpenAI 协议：finish_reason == "length"）。
func isTruncated(msg *schema.Message) bool {
	return msg != nil && msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason == "length"
}

// reasonForError 把"流读取出错"归类：上下文已取消 = aborted，其余 = error。
func reasonForError(ctx context.Context) StepEndReason {
	if ctx.Err() != nil {
		return StepAborted
	}
	return StepError
}

// drain 读干一次模型流：把每个 chunk 原样转发给调用方（文本→emit、usage→记账，契约 3），
// 同时收集起来供 ConcatMessages 拼接。正常结束返回 nil error。
func drain(ctx context.Context, reader *schema.StreamReader[*schema.Message], sw *schema.StreamWriter[*schema.Message]) ([]*schema.Message, error) {
	var closeOnce sync.Once
	closeReader := func() { closeOnce.Do(reader.Close) }
	stop := context.AfterFunc(ctx, closeReader)
	defer func() { stop(); closeReader() }()

	var chunks []*schema.Message
	for {
		// 调用方取消后不再往输出流写：pipe 满的时候 Send 会阻塞，
		// 调用方若不读了，这个 goroutine 就会永远挂着（泄漏）。
		if err := ctx.Err(); err != nil {
			return chunks, err
		}
		msg, err := reader.Recv()
		if cancelErr := ctx.Err(); cancelErr != nil {
			return chunks, cancelErr
		}
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
//
// **幂等前提（P3-1）**：`tool/call` 必须在工具**执行之前**落盘。否则进程在
// "工具已跑、结果没落"的窗口里挂掉，日志里没有任何痕迹，断点续跑只能重放这个工具（副作用做两遍）。
// 先落 call 之后，resume 见到"有 call 无 result"就能判定"已派发、结果未知"，
// 交给 Repair 合成一条结果，**绝不盲目重放**。
func (l *ownLoop) executeTools(ctx context.Context, userID uint, calls []schema.ToolCall) ([]*schema.Message, error) {
	write := l.writeEvents
	if write == nil {
		write = session.WriteMemoryEvents
	}
	resultDTO := l.toolResult
	if resultDTO == nil {
		resultDTO = memoryDTOFromToolResult
	}
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: calls}
	if userID != 0 {
		if err := write(ctx, userID, []session.MemoryDTO{memoryDTOFromToolCall(assistant)}); err != nil {
			return nil, fmt.Errorf("工具调用记录未确认，本步工具未执行: %w", err)
		}
	}

	out := make([]*schema.Message, 0, len(calls))
	for _, tc := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msg := schema.ToolMessage(l.runOne(ctx, tc), tc.ID)
		msg.ToolName = tc.Function.Name
		out = append(out, msg)
		if userID != 0 {
			if err := write(ctx, userID, []session.MemoryDTO{resultDTO(ctx, msg)}); err != nil {
				return nil, fmt.Errorf("工具 %s 已调用但结果写入未确认，结果未知；停止后续工具，禁止盲目重试: %w", tc.ID, err)
			}
		}
	}
	return out, nil
}

// runOne 执行单个工具调用。工具不存在或执行失败都**不炸对话** ——
// 包成"工具结果"交给模型，让它决定下一步（和 guard 流水线的失败语义一致）。
func (l *ownLoop) runOne(ctx context.Context, tc schema.ToolCall) string {
	it, ok := l.byName[tc.Function.Name]
	if !ok {
		return fmt.Sprintf("⚠️ 未知工具 %s，未执行。", tc.Function.Name)
	}
	out, err := it.InvokableRun(guard.WithCallID(ctx, tc.ID), tc.Function.Arguments)
	if err != nil {
		return fmt.Sprintf("⚠️ 工具 %s 执行失败：%v", tc.Function.Name, err)
	}
	return out
}
