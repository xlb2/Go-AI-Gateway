package harness

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/session"
)

func (h *Harness) resolveToolApproval(ctx context.Context, userID uint, p approval.PendingAction, approved bool) string {
	if h.executeTool == nil || p.Tool == nil || p.ID == "" {
		return "已拒绝执行：当前实例无法执行此工具提案，请重新发起。"
	}
	store, ok := h.Sessions.(session.EventStore)
	if !ok {
		return "已拒绝执行：会话存储不支持批量记录审批调用。"
	}
	ctx = context.WithValue(ctx, "user_id", userID)
	callID := "approval:" + p.ID
	decision := "批准"
	if !approved {
		decision = "拒绝"
	}
	// 原调用已有“等待审批”的结果；新调用对记录批准后的执行，不改写旧日志。
	if err := store.AppendEvents(ctx, userID, []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: fmt.Sprintf("%s提案 %s，原工具调用 %s；根据审批结果继续回答，不要重复发起已拒绝的调用。", decision, p.ID, p.Tool.CallID)},
		{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: callID, Name: p.Tool.Name, Arguments: p.Tool.Arguments}}},
	}); err != nil {
		return fmt.Sprintf("审批调用记录未确认，工具未执行：%v", err)
	}
	output := "审批已拒绝，工具未执行。"
	var runErr error
	if approved {
		if err := h.transitionApproval(ctx, userID, p.ID, approval.Claimed, approval.Running); err != nil {
			return fmt.Sprintf("执行状态写入未确认，工具未执行：%v", err)
		}
		output, runErr = h.executeTool(ctx, userID, p)
		if err := h.finishApproval(ctx, userID, p.ID, runErr != nil); err != nil {
			return fmt.Sprintf("工具已派发，但执行状态写入未确认，已停止续答，禁止盲目重试：%v", err)
		}
	}
	if runErr != nil {
		output = fmt.Sprintf("工具执行未成功：%v。副作用结果需核实，不要自动重试。", runErr)
	}
	dto := session.MemoryDTO{Type: session.EventToolResult, Role: "tool", ToolCallID: callID, ToolName: p.Tool.Name, Content: output}
	if h.approvalResult != nil {
		dto = h.approvalResult(ctx, &schema.Message{Role: schema.Tool, Content: output, ToolCallID: callID, ToolName: p.Tool.Name})
	}
	if err := store.AppendEvents(ctx, userID, []session.MemoryDTO{dto}); err != nil {
		return fmt.Sprintf("工具执行结果写入未确认，已停止续答，禁止盲目重试：%v", err)
	}
	history, err := store.GetHistory(ctx, userID)
	if err != nil {
		return fmt.Sprintf("执行结果已记录，但读取上下文失败：%v", err)
	}
	messages := append([]*schema.Message{schema.SystemMessage(h.buildSystemPrompt(ctx))}, history...)
	reply, err := h.runLoop(ctx, userID, messages, nil)
	if err != nil {
		return fmt.Sprintf("执行结果已记录，但模型续答失败：%v\n%s", err, reply)
	}
	if runErr != nil {
		return output + "\n" + reply
	}
	if !approved {
		return "审批已拒绝，工具未执行。\n" + reply
	}
	return "审批工具执行完成。\n" + reply
}
