package harness

import (
	"context"
	"fmt"
	"time"

	"go_im_gateway/internal/harness/approval"
)

func (h *Harness) transitionApproval(ctx context.Context, uid uint, id string, from, to approval.ExecutionState) error {
	store, ok := h.Approvals.(approval.ExecutionStore)
	if !ok {
		return fmt.Errorf("审批存储不支持执行状态")
	}
	return store.TransitionExecution(ctx, uid, id, from, to)
}

// Cancellation must not prevent recording what the executor already returned.
// This bounded write does not detach execution itself from the caller's context.
func (h *Harness) finishApproval(ctx context.Context, uid uint, id string, failed bool) error {
	state := approval.Succeeded
	if failed {
		state = approval.Unknown
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return h.transitionApproval(finalCtx, uid, id, approval.Running, state)
}

func (h *Harness) approvalStatus(ctx context.Context, uid uint, id string) string {
	if id == "" {
		return "请输入 auth:status <提案ID>。"
	}
	store, ok := h.Approvals.(approval.ExecutionStore)
	if !ok {
		return "审批存储不支持执行状态查询。"
	}
	record, err := store.GetExecution(ctx, uid, id)
	if err != nil {
		return fmt.Sprintf("读取审批执行状态失败：%v", err)
	}
	if record == nil {
		return "未找到该提案的执行记录；这不能证明动作从未执行。"
	}
	description := "无法识别的状态，需人工核实，禁止自动重试。"
	switch record.State {
	case approval.Claimed:
		description = "已领取，尚未确认进入执行边界；本记录不会自动恢复执行。"
	case approval.Running:
		description = "执行中或执行结果尚未确认；需核实外部副作用，禁止自动重试。"
	case approval.Succeeded:
		description = "执行入口已正常返回；不代表模型续答或会话结果回填成功。"
	case approval.Unknown:
		description = "执行入口返回错误，副作用结果需人工核实，禁止自动重试。"
	case approval.Rejected:
		description = "已拒绝，未派发执行。"
	}
	return fmt.Sprintf("提案 %s：%s\n%s", id, record.State, description)
}
