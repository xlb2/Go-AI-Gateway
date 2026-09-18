package assembly_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/retry"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/subagent"
)

type runtimeStore struct {
	dependencyStore
	name       string
	events     []session.MemoryDTO
	retryReads int
}

func (s *runtimeStore) AppendEvents(_ context.Context, _ uint, events []session.MemoryDTO) error {
	s.events = append(s.events, events...)
	return nil
}
func (s *runtimeStore) AppendEvent(ctx context.Context, id uint, event session.MemoryDTO) error {
	return s.AppendEvents(ctx, id, []session.MemoryDTO{event})
}
func (s *runtimeStore) RetryAttemptsInTurn(context.Context, uint) (int, error) {
	s.retryReads++
	return 0, nil
}
func (s *runtimeStore) SearchArchival(context.Context, uint, string, int) ([]*schema.Message, error) {
	return []*schema.Message{schema.UserMessage(s.name + " archive")}, nil
}

type runtimeModel struct {
	fakeModel
	name  string
	child bool
	calls int
}

func (m *runtimeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.name+" summary", nil), nil
}
func (m *runtimeModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	if m.calls == 1 {
		return nil, errors.New("status code: 429")
	}
	if m.child {
		return streamOf(schema.AssistantMessage(m.name+" child", nil)), nil
	}
	if m.calls == 2 {
		return streamOf(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "archive", Function: schema.FunctionCall{Name: "search_memory_archive", Arguments: `{"query":"history"}`}},
			{ID: "child", Function: schema.FunctionCall{Name: "delegate_task", Arguments: `{"task":"child"}`}},
			{ID: "extra", Function: schema.FunctionCall{Name: m.name, Arguments: `{}`}},
		}}), nil
	}
	var content strings.Builder
	for _, msg := range input {
		if msg.Role == schema.Tool {
			content.WriteString(msg.Content)
		}
	}
	for _, want := range []string{m.name + " archive", m.name + " child", m.name + " extra"} {
		if !strings.Contains(content.String(), want) {
			return nil, errors.New("wrong tool dependency: " + want)
		}
	}
	return streamOf(schema.AssistantMessage(m.name+" final", nil)), nil
}

func TestRuntimeHarnessAssemblyIsIsolated(t *testing.T) {
	t.Setenv("AGENT_LOOP", "invalid")
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	instances := make([]*harness.Harness, 2)
	stores := []*runtimeStore{{name: "alpha"}, {name: "beta"}}
	var mainModels, childModels, summaries [2]int
	for i, name := range []string{"alpha", "beta"} {
		cfg := agent.RuntimeConfig{Sessions: stores[i], Approvals: &pendingStore{}, Spill: &contentStore{name: name},
			MaxSteps: 3, ChildConcurrency: 2, Retry: retry.Policy{MaxAttempts: 1},
			ExtraTools: []tool.InvokableTool{&countingTool{name: name, out: name + " extra"}},
			NewModel: func(ctx context.Context) (model.ChatModel, error) {
				child := subagent.IsNested(ctx)
				if child {
					childModels[i]++
				} else {
					mainModels[i]++
				}
				return &runtimeModel{name: name, child: child}, nil
			},
			SummaryModel: func(context.Context) (model.ChatModel, error) { summaries[i]++; return &runtimeModel{name: name}, nil },
		}
		var err error
		instances[i], err = harness.NewConfigured(ctx, cfg, sandbox.FromEnv())
		if err != nil {
			t.Fatal(err)
		}
		cfg.ExtraTools[0] = nil
	}
	for i, h := range instances {
		for round := 0; round < 2; round++ {
			out, err := h.RunAgentTurn(ctx, 7, "work", nil)
			if err != nil || out != stores[i].name+" final" {
				t.Fatalf("output=%q err=%v", out, err)
			}
		}
		store := stores[i]
		if store.summary != store.name+" summary" || !strings.Contains(store.prompt, store.name) || len(store.replies) != 2 {
			t.Fatal("summary/prompt/reply assembly mismatch")
		}
		counts := map[string]int{}
		for _, event := range store.events {
			counts[event.Type]++
		}
		if counts[session.EventLLMRetry] != 2 || counts[session.EventStepStart] != 4 || counts[session.EventToolResult] != 6 || store.retryReads != 2 {
			t.Fatalf("parent records polluted or missing: counts=%v retryReads=%d", counts, store.retryReads)
		}
		if mainModels[i] != 2 || childModels[i] != 2 || summaries[i] != 2 {
			t.Fatal("model factory lifetime mismatch")
		}
		if i == 0 && len(stores[1].events) != 0 {
			t.Fatal("other instance modified")
		}
	}
}

func TestRuntimeRejectsInvalidAssembly(t *testing.T) {
	cfg := agent.RuntimeConfig{Sessions: &runtimeStore{}, Approvals: &pendingStore{}, Spill: &contentStore{},
		MaxSteps: 2, ChildConcurrency: 1, NewModel: func(context.Context) (model.ChatModel, error) { return &runtimeModel{}, nil }}
	cfg.ExtraTools = []tool.InvokableTool{&countingTool{name: "delegate_task"}}
	if _, err := agent.NewRuntime(context.Background(), cfg); err == nil {
		t.Fatal("duplicate builtin accepted")
	}
	cfg.ExtraTools = nil
	cfg.NewModel = func(context.Context) (model.ChatModel, error) { return nil, nil }
	r, err := agent.NewRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	if _, err := r.NewLoop(ctx); err == nil {
		t.Fatal("nil model accepted")
	}
	if _, err := r.Summarize(ctx, nil); err == nil {
		t.Fatal("nil summary model accepted")
	}
}

func TestRuntimeZeroRetryDoesNotRetry(t *testing.T) {
	m := &runtimeModel{}
	cfg := agent.RuntimeConfig{Sessions: &runtimeStore{}, Approvals: &pendingStore{}, Spill: &contentStore{},
		MaxSteps: 2, ChildConcurrency: 1, NewModel: func(context.Context) (model.ChatModel, error) { return m, nil }}
	r, err := agent.NewRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	l, err := r.NewLoop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := l.Stream(ctx, nil)
	if reader != nil {
		reader.Close()
	}
	if err == nil || m.calls != 1 {
		t.Fatalf("zero retry: calls=%d err=%v", m.calls, err)
	}
}
