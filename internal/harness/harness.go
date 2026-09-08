// Package harness 编排层：把各器官拼成一个能干活的 agent，对外只暴露两个入口。
//
// 定位：整合层/编排层（用户明确的定位："不自己造零件，用干净的方式把现成好零件拼成系统"）。
// 器官清单（每个器官一个独立子包，可单独替换，见 M1 的"换实现不动主体"）：
//
//	agent    Eino 固定内核：模型适配器 + ReAct 循环 + 工具
//	session  记忆器官：事件溯源日志 + 模型历史投影
//	approval 审批器官：人在回路挂起状态机
//	hooks    钩子器官：横切需求插槽（pre 拦截 / post 观察）
//	subagent 子智能体器官：一次性子 agent（delegate_task 工具派活，上下文不回流）
//	spill    溢出存储器官：大内容外存留定位符（store/load_large_content 工具）
//	mcp      MCP 集成器官：外部 MCP server 工具桥进统一注册表（mcp__server__tool 命名）
//	sandbox  沙箱器官（了解级）：Executor 接口 + 占位实现，生产换容器隔离
//	appserver app-server 协议器官（了解级）：JSON-RPC v1 双向契约（agent/run + approval/command）
//
// 框架至此 10 器官齐全；沙箱/app-server 是了解级骨架，生产需深化。
package harness

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/session"
)

// Harness 编排对象：持有记忆/审批器官接口，对外提供一轮对话的唯一入口。
type Harness struct {
	Sessions  session.Store
	Approvals approval.Store
}

// New 构造一个用 Redis 实现的 Harness（可替换字段注入别的实现）。
func New() *Harness {
	return &Harness{
		Sessions:  session.RedisStore{},
		Approvals: approval.RedisStore{},
	}
}

// Default 全局默认编排实例（main 启动后即可用）。
var Default = New()

// HandleApprovalCommand 处理人在回路审批命令（auth:approve / auth:reject）。
// 有挂起的高危动作时：识别命令并执行/取消，返回 (handled=true, 提示语)；
// 没有挂起动作时返回 (false, "")，由调用方继续走正常 agent 对话。
func (h *Harness) HandleApprovalCommand(ctx context.Context, userID uint, text string) (handled bool, reply string) {
	pending, err := h.Approvals.GetPending(ctx, userID)
	if err != nil || pending == nil {
		return false, ""
	}
	switch strings.TrimSpace(text) {
	case "auth:approve":
		h.Approvals.ClearPending(ctx, userID)
		fmt.Printf("[物理执行] 防御系统已启动！触发因素: %s\n", pending.Param)
		return true, "审批通过，防御系统已物理激活。"
	case "auth:reject":
		h.Approvals.ClearPending(ctx, userID)
		return true, "审批已拒绝，动作取消。"
	default:
		return true, "系统当前有待审批的高危任务，请先输入 auth:approve 或 auth:reject。"
	}
}

// RunAgentTurn 执行一轮 agent 对话（harness 主脊：领取输入→组装上下文→请求模型→执行工具→写日志）。
// 流程：pre 钩子(可拦) → 存系统提示/用户消息 → 投影历史 → Eino react 循环(流式) → 落盘回复 → post 钩子。
// emit 逐块回调流式回复（WebSocket 直接转发）；返回完整回复文本。
func (h *Harness) RunAgentTurn(ctx context.Context, userID uint, content string, emit func(chunk string)) (string, error) {
	// 0. pre 钩子：敏感词/超长等横切拦截（拦截直接返回提示语，不进 agent）
	if allow, reply := hooks.RunPre(ctx, userID, content); !allow {
		return reply, nil
	}

	// 1. 系统提示（log-only 事件，不投影）
	sysMsg := schema.SystemMessage(`你是一个极其冷酷的网关保安。
如果发现用户在愤怒抱怨、或者发出攻击性指令，不要安抚！必须立刻调用 execute_system_defense 工具！
如果只是普通聊天，正常回复即可。`)
	if err := h.Sessions.SaveMessage(ctx, userID, sysMsg); err != nil {
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
