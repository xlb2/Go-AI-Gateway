package assembly_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
)

func TestContextObservation(t *testing.T) {
	var inputs []agent.InputObservation
	var usages []agent.ModelUsage
	ctx := agent.WithProgress(context.Background(), func(p agent.Progress) {
		if p.Input != nil {
			inputs = append(inputs, *p.Input)
		}
		if p.Usage != nil {
			usages = append(usages, *p.Usage)
		}
	})
	usage := &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 123, CompletionTokens: 7}}
	m := &fakeModel{steps: []streamStep{
		func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			return streamOf(&schema.Message{Role: schema.Assistant, ReasoningContent: "reasoning",
				ToolCalls: []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}}}}), nil
		},
		func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			// Repeated cumulative usage must not be added twice.
			return streamOf(&schema.Message{Role: schema.Assistant, Content: "done", ResponseMeta: usage},
				&schema.Message{Role: schema.Assistant, ResponseMeta: usage}), nil
		},
	}}
	l, err := agent.NewConfiguredLoop(ctx, agent.LoopConfig{Model: m, MaxSteps: 2,
		Tools:       []tool.InvokableTool{&countingTool{name: "lookup", out: strings.Repeat("正文", 200)}},
		WriteEvents: func(context.Context, uint, []session.MemoryDTO) error { return nil },
		ToolResult: func(_ context.Context, m *schema.Message) session.MemoryDTO {
			return session.MemoryDTO{Content: m.Content}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("query")}
	before := []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("query")}
	r, err := l.Stream(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainAll(t, r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("observation mutated input")
	}
	if len(inputs) != 2 || inputs[0].Messages != 2 || inputs[1].Messages != 4 {
		t.Fatalf("inputs: %+v", inputs)
	}
	if inputs[0].System == 0 || inputs[0].User == 0 || inputs[0].Tools <= 0 || inputs[0].ToolResults != 0 {
		t.Fatalf("first input buckets: %+v", inputs[0])
	}
	if inputs[1].ToolResults == 0 || inputs[1].Reasoning == 0 || inputs[1].Structure <= inputs[0].Structure || inputs[1].Total <= inputs[0].Total {
		t.Fatalf("tool handoff not counted: %+v", inputs)
	}
	for _, o := range inputs {
		if o.Total != o.System+o.User+o.Assistant+o.ToolResults+o.Reasoning+o.Structure+o.Tools {
			t.Fatalf("non-additive buckets: %+v", o)
		}
	}
	if len(usages) != 1 || usages[0].Prompt != 123 || usages[0].Completion != 7 {
		t.Fatalf("usage missing/doubled: %+v", usages)
	}
}

func TestContextObservationFailedRequest(t *testing.T) {
	var inputs, usages int
	ctx := agent.WithProgress(context.Background(), func(p agent.Progress) {
		if p.Input != nil {
			inputs++
		}
		if p.Usage != nil {
			usages++
		}
	})
	want := errors.New("model unavailable")
	m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) { return nil, want }}}
	l, err := agent.NewConfiguredLoop(ctx, agent.LoopConfig{Model: m, MaxSteps: 1,
		WriteEvents: func(context.Context, uint, []session.MemoryDTO) error { return nil },
		ToolResult:  func(context.Context, *schema.Message) session.MemoryDTO { return session.MemoryDTO{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Stream(ctx, []*schema.Message{schema.UserMessage("hello")}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if inputs != 1 || usages != 0 {
		t.Fatalf("failure invented usage: inputs=%d usages=%d", inputs, usages)
	}
}
