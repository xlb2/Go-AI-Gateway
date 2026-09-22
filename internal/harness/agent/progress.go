package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"go_im_gateway/internal/harness/subagent"
)

// Progress is transient UI information, not a replacement for persisted events.
// Returned means a tool returned a result; approval or business success is not implied.
type Progress struct {
	Kind        string
	Tool        string
	Observation *ToolObservation
}

// ToolObservation contains bounded diagnostics, never arguments or result bodies.
type ToolObservation struct {
	Fingerprint                            string
	Elapsed                                time.Duration
	Truncated                              bool
	HasMore, ContentTruncated, ScanLimited bool
	Path                                   string
	FirstLine, LastLine                    int
}

func observeTool(name, args, out string, elapsed time.Duration) *ToolObservation {
	var value any
	if json.Unmarshal([]byte(args), &value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			args = string(canonical)
		}
	}
	digest := sha256.Sum256([]byte(name + "\x00" + args))
	o := &ToolObservation{Fingerprint: fmt.Sprintf("%x", digest), Elapsed: elapsed}
	if name != "read_file" && name != "search_files" && name != "list_files" {
		return o
	}
	var result struct {
		Truncated        bool `json:"truncated"`
		HasMore          bool `json:"has_more"`
		ContentTruncated bool `json:"content_truncated"`
		ScanLimited      bool `json:"scan_limited"`
		Lines            []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"lines"`
	}
	if json.Unmarshal([]byte(out), &result) != nil {
		return o
	}
	o.Truncated = result.Truncated
	o.HasMore, o.ContentTruncated, o.ScanLimited = result.HasMore, result.ContentTruncated, result.ScanLimited
	if name == "read_file" && len(result.Lines) > 0 {
		o.Path = result.Lines[0].Path
		o.FirstLine = result.Lines[0].Line
		o.LastLine = result.Lines[len(result.Lines)-1].Line
	}
	return o
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
