package assembly_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
)

func TestConfiguredLoopsKeepDependenciesSeparate(t *testing.T) {
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	for _, name := range []string{"alpha", "beta"} {
		var progress []agent.Progress
		ctx := agent.WithProgress(ctx, func(event agent.Progress) { progress = append(progress, event) })
		counter := &countingTool{name: name, out: name}
		m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			return streamOf(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: name, Function: schema.FunctionCall{Name: name, Arguments: `{}`}}}}), nil
		}, streamText(name)}}
		var events []session.MemoryDTO
		cfg := agent.LoopConfig{Model: m, Tools: []tool.InvokableTool{counter}, MaxSteps: 2,
			WriteEvents: func(_ context.Context, _ uint, batch []session.MemoryDTO) error {
				events = append(events, batch...)
				return nil
			},
			ToolResult: func(_ context.Context, msg *schema.Message) session.MemoryDTO {
				return session.MemoryDTO{Type: session.EventToolResult, Content: msg.Content, ToolCallID: msg.ToolCallID}
			},
		}
		l, err := agent.NewConfiguredLoop(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Tools[0] = &countingTool{name: "mutated"}
		r, err := l.Stream(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		text, err := drainAll(t, r)
		if err != nil || text != name || len(counter.calls) != 1 {
			t.Fatalf("%s: text=%q err=%v calls=%d", name, text, err, len(counter.calls))
		}
		if len(events) != 6 {
			t.Fatalf("expected all step/call/result events in injected writer, got %d", len(events))
		}
		if events[2].Type != session.EventToolResult || events[2].Content != name {
			t.Fatalf("wrong result: %+v", events)
		}
		want := []agent.Progress{{Kind: agent.ProgressModel}, {Kind: agent.ProgressToolStart, Tool: name}, {Kind: agent.ProgressToolReturned, Tool: name}, {Kind: agent.ProgressModel}}
		if !reflect.DeepEqual(progress, want) {
			t.Fatalf("unexpected progress: %+v", progress)
		}
	}
}

func TestConfiguredLoopRejectsMissingDependencies(t *testing.T) {
	if _, err := agent.NewConfiguredLoop(context.Background(), agent.LoopConfig{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}

type streamStep func(context.Context) (*schema.StreamReader[*schema.Message], error)
type fakeModel struct {
	steps []streamStep
	next  int
}

func (*fakeModel) BindTools([]*schema.ToolInfo) error { return nil }
func (*fakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}
func (m *fakeModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.next >= len(m.steps) {
		return nil, errors.New("unexpected model call")
	}
	step := m.steps[m.next]
	m.next++
	return step(ctx)
}
func streamOf(messages ...*schema.Message) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderFromArray(messages)
}
func streamText(text string) streamStep {
	return func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		return streamOf(schema.AssistantMessage(text, nil)), nil
	}
}

type countingTool struct {
	name, out string
	calls     []string
}

func (t *countingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}
func (t *countingTool) InvokableRun(_ context.Context, args string, _ ...tool.Option) (string, error) {
	t.calls = append(t.calls, args)
	return t.out, nil
}
func drainAll(t *testing.T, r *schema.StreamReader[*schema.Message]) (string, error) {
	t.Helper()
	defer r.Close()
	text := ""
	for {
		msg, err := r.Recv()
		if errors.Is(err, io.EOF) {
			return text, nil
		}
		if err != nil {
			return text, err
		}
		text += msg.Content
	}
}
