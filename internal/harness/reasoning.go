package harness

import "context"

type reasoningKey struct{}

// WithReasoning observes provider reasoning deltas separately from answer text.
// The callback must return promptly. Reasoning is not saved as an assistant reply.
func WithReasoning(ctx context.Context, emit func(string)) context.Context {
	return context.WithValue(ctx, reasoningKey{}, emit)
}

func emitReasoning(ctx context.Context, text string) {
	if emit, ok := ctx.Value(reasoningKey{}).(func(string)); ok && emit != nil && text != "" {
		emit(text)
	}
}
