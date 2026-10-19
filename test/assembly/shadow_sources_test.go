package assembly_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
)

type snapshotFixture struct {
	sources []session.HistorySource
	err     error
	calls   int
}

func (s *snapshotFixture) GetHistorySources(context.Context, uint, int) ([]session.HistorySource, error) {
	s.calls++
	return s.sources, s.err
}

func TestContextShadowSourceDiagnostics(t *testing.T) {
	for _, mode := range []string{"matched", "mismatch", "unavailable", "unsupported", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			history := []*schema.Message{schema.UserMessage("same"), schema.AssistantMessage("same", nil)}
			s := &snapshotFixture{sources: []session.HistorySource{
				{ID: "first", ProjectedSource: session.ProjectedSource{Message: schema.UserMessage("same")}},
				{ID: "second", ProjectedSource: session.ProjectedSource{Message: schema.AssistantMessage("same", nil)}},
			}}
			if mode == "mismatch" {
				s.sources[1].Message.Role = schema.User
			}
			if mode == "unavailable" {
				s.err = errors.New("private failure")
			}
			var store any = s
			if mode == "unsupported" {
				store = struct{}{}
			}
			budget := 1
			if mode == "disabled" {
				budget = 0
			}
			ctx := agent.WithContextShadow(context.Background(), budget)
			ctx = agent.ObserveHistorySources(ctx, store, 1, history)
			var shadow *agent.ContextShadow
			ctx = agent.WithProgress(ctx, func(p agent.Progress) {
				if p.Shadow != nil {
					shadow = p.Shadow
				}
			})
			want := errors.New("model unavailable")
			m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) { return nil, want }}}
			loop, err := agent.NewConfiguredLoop(ctx, agent.LoopConfig{Model: m, MaxSteps: 1,
				WriteEvents: func(context.Context, uint, []session.MemoryDTO) error { return nil },
				ToolResult:  func(context.Context, *schema.Message) session.MemoryDTO { return session.MemoryDTO{} },
			})
			if err != nil {
				t.Fatal(err)
			}
			input := append([]*schema.Message{schema.SystemMessage("system")}, history...)
			input = append(input, schema.UserMessage("current"))
			if _, err := loop.Stream(ctx, input); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if mode == "disabled" {
				if shadow != nil || s.calls != 0 {
					t.Fatal("disabled observation performed work")
				}
				return
			}
			if shadow == nil || shadow.SourceStatus != mode {
				t.Fatalf("shadow=%+v", shadow)
			}
			if mode == "matched" {
				if len(shadow.Sources) != 2 || shadow.Sources[0].Index != 1 || shadow.Sources[1].ID != "second" || shadow.Sources[0].Kept || shadow.UnmappedMessages != 2 {
					t.Fatalf("mapping=%+v", shadow)
				}
			} else if len(shadow.Sources) != 0 || shadow.UnmappedMessages != 4 {
				t.Fatalf("guessed sources: %+v", shadow)
			}
			if input[1] != history[0] || input[2] != history[1] {
				t.Fatal("actual input substituted")
			}
		})
	}
}

func TestContextShadowExplicitConstraintSelection(t *testing.T) {
	input := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("confirmed older constraint"),
		schema.AssistantMessage("acknowledged", nil),
		schema.UserMessage("current request"),
	}
	plain := agent.EvaluateContextShadow(input, 0, 1)
	if !reflect.DeepEqual(plain.Kept, []int{0, 3}) {
		t.Fatalf("plain=%+v", plain)
	}
	required := agent.EvaluateContextShadowWithRequired(input, 0, 1, []int{1, 1})
	if required.Status != "required_over_budget" || required.RequiredSources != 1 || required.UnresolvedRequirements != 0 || !reflect.DeepEqual(required.Kept, []int{0, 1, 2, 3}) {
		t.Fatalf("required=%+v", required)
	}
	invalid := agent.EvaluateContextShadowWithRequired(input, 0, 1000, []int{2, 99})
	if invalid.RequiredSources != 0 || invalid.UnresolvedRequirements != 2 || !invalid.NeedsConstraintReview {
		t.Fatalf("invalid requirements accepted: %+v", invalid)
	}
}
