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
	"errors"
	"fmt"
	"io"
	"os"
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
	"go_im_gateway/internal/harness/tokenmeter"
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
		// 沙箱后端按 SANDBOX_BACKEND 选（默认 local 本机直跑）。
		// 注意：配了 docker 但 docker 起不来时会拿到一个"永远拒绝执行"的实现，
		// **不会**静默退回本机直跑 —— 那比不配隔离危险得多。
		Exec: sandbox.FromEnv(),
	}
}

// Default 全局默认编排实例（main 启动后即可用）。
var Default = New()

// HandleApprovalCommand 处理人在回路审批命令（auth:approve / auth:reject）。
//
// 对齐 codex 的 AskForApproval → Decision → 执行/提权 决策链：
// 审批不是打印一行日志，批准之后必须经沙箱器官真的把动作执行起来，并把真实回执返回给人。
//
// 交互是**两步**的（HARNESS-TODO 的 P2-2）：
//
//	auth:approve        → 回显完整执行计划 + 一个短确认码，**不执行**
//	auth:approve <码>   → 校验确认码之后才执行
//	auth:reject         → 一步取消（拒绝不需要慎重）
//
// 为什么要两步：敲一下 approve 就执行的话，人完全可以看都不看就批 ——
// 而这条链的另一端会真的跑命令、真的写文件。多打 6 个字符，
// 强制人看一眼"到底要执行什么"，成本极低。
//
// 有挂起动作时返回 (handled=true, 提示语)；没有则返回 (false, "")，
// 由调用方继续走正常 agent 对话。
func (h *Harness) HandleApprovalCommand(ctx context.Context, userID uint, text string) (handled bool, reply string) {
	pending, err := h.Approvals.GetPending(ctx, userID)
	switch {
	case errors.Is(err, approval.ErrExpired):
		// 超时是**显式**事件（P2-2）：必须说出来，
		// 而不是让用户以为"没有待审批任务"（人还以为提案还挂着）。
		return true, fmt.Sprintf("⌛ 刚才挂起的高危动作已超时作废（有效期 %s），没有执行任何动作。\n要执行的话请重新发起。",
			approval.TTL)
	case err != nil || pending == nil:
		return false, ""
	}

	cmd, arg := splitCommand(text)
	switch cmd {
	case "auth:approve":
		if arg == "" {
			// 第一步：只回显，不执行
			return true, approvalPrompt(*pending)
		}
		if arg != pending.ConfirmCode() {
			return true, "❌ 确认码不对，没有执行任何动作。\n再次输入 auth:approve 可以看到完整提案与当前确认码。"
		}
		h.Approvals.ClearPending(ctx, userID)
		metrics.Default.Inc("approvals_approved_total")
		outcome := h.executeApproved(ctx, userID, *pending)
		h.audit(ctx, userID, *pending, "approved", outcome)
		return true, outcome
	case "auth:reject":
		// 拒绝一步即可：不多问，因为"不做"本身就是安全的那一边
		h.Approvals.ClearPending(ctx, userID)
		metrics.Default.Inc("approvals_rejected_total")
		outcome := "审批已拒绝，动作取消（未执行任何动作）。"
		h.audit(ctx, userID, *pending, "rejected", outcome)
		return true, outcome
	default:
		return true, approvalPrompt(*pending)
	}
}

// approvalPrompt 回显待审批提案：要执行什么、什么时候作废、可以怎么回。
//
// 回显里**必须有执行计划原文** —— 人是在为"这段具体内容"背书，
// 而不是为"某个工具被调用了"背书。
func approvalPrompt(p approval.PendingAction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏸️ 有待审批的高危动作（%s）\n", p.Action)
	if p.Reason != "" {
		fmt.Fprintf(&b, "原因：%s\n", p.Reason)
	}
	fmt.Fprintf(&b, "将要执行：%s\n", p.Plan.Summary())
	if !p.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, "有效期：还剩 %s（%s 作废）\n",
			p.Remaining().Truncate(time.Second), p.ExpiresAt.Format("15:04:05"))
	}
	fmt.Fprintf(&b, "可选项：%s\n", strings.Join(p.Decisions(), " / "))
	fmt.Fprintf(&b, "确认执行请输入：auth:approve %s\n", p.ConfirmCode())
	fmt.Fprintf(&b, "取消请输入：auth:reject")
	return b.String()
}

// splitCommand 把 "auth:approve ab12cd" 拆成 ("auth:approve", "ab12cd")。
func splitCommand(text string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], fields[1]
}

// executeApproved 经沙箱器官真实执行挂起的动作，返回给人看的执行回执。
//
// 三道关，缺一不可（对应 HARNESS-TODO 的 P0-3 / P2-1）：
//  1. **计划合法性**：非法计划（缺字段、或者想把命令交给 shell 解释器）直接拒绝执行。
//     没有携带执行计划的动作也在这一关被挡下 —— 回执如实说"拒绝"，不假装执行。
//     校验放在这里，执行器内部还会再校验一次（纵深防御，换后端也照抄这条）。
//  2. **隔离要求**：策略要求隔离而当前执行器给不出 → fail-closed。
//     人批的是"沙箱内的动作"还是"裸跑"是完全不同的两件事，回执里必须写明 ——
//     不写清楚，审批链上流通的就是假信息，那比没有沙箱更危险。
//  3. **如实回执**：写清"到底执行了什么、在什么隔离条件下"，失败了也要说。
func (h *Harness) executeApproved(ctx context.Context, userID uint, p approval.PendingAction) string {
	if err := sandbox.Validate(p.Plan); err != nil {
		metrics.Default.Inc("sandbox_refused_total")
		fmt.Printf("[沙箱] 拒绝执行（执行计划非法）: %v\n", err)
		return fmt.Sprintf("⛔ 已拒绝执行（%s）：%v\n", p.Action, err)
	}

	exec := h.Exec
	if exec == nil {
		exec = sandbox.LocalExecutor{}
	}
	level := exec.Isolation()

	// fail-closed：策略要求隔离，而当前执行器给不出隔离 → 拒绝，不执行。
	//
	// 注意内置文件动作**也**受这一条约束：不是因为它自己需要隔离，
	// 而是不给审批链留任何"某种动作可以绕过要求"的口子 ——
	// 一旦开了例外，将来加新动作类型时漏判就是漏洞。
	if requireSandboxIsolation() && level == sandbox.IsolationNone {
		metrics.Default.Inc("sandbox_refused_total")
		msg := fmt.Sprintf("⛔ 已拒绝执行（%s）：当前沙箱是「%s」，隔离等级 none，"+
			"而 REQUIRE_SANDBOX_ISOLATION 要求有隔离。动作未执行。\n"+
			"要执行的话：换成有隔离的沙箱实现，或显式关掉 REQUIRE_SANDBOX_ISOLATION（并自行承担风险）。",
			p.Action, exec.Describe())
		fmt.Printf("[沙箱] 拒绝执行（fail-closed，无隔离）: %s\n", p.Plan.Summary())
		return msg
	}

	metrics.Default.Inc("sandbox_runs_total")
	start := time.Now()
	res, runErr := exec.Run(ctx, p.Plan)
	metrics.Default.ObserveDuration("sandbox_run_seconds", time.Since(start))

	var b strings.Builder
	fmt.Fprintf(&b, "审批通过，动作已真实执行（%s）。\n", p.Action)
	fmt.Fprintf(&b, "沙箱: %s｜隔离等级: %s\n", exec.Describe(), level)
	if level == sandbox.IsolationNone {
		fmt.Fprintf(&b, "⚠️ 本次执行没有任何隔离（宿主机直跑），请确认这符合预期。\n")
	}
	fmt.Fprintf(&b, "执行内容: %s\n退出码: %d\n", p.Plan.Summary(), res.ExitCode)
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
		"plan":     p.Plan.Summary(),
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

// requireSandboxIsolation 是否要求"必须有隔离"才允许执行审批通过的动作。
//
// 默认 false：保持开箱能跑（占位实现是无隔离的）。
// 生产/敏感环境应该打开 —— 打开之后，沙箱给不出隔离就 fail-closed 拒绝执行，
// 而不是"反正批都批了，跑吧"。
func requireSandboxIsolation() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REQUIRE_SANDBOX_ISOLATION"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
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
	// 埋点：每轮结束记录轮次计数 + 耗时分布；出错额外计 errors_total
	start := time.Now()
	defer func() {
		metrics.Default.Inc("turns_total")
		elapsed := time.Since(start)
		lat := elapsed.Seconds()
		// 三者各有用途，不是重复：
		//   直方图 → P50/P99（"有没有慢请求在拖后腿"只能从这里看）
		//   求和   → 总耗时（算吞吐/成本）
		//   仪表   → 最近一次（排查时先看这一眼）
		metrics.Default.ObserveHistogram("turn_latency_seconds", lat)
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

	// 2. 修复上一轮留下的"开放轮次"（进程崩溃/被 kill 时 user/message 后面什么都没有）。
	//    必须在压缩和投影之前做：否则那一轮会被当成"模型没回"，没人分得清是断了还是真没回。
	if n, err := h.Sessions.Repair(ctx, userID); err != nil {
		fmt.Printf(" [修复] 跳过本轮修复: %v\n", err)
	} else if n > 0 {
		metrics.Default.Add("turns_repaired_total", float64(n))
	}

	// 3. 上下文压缩（best-effort：早期对话压成摘要，替代粗暴截断；失败就跳过本次）
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

	// 4. 流式接收：转发给前端 + 攒完整回复；顺手收下模型返回的真实 token 用量
	//
	// **区分"正常结束"和"被切断"**：只有 io.EOF 才是正常结束。
	// 其它错误（网络断、上游断连、服务重启）都意味着这条回复是**半截的** ——
	// 不区分的话，半截回复会被当成"模型的完整回答"永久写进历史，
	// 模型之后会以为自己说过那些话，然后接着半截话往下答。
	var aiFullResponse strings.Builder
	var usage *schema.TokenUsage
	interrupted := false
	for {
		msg, err := responseStream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				interrupted = true
				fmt.Printf(" [中断] 流被切断（非正常结束）: %v\n", err)
				metrics.Default.Inc("turns_interrupted_total")
			}
			break
		}
		if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
			usage = msg.ResponseMeta.Usage
		}
		if msg.Content != "" {
			aiFullResponse.WriteString(msg.Content)
			if emit != nil {
				emit(msg.Content)
			}
		}
	}
	// 记用量：这是预算判断的唯一真实依据（估算只是估算）。
	// 落盘 + 喂给校准器：让"估算 vs 实际"的偏差被持续纠正，而不是一直拍脑袋。
	if usage != nil && usage.PromptTokens > 0 {
		estimated := tokenmeter.EstimateMessages(fullMessages)
		tokenmeter.Observe(estimated, usage.PromptTokens)
		ratio, samples := tokenmeter.Calibration()
		payload, _ := json.Marshal(map[string]any{
			"estimated_prompt":  estimated,
			"actual_prompt":     usage.PromptTokens,
			"completion":        usage.CompletionTokens,
			"total":             usage.TotalTokens,
			"calibration_ratio": ratio,
			"calibration_n":     samples,
		})
		_ = h.Sessions.AppendEvent(ctx, userID, session.MemoryDTO{
			Type: session.EventUsage, Role: "system", Content: string(payload),
		})
		metrics.Default.Add("prompt_tokens_total", float64(usage.PromptTokens))
		metrics.Default.Add("completion_tokens_total", float64(usage.CompletionTokens))
	}

	// 5. 落盘回复 + 正常收尾标记 + post 钩子（观察，不改流程）
	//
	// 收尾标记只在**正常结束**时写；被中断的轮次故意不写 turn/end ——
	// 让日志如实保留"这一轮没闭合"，下一轮的 Repair 才能认出它是断的（而不是模型没回）。
	reply := aiFullResponse.String()
	if reply != "" {
		if err := h.Sessions.SaveReply(ctx, userID, reply, interrupted); err != nil {
			return "", err
		}
	}
	if !interrupted {
		_ = h.Sessions.AppendEvent(ctx, userID, session.MemoryDTO{
			Type: session.EventTurnEnd, Role: "system", Content: "completed",
		})
	}
	hooks.RunPost(ctx, userID, reply)
	return reply, nil
}
