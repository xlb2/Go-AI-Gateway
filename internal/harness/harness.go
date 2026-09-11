// Package harness 编排层：把各器官拼成一个能干活的 agent，对外只暴露两个入口。
//
// 定位：整合层/编排层（用户明确的定位："不自己造零件，用干净的方式把现成好零件拼成系统"）。
// 器官清单（每个器官一个独立子包，可单独替换，见 M1 的"换实现不动主体"）：
//
//	agent    Eino 固定内核：模型适配器 + ReAct 循环 + 工具
//	session  记忆器官：事件溯源日志 + 模型历史投影
//	approval 审批器官：人在回路挂起状态机
//	hooks    钩子器官：横切需求插槽（pre 拦截 / post 观察）
//	subagent 子智能体器官：一次性/并行子 agent（delegate_task 派活、delegate_tasks 并行 fan-out、递归深度上限）
//	spill    溢出存储器官：大内容外存留定位符（store/load_large_content 工具）
//	mcp      MCP 集成器官：外部 MCP server 工具桥进统一注册表（mcp__server__tool 命名）
//	sandbox  沙箱器官（了解级）：Executor 接口 + 占位实现，生产换容器隔离
//	appserver app-server 协议器官（了解级）：JSON-RPC v1 双向契约（agent/run 流式 + approval/command）
//	metrics  可观测性器官：计数器/求和/仪表 + Prometheus 文本导出（/metrics 端点）
//
// 框架至此 11 器官齐全；沙箱是了解级骨架，生产需深化。
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/metrics"
	"go_im_gateway/internal/harness/prompt"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
)

// Harness 编排对象：持有记忆/审批/沙箱器官接口，对外提供一轮对话的唯一入口。
type Harness struct {
	Sessions  session.Store
	Approvals approval.Store
	// Exec 沙箱执行器：审批通过后的动作在这里真实执行。
	// 默认是"无隔离"的本机执行器（了解级骨架）；生产换成容器/隔离实现，上层不用改。
	Exec sandbox.Executor
}

// New 构造一个用 Redis 实现的 Harness（可替换字段注入别的实现）。
func New() *Harness {
	return &Harness{
		Sessions:  session.RedisStore{},
		Approvals: approval.RedisStore{},
		Exec:      sandbox.LocalExecutor{},
	}
}

// Default 全局默认编排实例（main 启动后即可用）。
var Default = New()

// HandleApprovalCommand 处理人在回路审批命令（auth:approve / auth:reject）。
// 有挂起的高危动作时：识别命令、真实执行/取消，返回 (handled=true, 提示语)；
// 没有挂起动作时返回 (false, "")，由调用方继续走正常 agent 对话。
//
// 对齐 codex 的 AskForApproval → Decision → 执行/提权 决策链：
// 审批不是打印一行日志，批准之后必须经沙箱器官真的把命令跑起来，并把真实回执（退出码/输出）返回给人。
func (h *Harness) HandleApprovalCommand(ctx context.Context, userID uint, text string) (handled bool, reply string) {
	pending, err := h.Approvals.GetPending(ctx, userID)
	if err != nil || pending == nil {
		return false, ""
	}
	switch strings.TrimSpace(text) {
	case "auth:approve":
		h.Approvals.ClearPending(ctx, userID)
		metrics.Default.Inc("approvals_approved_total")
		outcome := h.executeApproved(ctx, userID, *pending)
		h.audit(ctx, userID, *pending, "approved", outcome)
		return true, outcome
	case "auth:reject":
		h.Approvals.ClearPending(ctx, userID)
		metrics.Default.Inc("approvals_rejected_total")
		outcome := "审批已拒绝，动作取消（未执行任何命令）。"
		h.audit(ctx, userID, *pending, "rejected", outcome)
		return true, outcome
	default:
		return true, "系统当前有待审批的高危任务，请先输入 auth:approve 或 auth:reject。"
	}
}

// executeApproved 经沙箱器官真实执行挂起的动作，返回给人看的执行回执。
// 没有携带可执行命令的动作只回执"未执行"——不假装执行。
func (h *Harness) executeApproved(ctx context.Context, userID uint, p approval.PendingAction) string {
	if strings.TrimSpace(p.Command) == "" {
		return fmt.Sprintf("审批已通过，但该动作没有附带可执行命令（action=%s），未执行任何命令。", p.Action)
	}
	exec := h.Exec
	if exec == nil {
		exec = sandbox.LocalExecutor{}
	}
	req := sandbox.Request{
		Command: p.Command,
		Args:    p.Args,
		Workdir: p.Workdir,
		Timeout: 30 * time.Second,
	}
	metrics.Default.Inc("sandbox_runs_total")
	start := time.Now()
	res, runErr := exec.Run(ctx, req)
	metrics.Default.Add("sandbox_run_seconds_total", time.Since(start).Seconds())

	var b strings.Builder
	fmt.Fprintf(&b, "审批通过，动作已真实执行（%s）。\n", p.Action)
	fmt.Fprintf(&b, "命令: %s %s\n退出码: %d\n", p.Command, strings.Join(p.Args, " "), res.ExitCode)
	if s := strings.TrimSpace(res.Stdout); s != "" {
		fmt.Fprintf(&b, "标准输出: %s\n", s)
	}
	if s := strings.TrimSpace(res.Stderr); s != "" {
		fmt.Fprintf(&b, "标准错误: %s\n", s)
	}
	if runErr != nil {
		metrics.Default.Inc("sandbox_errors_total")
		fmt.Fprintf(&b, "执行失败: %v\n", runErr)
	}
	fmt.Printf("[物理执行] %s\n", strings.ReplaceAll(b.String(), "\n", " | "))
	return b.String()
}

// audit 把审批结果写进会话日志（log-only、不喂模型）。
// 为什么必须落盘：审批是"人介入"的动作，要能追溯谁批的、批了什么、真实结果如何。
func (h *Harness) audit(ctx context.Context, userID uint, p approval.PendingAction, decision, outcome string) {
	payload, err := json.Marshal(map[string]any{
		"decision": decision,
		"action":   p.Action,
		"param":    p.Param,
		"reason":   p.Reason,
		"command":  strings.TrimSpace(p.Command + " " + strings.Join(p.Args, " ")),
		"outcome":  outcome,
	})
	if err != nil {
		payload = []byte(fmt.Sprintf("decision=%s action=%s", decision, p.Action))
	}
	if err := h.Sessions.AppendEvent(ctx, userID, session.MemoryDTO{
		Type:    session.EventAudit,
		Role:    "system",
		Content: string(payload),
	}); err != nil {
		fmt.Printf(" [审计] 审批记录落盘失败: %v\n", err)
	}
}

// buildSystemPrompt 用 prompt 器官把系统提示组装出来（对应 HARNESS-STUDY M2）。
//
// 与"写死一坨字符串"的区别：身份 / 人设 / 工具指引各是一个带 order 的片段，
// 而且工具指引里的工具名是**从注册表实时取的**——加了工具，提示词自动跟上，
// 不用回来改这段代码（原来就是硬编码一句话，改了工具模型也不知道）。
func (h *Harness) buildSystemPrompt(ctx context.Context) string {
	vars := map[string]string{}

	b := prompt.New().
		Set("identity", prompt.OrderIdentity,
			"你是「网关保安」，一个部署在 IM 网关里的防御型 agent。").
		Set("persona", prompt.OrderPersona, `性格：极度冷酷、公事公办；不寒暄、不安抚、不说废话。
原则：
- 发现用户在愤怒抱怨、或发出攻击性指令时，绝不顺从、也不安抚，必须立刻调用 execute_system_defense 起草防御动作（它只会挂起等待管理员审批，你自己无权执行）。
- 普通聊天正常答复即可。
- 不确定的信息不要编造，先查历史。`)

	if names := agent.ToolNames(ctx); len(names) > 0 {
		vars["tools"] = strings.Join(names, "、")
		b.Set("tools", prompt.OrderTools, `可用工具：{{tools}}。
工具使用规则：
- 调用前先确认参数齐全，参数不对就不要调。
- 封禁/处置这类高危动作只能经 execute_system_defense 起草，等管理员审批。
- 内容很长时优先用 store_large_content 存起来，只把定位符留在对话里。`)
	}

	return b.Render(vars)
}

// ensureSystemPrompt 系统提示只落一次日志。
// 它每轮内容完全一样，又是 log-only（不投影），每轮重写只会白涨日志
// （实测 38 条 user 消息对应 38 条 system/prompt，1:1 纯冗余）。
func (h *Harness) ensureSystemPrompt(ctx context.Context, userID uint, msg *schema.Message) error {
	exists, err := h.Sessions.HasEvent(ctx, userID, session.EventSystemPrompt)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return h.Sessions.SaveMessage(ctx, userID, msg)
}

// RunAgentTurn 执行一轮 agent 对话（harness 主脊：领取输入→组装上下文→请求模型→执行工具→写日志）。
// 流程：pre 钩子(可拦) → 存系统提示/用户消息 → 投影历史 → Eino react 循环(流式) → 落盘回复 → post 钩子。
// emit 逐块回调流式回复（WebSocket 直接转发）；返回完整回复文本。
func (h *Harness) RunAgentTurn(ctx context.Context, userID uint, content string, emit func(chunk string)) (out string, err error) {
	// 埋点：每轮结束记录轮次计数 + 耗时；出错额外计 errors_total
	start := time.Now()
	defer func() {
		metrics.Default.Inc("turns_total")
		lat := time.Since(start).Seconds()
		metrics.Default.Add("turn_latency_seconds_total", lat)
		metrics.Default.SetGauge("last_turn_latency_seconds", lat)
		if err != nil {
			metrics.Default.Inc("errors_total")
		}
	}()

	// 0. pre 钩子：敏感词/超长等横切拦截（拦截直接返回提示语，不进 agent）
	if allow, reply := hooks.RunPre(ctx, userID, content); !allow {
		return reply, nil
	}

	// 1. 系统提示（M2 prompt 器官组装：身份/人设/工具指引分段、按 order 拼接；
	//    log-only 事件不投影；每轮内容一致，所以只写第一次）
	sysMsg := schema.SystemMessage(h.buildSystemPrompt(ctx))
	if err := h.ensureSystemPrompt(ctx, userID, sysMsg); err != nil {
		return "", err
	}

	// 2. 上下文压缩（best-effort：早期对话压成摘要，替代粗暴截断；失败就跳过本次）
	if err := h.Sessions.Compact(ctx, userID, agent.Summarize); err != nil {
		fmt.Printf(" [压缩] 跳过本次压缩: %v\n", err)
	}

	// 3. 组装上下文：系统提示 + 历史投影 + 本条用户消息
	usrMsg := schema.UserMessage(content)
	history, err := h.Sessions.GetHistory(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("读取记忆失败: %v", err)
	}
	if err := h.Sessions.SaveMessage(ctx, userID, usrMsg); err != nil {
		return "", err
	}

	fullMessages := make([]*schema.Message, 0, len(history)+2)
	fullMessages = append(fullMessages, sysMsg)
	fullMessages = append(fullMessages, history...)
	fullMessages = append(fullMessages, usrMsg)

	// 3. Eino react 循环（固定内核：模型→工具→模型，直到给出最终答案）
	agentRunner, err := agent.BuildEinoAgent(ctx)
	if err != nil {
		return "", fmt.Errorf("组装 agent 失败: %v", err)
	}
	responseStream, err := agentRunner.Stream(ctx, fullMessages)
	if err != nil {
		return "", fmt.Errorf("推流失败: %v", err)
	}

	// 4. 流式接收：转发给前端 + 攒完整回复
	var aiFullResponse strings.Builder
	for {
		msg, err := responseStream.Recv()
		if err != nil {
			break // 流结束
		}
		if msg.Content != "" {
			aiFullResponse.WriteString(msg.Content)
			if emit != nil {
				emit(msg.Content)
			}
		}
	}

	// 5. 落盘完整回复 + post 钩子（观察，不改流程）
	reply := aiFullResponse.String()
	if reply != "" {
		if err := h.Sessions.SaveMessage(ctx, userID, schema.AssistantMessage(reply, nil)); err != nil {
			return "", err
		}
	}
	hooks.RunPost(ctx, userID, reply)
	return reply, nil
}
