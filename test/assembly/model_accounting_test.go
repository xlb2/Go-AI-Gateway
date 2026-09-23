package assembly_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/retry"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/subagent"
)

// Uses the real Harness -> Runtime -> retry -> main/child/summary chain.
func TestModelAccountingRuntime(t *testing.T) {
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	ctx, accounting := agent.WithModelAccounting(ctx)
	cfg := agent.RuntimeConfig{Sessions: &runtimeStore{name: "alpha"}, Approvals: &pendingStore{}, Spill: &contentStore{},
		MaxSteps: 3, ChildConcurrency: 2, Retry: retry.Policy{MaxAttempts: 1},
		ExtraTools: []tool.InvokableTool{&countingTool{name: "alpha", out: "alpha extra"}},
		NewModel: func(ctx context.Context) (model.ChatModel, error) {
			return &runtimeModel{name: "alpha", child: subagent.IsNested(ctx)}, nil
		},
		SummaryModel: func(context.Context) (model.ChatModel, error) { return &runtimeModel{name: "alpha"}, nil },
	}
	h, err := harness.NewConfigured(ctx, cfg, sandbox.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.RunAgentTurn(ctx, 7, "work", nil); err != nil {
		t.Fatal(err)
	}
	s := accounting.Snapshot()
	if s.Main.Attempts != 3 || s.Main.Errors != 1 || s.Child.Attempts != 2 || s.Child.Errors != 1 || s.Summary.Attempts != 1 {
		t.Fatalf("wrong attribution/retry counts: %+v", s)
	}
	if s.Main.WithUsage != 0 || s.Child.WithUsage != 0 || s.Summary.WithUsage != 0 {
		t.Fatalf("invented usage: %+v", s)
	}
	if s.Main.EstimatedInput == 0 || s.Child.EstimatedInput == 0 || s.Summary.EstimatedInput == 0 {
		t.Fatalf("missing estimates: %+v", s)
	}
	_, next := agent.WithModelAccounting(ctx)
	if next.Snapshot().Main.Attempts != 0 {
		t.Fatal("accounting leaked into next turn")
	}
}

type accountingModel struct {
	fakeModel
	fail bool
}

func (*accountingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "summary", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 4}}}, nil
}
func (m *accountingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "a", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 2}}},
		{Role: schema.Assistant, Content: "b", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 5}}},
	}
	if !m.fail {
		return streamOf(chunks...), nil
	}
	r, w := schema.Pipe[*schema.Message](3)
	w.Send(chunks[0], nil)
	w.Send(chunks[1], nil)
	w.Send(nil, io.ErrUnexpectedEOF)
	w.Close()
	return r, nil
}

func TestModelAccountingPartialUsage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		ctx := context.WithValue(context.Background(), "user_id", uint(7))
		ctx, accounting := agent.WithModelAccounting(ctx)
		r, err := agent.NewRuntime(ctx, agent.RuntimeConfig{Sessions: &runtimeStore{}, Approvals: &pendingStore{}, Spill: &contentStore{}, MaxSteps: 1, ChildConcurrency: 1,
			NewModel: func(context.Context) (model.ChatModel, error) { return &accountingModel{fail: fail}, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		loop, err := r.NewLoop(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := loop.Stream(ctx, []*schema.Message{schema.UserMessage("question")})
		if err != nil {
			t.Fatal(err)
		}
		text, err := drainAll(t, stream)
		if fail && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("lost stream failure: %v", err)
		}
		if !fail && (err != nil || text != "ab") {
			t.Fatalf("stream changed: %q %v", text, err)
		}
		if _, err := r.Summarize(ctx, []string{"history"}); err != nil {
			t.Fatal(err)
		}
		s := accounting.Snapshot()
		if s.Main.Attempts != 1 || s.Main.WithUsage != 1 || s.Main.Prompt != 100 || s.Main.Completion != 5 {
			t.Fatalf("usage lost/doubled: %+v", s)
		}
		if (s.Main.Errors == 1) != fail {
			t.Fatalf("wrong observed errors: %+v", s)
		}
		if s.Summary.Attempts != 1 || s.Summary.Prompt != 40 || s.Summary.Completion != 4 {
			t.Fatalf("summary not accounted: %+v", s)
		}
	}
}

func TestModelAccountingConcurrentSummary(t *testing.T) {
	ctx, accounting := agent.WithModelAccounting(context.Background())
	r, err := agent.NewRuntime(ctx, agent.RuntimeConfig{Sessions: &runtimeStore{}, Approvals: &pendingStore{}, Spill: &contentStore{}, MaxSteps: 1, ChildConcurrency: 2,
		NewModel: func(context.Context) (model.ChatModel, error) { return &accountingModel{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Summarize(ctx, []string{"history"}); err != nil {
				t.Error(err)
			}
			_ = accounting.Snapshot()
		}()
	}
	wg.Wait()
	s := accounting.Snapshot()
	if s.Summary.Attempts != 12 || s.Summary.WithUsage != 12 || s.Summary.Prompt != 480 || s.Summary.Completion != 48 || s.Main.Attempts != 0 || s.Child.Attempts != 0 {
		t.Fatalf("concurrent accounting lost or mixed calls: %+v", s)
	}
	otherCtx, other := agent.WithModelAccounting(ctx)
	if _, err := r.Summarize(otherCtx, nil); err != nil {
		t.Fatal(err)
	}
	if other.Snapshot().Summary.Attempts != 1 || accounting.Snapshot().Summary.Attempts != 12 {
		t.Fatal("collector crossed operation boundary")
	}
}
