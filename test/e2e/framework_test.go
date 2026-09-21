package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

// The same task crosses assembly, approval, execution, cancellation and reload.
func TestFrameworkAcceptance(t *testing.T) {
	for i, mode := range []string{"success", "reject", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, uint(9120+i))
			var calls atomic.Int32
			entered := make(chan struct{})
			publish, err := utils.InferTool("mcp__acceptance__publish", "Publish an acceptance record", func(ctx context.Context, p *approvalEchoParams) (string, error) {
				calls.Add(1)
				if p.Value != "record-9120" {
					return "", errors.New("incorrect original arguments")
				}
				if mode == "cancel" {
					close(entered)
					<-ctx.Done()
					return "", ctx.Err()
				}
				if mode == "failure" {
					return "", errors.New("publication unavailable")
				}
				return "receipt:record-9120", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := agent.RuntimeConfigFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			cfg.ExtraTools = []tool.InvokableTool{publish}
			e.h, err = harness.NewConfigured(e.ctx(), cfg, &recordingExecutor{})
			if err != nil {
				t.Fatal(err)
			}
			e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "review recorded"}, Rules: []fakemodel.Rule{
				{Match: "publish acceptance record", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "mcp__acceptance__publish", Arguments: `{"value":"record-9120"}`}}}},
			}})
			e.run("publish acceptance record")
			p, err := e.h.Approvals.GetPending(e.ctx(), e.uid)
			if err != nil || p == nil || calls.Load() != 0 {
				t.Fatalf("pending=%v calls=%d err=%v", p, calls.Load(), err)
			}
			_, prompt := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")
			code := extractConfirmCode(prompt)
			if code == "" || calls.Load() != 0 {
				t.Fatal("confirmation executed or missing code")
			}
			wantState, wantCalls, evidence := approval.Succeeded, int32(1), "receipt:record-9120"
			var reply string
			switch mode {
			case "reject":
				wantState, wantCalls, evidence = approval.Rejected, 0, "审批已拒绝"
				_, reply = e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:reject")
			case "cancel":
				wantState = approval.Unknown
				ctx, cancel := context.WithTimeout(e.ctx(), 5*time.Second)
				defer cancel()
				done := make(chan string, 1)
				go func() { _, out := e.h.HandleApprovalCommand(ctx, e.uid, "auth:approve "+code); done <- out }()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("tool did not start")
				}
				run, active := e.h.ActiveRun(e.uid)
				if !active || !e.h.CancelRun(e.uid, run.ID) {
					t.Fatal("approval run not cancellable")
				}
				select {
				case reply = <-done:
				case <-ctx.Done():
					t.Fatal("approval did not exit after cancellation")
				}
			default:
				if mode == "failure" {
					wantState, evidence = approval.Unknown, "publication unavailable"
				}
				_, reply = e.approve()
			}
			if mode != "cancel" && !strings.Contains(reply, evidence) {
				t.Fatalf("reply missing outcome: %q", reply)
			}
			if mode != "cancel" && len(e.fake.Requests()) != 3 {
				t.Fatal("model did not continue exactly once after approval resolution")
			}
			if mode == "cancel" && strings.Contains(reply, "审批工具执行完成") {
				t.Fatal("canceled execution reported success")
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
			}
			requireExecution(t, e, p.ID, wantState)
			if _, active := e.h.ActiveRun(e.uid); active {
				t.Fatal("run slot not released")
			}
			if pending, err := e.h.Approvals.GetPending(e.ctx(), e.uid); err != nil || pending != nil {
				t.Fatalf("pending=%v err=%v", pending, err)
			}

			// A new Harness must read durable evidence, without replaying the effect.
			e.h, err = harness.NewConfigured(e.ctx(), cfg, &recordingExecutor{})
			if err != nil {
				t.Fatal(err)
			}
			_, status := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:status "+p.ID)
			if !strings.Contains(status, string(wantState)) {
				t.Fatalf("reloaded status=%q", status)
			}
			e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+code)
			e.run("review recorded outcome")
			if calls.Load() != wantCalls {
				t.Fatal("history reload replayed effect")
			}
			if problems := session.ValidateLog(e.log()); len(problems) != 0 {
				t.Fatal(problems)
			}
			history, err := (session.RedisStore{}).GetHistory(e.ctx(), e.uid)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, msg := range history {
				if strings.Contains(msg.Content, evidence) {
					found = true
				}
			}
			if mode != "cancel" && !found {
				t.Fatal("durable history lost outcome")
			}
		})
	}
}

func TestFrameworkReplacement(t *testing.T) {
	e := newEnv(t, 9124)
	for i, name := range []string{"mcp__acceptance__publish", "mcp__acceptance__archive"} {
		// Separate HTTP servers prove the configured model endpoint is replaceable.
		fake := fakemodel.NewServer(fakemodel.Scenario{Default: fakemodel.Reply{Text: "ready"}, Rules: []fakemodel.Rule{
			{Match: "perform task", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: name, Arguments: `{"value":"extension"}`}}}},
		}})
		srv := httptest.NewServer(fake.Handler())
		t.Cleanup(srv.Close)
		t.Setenv("VOLC_BASE_URL", srv.URL+"/api/v3")
		cfg, err := agent.RuntimeConfigFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		extension, err := utils.InferTool(name, "Acceptance extension", func(_ context.Context, p *approvalEchoParams) (string, error) {
			calls++
			return name + ":" + p.Value, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg.ExtraTools = []tool.InvokableTool{extension}
		e.h, err = harness.NewConfigured(e.ctx(), cfg, &recordingExecutor{})
		if err != nil {
			t.Fatal(err)
		}
		e.run("perform task")
		_, reply := e.approve()
		if calls != 1 || !strings.Contains(reply, name+":extension") || len(fake.Requests()) != 3 {
			t.Fatalf("variant=%d calls=%d reply=%q requests=%d", i, calls, reply, len(fake.Requests()))
		}
		if problems := session.ValidateLog(e.log()); len(problems) != 0 {
			t.Fatal(problems)
		}
	}
	if len(e.fake.Requests()) != 0 {
		t.Fatal("replacement fell back to original model")
	}
}
