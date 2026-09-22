package agent

import (
	"context"

	"go_im_gateway/internal/harness/subagent"
)

// Progress is transient UI information, not a replacement for persisted events.
// Returned means a tool returned a result; approval or business success is not implied.
type Progress struct {
	Kind string
	Tool string
}

const (
	ProgressModel        = "model"
	ProgressToolStart    = "tool-start"
	ProgressToolReturned = "tool-returned"
	ProgressToolFailed   = "tool-failed"
)

type progressKey struct{}

// WithProgress attaches a synchronous observer to this turn. It must return promptly
// and support concurrent calls. Child loops do not report into the parent's display.
func WithProgress(ctx context.Context, observe func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, observe)
}

func reportProgress(ctx context.Context, event Progress) {
	if subagent.IsNested(ctx) {
		return
	}
	if observe, ok := ctx.Value(progressKey{}).(func(Progress)); ok && observe != nil {
		observe(event)
	}
}
