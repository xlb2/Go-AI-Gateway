package runstate

import (
	"context"
	"fmt"
)

// Ref is immutable attribution, not authorization or an execution status.
type Ref struct {
	UserID           uint   `json:"user_id"`
	SessionID        string `json:"session_id"`
	RunID            string `json:"run_id"`
	ParentRunID      string `json:"parent_run_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
}

type contextKey struct{}

// LegacySessionID names the existing per-user history without changing Redis keys.
func LegacySessionID(uid uint) string { return fmt.Sprintf("user:%d", uid) }

func WithRef(ctx context.Context, ref Ref) context.Context {
	return context.WithValue(ctx, contextKey{}, ref)
}
func FromContext(ctx context.Context) (Ref, bool) {
	ref, ok := ctx.Value(contextKey{}).(Ref)
	return ref, ok
}

// Bind attributes a new write to its current operation. Legacy writes may omit
// attribution; inconsistent explicit attribution must never be silently rewritten.
func Bind(ctx context.Context, uid uint, existing *Ref) (*Ref, error) {
	current, ok := FromContext(ctx)
	if existing != nil {
		if existing.UserID != uid || existing.SessionID != LegacySessionID(uid) || existing.RunID == "" {
			return nil, fmt.Errorf("invalid run attribution")
		}
		if ok && *existing != current {
			return nil, fmt.Errorf("run attribution conflicts with context")
		}
	}
	if ok {
		if current.UserID != uid || current.SessionID != LegacySessionID(uid) || current.RunID == "" {
			return nil, fmt.Errorf("run context does not belong to target session")
		}
		return &current, nil
	}
	if existing == nil {
		return nil, nil
	}
	copy := *existing
	return &copy, nil
}
