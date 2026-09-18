package e2e

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/runstate"
	"go_im_gateway/internal/harness/session"
)

func TestRunTraceApprovalOrigin(t *testing.T) {
	f := newToolApprovalFixture(t, 9090)
	origin := f.pending.Origin
	if origin == nil || origin.RunID == "" || origin.UserID != f.e.uid || origin.SessionID != runstate.LegacySessionID(f.e.uid) {
		t.Fatalf("origin=%+v", origin)
	}
	before := f.e.log()
	for _, event := range before {
		if event.Run == nil || *event.Run != *origin {
			t.Fatalf("original event lost attribution: %+v", event)
		}
	}
	f.e.approve()
	log := f.e.log()
	var approvalRun string
	foundResult, foundAudit := false, false
	for _, event := range log[len(before):] {
		if event.Run == nil || event.Run.RunID == origin.RunID || event.Run.UserID != origin.UserID || event.Run.SessionID != origin.SessionID {
			t.Fatalf("approval event attribution: %+v", event)
		}
		if approvalRun == "" {
			approvalRun = event.Run.RunID
		}
		if event.Run.RunID != approvalRun {
			t.Fatal("approval continuation changed run")
		}
		if event.Type == session.EventToolResult && event.ToolCallID == "approval:"+f.pending.ID {
			foundResult = true
		}
		if event.Type == session.EventAudit {
			var payload struct {
				Origin     *runstate.Ref `json:"origin"`
				ProposalID string        `json:"proposal_id"`
			}
			if err := json.Unmarshal([]byte(event.Content), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Origin == nil || *payload.Origin != *origin || payload.ProposalID != f.pending.ID {
				t.Fatal("audit lost original run")
			}
			foundAudit = true
		}
	}
	if !foundResult || !foundAudit {
		t.Fatal("missing result or origin audit")
	}
	record := requireExecution(t, f.e, f.pending.ID, approval.Succeeded)
	if record.Proposal.Origin == nil || *record.Proposal.Origin != *origin {
		t.Fatal("claim lost origin")
	}
	if problems := session.ValidateLog(log); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestRunTraceFreshTurnsAndLegacyHistory(t *testing.T) {
	e := newEnv(t, 9091)
	s := session.RedisStore{}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("legacy")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{Type: session.EventTurnEnd, Role: "system", Content: "completed"}); err != nil {
		t.Fatal(err)
	}
	var err error
	e.h, err = harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	e.run("first trace")
	first := e.log()
	if first[0].Run != nil {
		t.Fatal("legacy event rewritten")
	}
	if len(first) <= 2 || first[len(first)-1].Run == nil {
		t.Fatal("first turn has no run attribution")
	}
	firstID := first[len(first)-1].Run.RunID
	e.run("second trace")
	log := e.log()
	for _, event := range log[len(first):] {
		if event.Run == nil || event.Run.RunID == firstID || event.Run.SessionID != runstate.LegacySessionID(e.uid) {
			t.Fatalf("new turn attribution=%+v", event.Run)
		}
	}
	history, err := s.GetHistory(e.ctx(), e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[0].Content != "legacy" {
		t.Fatal("legacy projection changed")
	}
}

func TestRunTraceRejectsWrongOwnerAndConflicts(t *testing.T) {
	e := newEnv(t, 9092)
	s := session.RedisStore{}
	ref := runstate.Ref{UserID: e.uid, SessionID: runstate.LegacySessionID(e.uid), RunID: "run-a"}
	ctx := runstate.WithRef(e.ctx(), ref)
	dto := session.MemoryDTO{Type: session.EventAudit, Role: "system", Content: "trace"}
	other := ref
	other.RunID = "run-b"
	conflict := dto
	conflict.Run = &other
	if err := s.AppendEvents(ctx, e.uid, []session.MemoryDTO{dto, conflict}); err == nil {
		t.Fatal("conflicting batch accepted")
	}
	if len(e.log()) != 0 {
		t.Fatal("partial conflicting batch written")
	}
	wrong := ref
	wrong.UserID++
	wrongCtx := runstate.WithRef(e.ctx(), wrong)
	if err := s.AppendEvent(wrongCtx, e.uid, dto); err == nil {
		t.Fatal("wrong user write accepted")
	}
	if err := (approval.RedisStore{}).Propose(wrongCtx, e.uid, approval.PendingAction{Action: "test"}); err == nil {
		t.Fatal("wrong proposal owner accepted")
	}
	if p, err := (approval.RedisStore{}).GetPending(e.ctx(), e.uid); err != nil || p != nil {
		t.Fatalf("unexpected proposal=%v err=%v", p, err)
	}
	if err := s.AppendEvents(ctx, e.uid, []session.MemoryDTO{dto}); err != nil {
		t.Fatal(err)
	}
	if dto.Run != nil {
		t.Fatal("writer mutated caller DTO")
	}
	log := e.log()
	if len(log) != 1 || log[0].Run == nil || *log[0].Run != ref {
		t.Fatal("valid event attribution lost")
	}
}
