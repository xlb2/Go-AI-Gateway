package subagent

import (
	"context"
	"errors"
	"testing"

	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

type scriptedChild struct{ failure error }

func (s scriptedChild) Stream(context.Context, []*schema.Message, ...einoagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	r, w := schema.Pipe[*schema.Message](2)
	w.Send(schema.AssistantMessage("partial", nil), nil)
	if s.failure != nil {
		w.Send(nil, s.failure)
	}
	w.Close()
	return r, nil
}

func TestChildStreamOutcome(t *testing.T) {
	previous := runner
	t.Cleanup(func() { SetRunner(previous) })
	boom := errors.New("upstream disconnected")
	for _, failure := range []error{nil, boom, context.Canceled} {
		SetRunner(func(context.Context) (ChildAgent, error) { return scriptedChild{failure: failure}, nil })
		result, err := Run(context.Background(), "task")
		if !errors.Is(err, failure) || result == nil || result.Output != "partial" || !errors.Is(result.Err, failure) {
			t.Fatalf("failure=%v result=%+v err=%v", failure, result, err)
		}
		results := RunParallel(context.Background(), []string{"task"}, 1)
		if results[0].Output != "partial" || !errors.Is(results[0].Err, failure) {
			t.Fatalf("parallel result=%+v", results[0])
		}
	}
}
