package assembly_test

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
)

func TestConfiguredLoopStepWriteBarrier(t *testing.T) {
	for _, boundary := range []string{session.EventStepStart, session.EventStepEnd} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), "user_id", uint(7))
			var progress []agent.Progress
			ctx = agent.WithProgress(ctx, func(event agent.Progress) { progress = append(progress, event) })
			counter := &countingTool{name: "read", out: "fact"}
			m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) {
				return streamOf(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "read", Arguments: `{}`}}}}), nil
			}, streamText("unexpected continuation")}}
			injected := errors.New("step persistence unavailable")
			loop, err := agent.NewConfiguredLoop(ctx, agent.LoopConfig{Model: m, Tools: []tool.InvokableTool{counter}, MaxSteps: 2,
				WriteEvents: func(_ context.Context, _ uint, events []session.MemoryDTO) error {
					for _, event := range events {
						if event.Type == boundary {
							return injected
						}
					}
					return nil
				},
				ToolResult: func(_ context.Context, msg *schema.Message) session.MemoryDTO {
					return session.MemoryDTO{Type: session.EventToolResult, Content: msg.Content, ToolCallID: msg.ToolCallID}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := loop.Stream(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainAll(t, stream)
			if !errors.Is(err, injected) {
				t.Fatalf("lost write failure: %v", err)
			}
			want := 0
			if boundary == session.EventStepEnd {
				want = 1
			}
			if len(counter.calls) != want || m.next != 1 {
				t.Fatalf("tool calls=%d model calls=%d", len(counter.calls), m.next)
			}
			if boundary == session.EventStepStart && (len(progress) != 1 || progress[0].Kind != agent.ProgressModel) {
				t.Fatalf("reported tool execution before write barrier: %+v", progress)
			}
		})
	}
}
