package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/runstate"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/subagent"
	"go_im_gateway/test/fakemodel"
)

func TestChildTraceParallelIsolation(t *testing.T) {
	e := newEnv(t, 9100)
	probe, err := utils.InferTool("child_probe", "child trace", func(_ context.Context, p *approvalEchoParams) (string, error) { return p.Value, nil })
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExtraTools = []tool.InvokableTool{probe}
	e.h, err = harness.NewConfigured(e.ctx(), cfg, &recordingExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "done"}, Rules: []fakemodel.Rule{
		{Match: "parallel root", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "delegate_tasks", Arguments: `{"tasks":["child alpha","child beta"]}`}}}},
		{Match: "child alpha", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "child_probe", Arguments: `{"value":"alpha"}`}}}},
		{Match: "child beta", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "child_probe", Arguments: `{"value":"beta"}`}}}},
	}})
	e.run("parallel root")
	parent := e.log()
	if len(parent) == 0 || parent[0].Run == nil {
		t.Fatal("missing root attribution")
	}
	root := parent[0].Run.RunID
	var callID string
	for _, event := range parent {
		if event.Run == nil || event.Run.RunID != root {
			t.Fatal("child event in parent log")
		}
		if event.Type == session.EventToolCall {
			callID = event.ToolCalls[0].ID
		}
	}
	if e.countTypes()[session.EventStepStart] != 2 {
		t.Fatal("child steps polluted parent")
	}
	s := session.RedisStore{}
	ids, err := s.ChildRunIDs(e.ctx(), e.uid, root)
	if err != nil || len(ids) != 2 {
		t.Fatalf("children=%v err=%v", ids, err)
	}
	prompts := map[string]bool{}
	for _, id := range ids {
		log, err := s.GetChildLog(e.ctx(), e.uid, id)
		if err != nil || len(log) == 0 {
			t.Fatalf("child log=%v err=%v", log, err)
		}
		if problems := session.ValidateLog(log); len(problems) != 0 {
			t.Fatal(problems)
		}
		prompts[log[0].Content] = true
		steps, calls, results := 0, 0, 0
		for _, event := range log {
			if event.Run == nil || event.Run.RunID != id || event.Run.ParentRunID != root || event.Run.ParentToolCallID != callID {
				t.Fatalf("child attribution=%+v", event.Run)
			}
			switch event.Type {
			case session.EventStepStart:
				steps++
			case session.EventToolCall:
				calls++
			case session.EventToolResult:
				results++
			}
		}
		if steps != 2 || calls != 1 || results != 1 || log[len(log)-1].Type != session.EventTurnEnd {
			t.Fatal("incomplete child lifecycle")
		}
		if other, err := s.GetChildLog(e.ctx(), e.uid+1, id); err != nil || len(other) != 0 {
			t.Fatal("cross-user child log exposed")
		}
	}
	if !prompts["child alpha"] || !prompts["child beta"] {
		t.Fatal("parallel child inputs mixed")
	}
}

type traceChild struct{}

func (traceChild) Stream(context.Context, []*schema.Message, ...einoagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child done", nil)}), nil
}

func TestChildTracePersistenceFailures(t *testing.T) {
	for _, fail := range []string{"start", "finish", "factory"} {
		t.Run(fail, func(t *testing.T) {
			e := newEnv(t, 9101)
			s := session.RedisStore{}
			ctx := runstate.WithRef(e.ctx(), runstate.Ref{UserID: e.uid, SessionID: runstate.LegacySessionID(e.uid), RunID: "root"})
			ctx = guard.WithCallID(ctx, "delegate-call")
			ctx = subagent.WithRecorder(ctx, func(ctx context.Context, uid uint, events []session.MemoryDTO) error {
				if fail == "start" && events[0].Type == session.EventUserMessage || fail == "finish" && events[0].Type == session.EventAssistantMessage {
					return errors.New("injected child write failure")
				}
				return s.AppendChildEvents(ctx, uid, events)
			})
			calls := 0
			r := subagent.Runner(func(context.Context) (subagent.ChildAgent, error) {
				calls++
				if fail == "factory" {
					return nil, errors.New("factory failed")
				}
				return traceChild{}, nil
			})
			result, err := r.Run(ctx, "child")
			if err == nil {
				t.Fatal("failed child reported success")
			}
			if fail == "start" && calls != 0 {
				t.Fatal("model constructed without child log")
			}
			if result != nil && result.Err == nil {
				t.Fatal("result hides persistence failure")
			}
			ids, readErr := s.ChildRunIDs(e.ctx(), e.uid, "root")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if fail == "start" {
				if len(ids) != 0 {
					t.Fatal("unexpected child index")
				}
				return
			}
			if len(ids) != 1 {
				t.Fatal(ids)
			}
			log, readErr := s.GetChildLog(e.ctx(), e.uid, ids[0])
			if readErr != nil || len(log) == 0 {
				t.Fatalf("log=%v err=%v", log, readErr)
			}
			if fail == "factory" {
				var end struct {
					Reason string `json:"reason"`
				}
				if err := json.Unmarshal([]byte(log[len(log)-1].Content), &end); err != nil || end.Reason != "interrupted" {
					t.Fatal("factory failure not recorded")
				}
			}
			if fail == "finish" && log[len(log)-1].Type == session.EventTurnEnd {
				t.Fatal("unconfirmed finish appeared completed")
			}
			if len(e.log()) != 0 {
				t.Fatal("child recorder wrote parent history")
			}
		})
	}
}

func TestChildTraceCancellation(t *testing.T) {
	e := newEnv(t, 9102)
	s := session.RedisStore{}
	ctx := runstate.WithRef(e.ctx(), runstate.Ref{UserID: e.uid, SessionID: runstate.LegacySessionID(e.uid), RunID: "root"})
	ctx = subagent.WithRecorder(guard.WithCallID(ctx, "delegate-call"), s.AppendChildEvents)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	r := subagent.Runner(func(ctx context.Context) (subagent.ChildAgent, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go func() { _, err := r.Run(ctx, "child"); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("child cancellation did not finish")
	}
	ids, err := s.ChildRunIDs(e.ctx(), e.uid, "root")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	log, err := s.GetChildLog(e.ctx(), e.uid, ids[0])
	if err != nil || len(log) < 3 {
		t.Fatalf("log=%v err=%v", log, err)
	}
	var end struct {
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(log[len(log)-1].Content), &end); err != nil || end.Reason != "interrupted" || end.Detail != "aborted" {
		t.Fatal("cancellation not durably distinguished")
	}
}
