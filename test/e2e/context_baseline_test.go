package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/subagent"
	"go_im_gateway/internal/workspace"
	"go_im_gateway/test/contextbaseline"
	"go_im_gateway/test/fakemodel"
)

func TestContextBaselineHarnessReport(t *testing.T) {
	e := newEnv(t, 9173)
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "fixture answer"}})
	c, err := contextbaseline.Load("resume-dev", false)
	if err != nil {
		t.Fatal(err)
	}
	restarts := 0
	restart := func(ctx context.Context) error {
		restarts++
		var err error
		e.h, err = harness.NewFromEnv(ctx)
		return err
	}
	if err := restart(e.ctx()); err != nil {
		t.Fatal(err)
	}
	// A blank context matches the real entry; e.ctx() hid missing user_id.
	report := contextbaseline.Run(agent.WithContextShadow(context.Background(), 1), e.uid, c, restart, func(ctx context.Context, input string, emit func(string)) (string, error) {
		if ctx.Value("user_id") != e.uid {
			t.Fatal("evaluation owner missing")
		}
		return e.h.RunAgentTurn(ctx, e.uid, input, emit)
	})
	if !report.ExecutionComplete || report.Quality != "unreviewed" || restarts != 2 || len(report.Turns) != 3 {
		t.Fatalf("report: %+v restarts=%d", report, restarts)
	}
	for _, row := range report.Turns {
		if len(row.Shadows) != 1 || row.Shadows[0].Status != "mandatory_over_budget" {
			t.Fatalf("shadow missing: %+v", row.Shadows)
		}
		if row.Attempts.Main.Attempts != 1 || row.Attempts.Main.WithUsage != 1 || row.Attempts.Main.Prompt <= 0 || len(row.Requests) != 1 || row.Reply != "fixture answer" {
			t.Fatalf("missing/reset usage: %+v", row)
		}
	}
	requests := e.fake.Requests()
	second := report.Turns[1].Shadows[0]
	if second.SourceStatus != "matched" || len(second.Sources) == 0 {
		t.Fatalf("missing history sources: %+v", second)
	}
	for _, source := range second.Sources {
		page, err := (session.RedisStore{}).ReadSource(e.ctx(), e.uid, source.ID, 0, 2000)
		if err != nil || page.Text != requests[1].Messages[source.Index].Content || source.Kept {
			t.Fatalf("source drift: %+v page=%+v err=%v", source, page, err)
		}
	}
	found := false
	for _, msg := range requests[1].Messages {
		if msg.Content == c.Turns[0].Input {
			found = true
		}
	}
	if !found {
		t.Fatal("recreated Harness did not retain history")
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"fees":null`) || !strings.Contains(string(b), `"cached_tokens":null`) {
		t.Fatal("unknown costs reported as numbers")
	}
}

func TestContextBaselineShadowApplicationChangesOnlyEvaluationInput(t *testing.T) {
	e := newEnv(t, 9175)
	store := session.RedisStore{}
	for i := range 5 {
		if err := store.SaveMessage(e.ctx(), e.uid, schema.UserMessage(fmt.Sprintf("older-user-%d %s", i, strings.Repeat("旧", 1200)))); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveReply(e.ctx(), e.uid, fmt.Sprintf("older-reply-%d %s", i, strings.Repeat("答", 1200)), false); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	baseFactory := cfg.NewModel
	cfg.SummaryModel = baseFactory
	cfg.NewModel = func(modelCtx context.Context) (model.ChatModel, error) {
		base, err := baseFactory(modelCtx)
		if err != nil || subagent.IsNested(modelCtx) {
			return base, err
		}
		return contextbaseline.WrapShadowModelForEvaluation(modelCtx, base), nil
	}
	e.h, err = harness.NewConfigured(e.ctx(), cfg, sandbox.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "ok"}})
	c := contextbaseline.Case{ID: "shadow-application", Turns: []contextbaseline.Turn{{Input: "apply-shadow-current"}}}
	report := contextbaseline.RunWithOptions(agent.WithContextShadow(context.Background(), 8000), e.uid, c, nil,
		func(ctx context.Context, input string, emit func(string)) (string, error) {
			return e.h.RunAgentTurn(ctx, e.uid, input, emit)
		}, contextbaseline.RunOptions{ApplyShadow: true})
	if !report.ExecutionComplete || len(report.Turns) != 1 || len(report.Turns[0].Applications) != 1 {
		t.Fatalf("report=%+v", report)
	}
	application := report.Turns[0].Applications[0]
	if !application.Applied || application.DroppedMessages == 0 || application.SentMessages >= application.InputMessages {
		t.Fatalf("shadow candidate was not applied: %+v", application)
	}
	requests := e.fake.Requests()
	if len(requests) != 1 || len(requests[0].Messages) != application.SentMessages {
		messageCount := 0
		if len(requests) > 0 {
			messageCount = len(requests[0].Messages)
		}
		t.Fatalf("adapter requests=%d messages=%d application=%+v", len(requests), messageCount, application)
	}
	currentPresent, oldestPresent := false, false
	for _, message := range requests[0].Messages {
		currentPresent = currentPresent || message.Content == "apply-shadow-current"
		oldestPresent = oldestPresent || strings.HasPrefix(message.Content, "older-user-0 ")
	}
	if !currentPresent || oldestPresent {
		t.Fatalf("adapter input lost current turn or kept dropped old turn: current=%t oldest=%t", currentPresent, oldestPresent)
	}
}

func TestContextBaselineToolEvidence(t *testing.T) {
	e := newEnv(t, 9174)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reading.md"), []byte("private-body\nsecond\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tools, err := w.Tools()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExtraTools = append(cfg.ExtraTools, tools...)
	e.h, err = harness.NewConfigured(e.ctx(), cfg, sandbox.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	e.fake.SetScenario(fakemodel.Scenario{Rules: []fakemodel.Rule{{Match: "inspect", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{
		{Name: "read_file", Arguments: `{"path":"reading.md","start_line":1,"lines":1}`},
		{Name: "read_file", Arguments: `{"path":"reading.md","start_line":3,"lines":1}`},
	}}}}, Default: fakemodel.Reply{Text: "ok"}})
	c := contextbaseline.Case{ID: "tool-evidence", Turns: []contextbaseline.Turn{{Input: "inspect"}, {Input: "done"}}}
	r := contextbaseline.Run(context.Background(), e.uid, c, nil, func(ctx context.Context, input string, emit func(string)) (string, error) {
		return e.h.RunAgentTurn(ctx, e.uid, input, emit)
	})
	if !r.ExecutionComplete || len(r.Turns) != 2 {
		t.Fatalf("report: %+v", r)
	}
	if len(r.Turns[0].Tools) != 2 || len(r.Turns[1].Tools) != 0 {
		t.Fatalf("tool evidence not isolated: %+v", r.Turns)
	}
	seen := map[int]bool{}
	for _, event := range r.Turns[0].Tools {
		o := event.Observation
		if event.Name != "read_file" || event.Kind != agent.ProgressToolReturned || o == nil {
			t.Fatalf("event: %+v", event)
		}
		if o.Path != "reading.md" || o.FirstLine != o.LastLine || len(o.Fingerprint) != 64 || o.ContentTruncated || o.ScanLimited {
			t.Fatalf("observation: %+v", o)
		}
		if o.HasMore != (o.FirstLine == 1) {
			t.Fatalf("pagination: %+v", o)
		}
		seen[o.FirstLine] = true
	}
	if !seen[1] || !seen[3] {
		t.Fatalf("ranges: %v", seen)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-body") || strings.Contains(string(b), "start_line") {
		t.Fatal("tool body or raw arguments leaked")
	}
}
