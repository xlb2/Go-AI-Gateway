package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

type approvalEchoParams struct {
	Value string `json:"value" jsonschema:"required"`
}
type approvalEventStore struct {
	session.RedisStore
	fail string
}

func (s *approvalEventStore) AppendEvents(ctx context.Context, id uint, batch []session.MemoryDTO) error {
	for _, event := range batch {
		if s.fail != "" && event.Type == s.fail {
			return errors.New("injected persistence failure")
		}
	}
	return s.RedisStore.AppendEvents(ctx, id, batch)
}

type toolApprovalFixture struct {
	e         *env
	cfg       agent.RuntimeConfig
	store     *approvalEventStore
	calls     []string
	failure   bool
	arguments string
	pending   approval.PendingAction
}

func newToolApprovalFixture(t *testing.T, uid uint) *toolApprovalFixture {
	t.Helper()
	f := &toolApprovalFixture{e: newEnv(t, uid), store: &approvalEventStore{}}
	probe, err := utils.InferTool("mcp__approval__echo", "approval fixture", func(_ context.Context, p *approvalEchoParams) (string, error) {
		f.calls = append(f.calls, p.Value)
		if f.failure {
			return "", errors.New("fixture tool failure")
		}
		return "approved-result", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cfg, err = agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Sessions = f.store
	f.cfg.ExtraTools = []tool.InvokableTool{probe}
	f.e.h, err = harness.NewConfigured(f.e.ctx(), f.cfg, &recordingExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(approvalEchoParams{Value: strings.Repeat("x", 400) + "-tail"})
	if err != nil {
		t.Fatal(err)
	}
	f.arguments = string(data)
	f.e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "waiting"}, Rules: []fakemodel.Rule{
		{Match: "request approval", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "mcp__approval__echo", Arguments: f.arguments}}}},
	}})
	f.e.run("request approval")
	p, err := f.e.h.Approvals.GetPending(f.e.ctx(), uid)
	if err != nil || p == nil {
		t.Fatalf("pending=%v err=%v", p, err)
	}
	f.pending = *p
	if len(f.calls) != 0 {
		t.Fatal("tool executed before approval")
	}
	return f
}

func TestToolApprovalFullCallAndContinuation(t *testing.T) {
	f := newToolApprovalFixture(t, 9050)
	p := f.pending
	if p.Kind != approval.KindTool || p.Tool == nil || p.Tool.Arguments != f.arguments || p.Tool.CallID == "" || p.Tool.RuntimeID == "" || p.Tool.UserID != f.e.uid {
		t.Fatalf("incomplete proposal: %+v", p)
	}
	if len([]rune(p.Param)) > 201 {
		t.Fatal("display summary not truncated")
	}
	_, prompt := f.e.h.HandleApprovalCommand(f.e.ctx(), f.e.uid, "auth:approve")
	if !strings.Contains(prompt, f.arguments) {
		t.Fatal("confirmation omitted full arguments")
	}
	handled, reply := f.e.approve()
	if !handled || !strings.Contains(reply, "approved-result") || len(f.calls) != 1 || f.calls[0] != strings.Repeat("x", 400)+"-tail" {
		t.Fatalf("reply=%q calls=%v", reply, f.calls)
	}
	before := len(f.e.fake.Requests())
	handled, _ = f.e.h.HandleApprovalCommand(f.e.ctx(), f.e.uid, "auth:approve "+p.ConfirmCode())
	if !handled || len(f.calls) != 1 || len(f.e.fake.Requests()) != before {
		t.Fatal("repeat approval was replayed")
	}
	log := f.e.log()
	if problems := session.ValidateLog(log); len(problems) != 0 {
		t.Fatal(problems)
	}
	resultFound := false
	for _, event := range log {
		if event.Type == session.EventToolResult && event.ToolCallID == "approval:"+p.ID && event.Content == "approved-result" {
			resultFound = true
		}
	}
	if !resultFound {
		t.Fatal("approved result missing from paired history")
	}
	// Approval did not permanently trust this tool or server.
	f.e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "waiting"}, Rules: []fakemodel.Rule{{Match: "again", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: p.Tool.Name, Arguments: f.arguments}}}}}})
	f.e.run("again")
	next, err := f.e.h.Approvals.GetPending(f.e.ctx(), f.e.uid)
	if err != nil || next == nil || next.ID == p.ID || len(f.calls) != 1 {
		t.Fatal("approval leaked to next call")
	}
}

func TestToolApprovalRejectAndFailure(t *testing.T) {
	for _, reject := range []bool{true, false} {
		t.Run(map[bool]string{true: "reject", false: "failure"}[reject], func(t *testing.T) {
			f := newToolApprovalFixture(t, 9051)
			f.failure = true
			var reply string
			if reject {
				_, reply = f.e.h.HandleApprovalCommand(f.e.ctx(), f.e.uid, "auth:reject")
			} else {
				_, reply = f.e.approve()
			}
			wantCalls, wantText := 1, "fixture tool failure"
			if reject {
				wantCalls, wantText = 0, "审批已拒绝"
			}
			if len(f.calls) != wantCalls || !strings.Contains(reply, wantText) || len(f.e.fake.Requests()) != 3 {
				t.Fatalf("reply=%q calls=%d requests=%d", reply, len(f.calls), len(f.e.fake.Requests()))
			}
			if problems := session.ValidateLog(f.e.log()); len(problems) != 0 {
				t.Fatal(problems)
			}
		})
	}
}

func TestToolApprovalPersistenceBarriers(t *testing.T) {
	for _, stage := range []string{session.EventToolCall, session.EventToolResult} {
		t.Run(stage, func(t *testing.T) {
			f := newToolApprovalFixture(t, 9052)
			f.store.fail = stage
			before := len(f.e.fake.Requests())
			_, reply := f.e.approve()
			want := 0
			if stage == session.EventToolResult {
				want = 1
			}
			if len(f.calls) != want || len(f.e.fake.Requests()) != before || !strings.Contains(reply, "未确认") {
				t.Fatalf("reply=%q calls=%d", reply, len(f.calls))
			}
			f.e.h.HandleApprovalCommand(f.e.ctx(), f.e.uid, "auth:approve "+f.pending.ConfirmCode())
			if len(f.calls) != want {
				t.Fatal("unconfirmed execution replayed")
			}
		})
	}
}

func TestToolApprovalRejectsDifferentRuntime(t *testing.T) {
	f := newToolApprovalFixture(t, 9053)
	var err error
	f.e.h, err = harness.NewConfigured(f.e.ctx(), f.cfg, &recordingExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	_, reply := f.e.approve()
	if len(f.calls) != 0 || !strings.Contains(reply, "不匹配") {
		t.Fatalf("reply=%q calls=%d", reply, len(f.calls))
	}
}

func TestToolApprovalProposalCannotOverwrite(t *testing.T) {
	f := newToolApprovalFixture(t, 9054)
	s := approval.RedisStore{}
	if err := s.Propose(f.e.ctx(), f.e.uid, approval.PendingAction{Action: "replacement"}); !errors.Is(err, approval.ErrPending) {
		t.Fatalf("overwrite: %v", err)
	}
	p, err := s.GetPending(f.e.ctx(), f.e.uid)
	if err != nil || p == nil || p.ID != f.pending.ID {
		t.Fatal("original proposal lost")
	}
	if err := s.SetPending(f.e.ctx(), f.e.uid, approval.PendingAction{Action: "expired", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Propose(f.e.ctx(), f.e.uid, approval.PendingAction{Action: "fresh"}); err != nil {
		t.Fatalf("expired proposal blocks new request: %v", err)
	}
	p, err = s.GetPending(f.e.ctx(), f.e.uid)
	if err != nil || p == nil || p.Action != "fresh" {
		t.Fatalf("fresh proposal: %v", err)
	}
}
