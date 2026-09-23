// Package contextbaseline is a small test-only replay/report helper. It invokes
// the existing Harness once per fixed user turn; it does not implement an agent loop.
package contextbaseline

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/retry"
)

//go:embed cases.json fixtures/project.md fixtures/reading.md
var corpus embed.FS

type Turn struct {
	Input         string   `json:"input"`
	RestartBefore bool     `json:"restart_before,omitempty"`
	Expect        []string `json:"expect"`
}
type Case struct {
	ID     string `json:"id"`
	Split  string `json:"split"`
	Rhythm string `json:"rhythm"`
	Turns  []Turn `json:"turns"`
}

func Load(id string, allowHoldout bool) (Case, error) {
	data, _ := corpus.ReadFile("cases.json")
	var suite struct {
		Version string `json:"version"`
		Cases   []Case `json:"cases"`
	}
	if err := json.Unmarshal(data, &suite); err != nil {
		return Case{}, err
	}
	for _, c := range suite.Cases {
		if c.ID != id {
			continue
		}
		if c.Split == "holdout" && !allowHoldout {
			return Case{}, fmt.Errorf("holdout requires explicit opt-in")
		}
		return c, nil
	}
	return Case{}, fmt.Errorf("unknown case %q", id)
}

func Fixture() []byte        { b, _ := corpus.ReadFile("fixtures/project.md"); return b }
func ReadingFixture() []byte { b, _ := corpus.ReadFile("fixtures/reading.md"); return b }
func CorpusHash() string {
	b, _ := corpus.ReadFile("cases.json")
	b = append(b, Fixture()...)
	b = append(b, ReadingFixture()...)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type TurnReport struct {
	Input       string                   `json:"input"`
	Reply       string                   `json:"reply"`
	Expect      []string                 `json:"expect"`
	Status      string                   `json:"status"`
	ErrorCode   string                   `json:"error_code,omitempty"`
	Recreated   bool                     `json:"harness_recreated"`
	ElapsedMS   int64                    `json:"elapsed_ms"`
	FirstTextMS *int64                   `json:"first_text_ms"`
	Requests    []agent.InputObservation `json:"logical_main_inputs"`
	Attempts    agent.AccountingSnapshot `json:"sdk_attempts"`
	Tools       []ToolEvidence           `json:"tool_events"`
}

// Terminal tool events only; nil observation means unavailable, not false/zero.
// This main-loop evidence excludes child loops and contains no argument/result bodies.
type ToolEvidence struct {
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	Observation *agent.ToolObservation `json:"observation"`
}
type Report struct {
	Version           string         `json:"version"`
	CaseID            string         `json:"case_id"`
	Split             string         `json:"split"`
	Rhythm            string         `json:"rhythm"`
	CorpusHash        string         `json:"corpus_sha256"`
	Started           time.Time      `json:"started_utc"`
	ExecutionComplete bool           `json:"execution_complete"`
	Quality           string         `json:"quality"`
	Fees              *float64       `json:"fees"`
	Cache             *int           `json:"cached_tokens"`
	Metadata          map[string]any `json:"metadata"`
	Turns             []TurnReport   `json:"turns"`
}

// Run stops after a failed turn. Expectations stay in the report and are never
// passed to the model. All setup turns are included in usage, not amortized away.
func Run(ctx context.Context, owner uint, c Case, restart func(context.Context) error,
	run func(context.Context, string, func(string)) (string, error)) Report {
	// Same adapter contract as cli.Run; Runtime/tools still consume this key.
	ctx = context.WithValue(ctx, "user_id", owner)
	r := Report{Version: "context-report-v1", CaseID: c.ID, Split: c.Split, Rhythm: c.Rhythm,
		CorpusHash: CorpusHash(), Started: time.Now().UTC(), Quality: "unreviewed", Turns: []TurnReport{}}
	for _, turn := range c.Turns {
		row := TurnReport{Input: turn.Input, Expect: turn.Expect, Recreated: turn.RestartBefore, Requests: []agent.InputObservation{}, Tools: []ToolEvidence{}}
		if ctx.Err() != nil {
			row.Status = "canceled"
			row.ErrorCode = errorCode(ctx.Err())
			r.Turns = append(r.Turns, row)
			return r
		}
		if turn.RestartBefore {
			if err := restart(ctx); err != nil {
				row.Status = "restart_error"
				row.ErrorCode = errorCode(err)
				r.Turns = append(r.Turns, row)
				return r
			}
		}
		turnCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		turnCtx, accounting := agent.WithModelAccounting(turnCtx)
		var mu sync.Mutex
		turnCtx = agent.WithProgress(turnCtx, func(p agent.Progress) {
			mu.Lock()
			defer mu.Unlock()
			if p.Input != nil {
				row.Requests = append(row.Requests, *p.Input)
			}
			if p.Kind == agent.ProgressToolReturned || p.Kind == agent.ProgressToolFailed {
				evidence := ToolEvidence{Kind: p.Kind, Name: p.Tool}
				if p.Observation != nil {
					copy := *p.Observation
					evidence.Observation = &copy
				}
				row.Tools = append(row.Tools, evidence)
			}
		})
		start := time.Now()
		reply, err := run(turnCtx, turn.Input, func(text string) {
			mu.Lock()
			defer mu.Unlock()
			if text != "" && row.FirstTextMS == nil {
				ms := time.Since(start).Milliseconds()
				row.FirstTextMS = &ms
			}
		})
		elapsed := time.Since(start).Milliseconds()
		interrupted := turnCtx.Err() != nil
		cancel()
		mu.Lock()
		row.Reply, row.ElapsedMS, row.Attempts = reply, elapsed, accounting.Snapshot()
		row.Status = "returned"
		if err != nil {
			row.Status = "error"
			row.ErrorCode = errorCode(err)
		}
		if interrupted {
			row.Status = "canceled_or_timeout"
		}
		r.Turns = append(r.Turns, row)
		mu.Unlock()
		if err != nil || interrupted {
			return r
		}
	}
	r.ExecutionComplete = true
	return r
}

// Only allowlisted categories, never provider error bodies, URLs or credentials.
func errorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if strings.Contains(err.Error(), "上下文中丢失 user_id") {
		return "missing_user_id"
	}
	if status := retry.StatusCode(err); status >= 100 && status <= 599 {
		return fmt.Sprintf("http_%d", status)
	}
	return "run_error"
}
