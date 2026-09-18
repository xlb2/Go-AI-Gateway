package harness

import (
	"context"
	"fmt"

	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
)

// Dependencies 显式实例入口。工厂每轮创建独立循环与重试预算；
// ToolNames 必须描述工厂实际提供的工具，Summarize 可使用单独的摘要模型。
type Dependencies struct {
	Sessions  session.Store
	Approvals approval.Store
	Executor  sandbox.Executor
	NewLoop   func(context.Context) (agent.Loop, error)
	Summarize session.CompactSummarizer
	ToolNames func(context.Context) []string
}

func NewWithDependencies(d Dependencies) (*Harness, error) {
	if d.Sessions == nil || d.Approvals == nil || d.Executor == nil || d.NewLoop == nil || d.Summarize == nil || d.ToolNames == nil {
		return nil, fmt.Errorf("harness requires stores, executor, loop factory, summarizer and tool names")
	}
	return &Harness{Sessions: d.Sessions, Approvals: d.Approvals, Exec: d.Executor, newLoop: d.NewLoop, summarize: d.Summarize, toolNames: d.ToolNames}, nil
}

func (h *Harness) loopFactory(ctx context.Context) (agent.Loop, error) {
	if h.newLoop != nil {
		return h.newLoop(ctx)
	}
	return agent.NewLoop(ctx)
}
func (h *Harness) summary(ctx context.Context, messages []string) (string, error) {
	if h.summarize != nil {
		return h.summarize(ctx, messages)
	}
	return agent.Summarize(ctx, messages)
}
func (h *Harness) names(ctx context.Context) []string {
	if h.toolNames != nil {
		return h.toolNames(ctx)
	}
	return agent.ToolNames(ctx)
}
