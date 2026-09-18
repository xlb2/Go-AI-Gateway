package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
)

type simultaneousReads struct {
	approval.RedisStore
	barrier sync.WaitGroup
}

func (s *simultaneousReads) GetPending(ctx context.Context, id uint) (*approval.PendingAction, error) {
	p, err := s.RedisStore.GetPending(ctx, id)
	s.barrier.Done()
	s.barrier.Wait()
	return p, err
}

func pendingForClaim(t *testing.T, e *env) approval.PendingAction {
	t.Helper()
	s := approval.RedisStore{}
	if err := s.SetPending(e.ctx(), e.uid, approval.PendingAction{Action: "diagnostic", Plan: sandbox.Request{Command: "echo", Args: []string{"hello"}}}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPending(e.ctx(), e.uid)
	if err != nil || p == nil {
		t.Fatalf("pending=%v err=%v", p, err)
	}
	return *p
}

func TestApprovalClaimConcurrentConfirmation(t *testing.T) {
	e := newEnv(t, 9042)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)
	p := pendingForClaim(t, e)
	store := &simultaneousReads{}
	const callers = 16
	store.barrier.Add(callers)
	e.h.Approvals = store
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+p.ConfirmCode())
		}()
	}
	wg.Wait()
	if got := len(exec.calls()); got != 1 {
		t.Fatalf("executions=%d, want 1", got)
	}
	if got := e.countTypes()[session.EventAudit]; got != 1 {
		t.Fatalf("audit events=%d, want 1", got)
	}
}

func TestApprovalClaimRejectRacesWithApprove(t *testing.T) {
	e := newEnv(t, 9043)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)
	p := pendingForClaim(t, e)
	store := &simultaneousReads{}
	store.barrier.Add(2)
	e.h.Approvals = store
	var wg sync.WaitGroup
	wg.Add(2)
	for _, command := range []string{"auth:approve " + p.ConfirmCode(), "auth:reject"} {
		go func(command string) {
			defer wg.Done()
			e.h.HandleApprovalCommand(e.ctx(), e.uid, command)
		}(command)
	}
	wg.Wait()
	if got := len(exec.calls()); got > 1 {
		t.Fatalf("executions=%d", got)
	}
	if got := e.countTypes()[session.EventAudit]; got != 1 {
		t.Fatalf("both decisions accepted: audit events=%d", got)
	}
	for _, event := range e.log() {
		if event.Type != session.EventAudit {
			continue
		}
		var record struct {
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal([]byte(event.Content), &record); err != nil {
			t.Fatal(err)
		}
		want := 0
		if record.Decision == "approved" {
			want = 1
		} else if record.Decision != "rejected" {
			t.Fatal(record.Decision)
		}
		if len(exec.calls()) != want {
			t.Fatalf("decision=%s executions=%d", record.Decision, len(exec.calls()))
		}
	}
}

func TestApprovalClaimRejectsStaleSnapshot(t *testing.T) {
	e := newEnv(t, 9044)
	s := approval.RedisStore{}
	old := pendingForClaim(t, e)
	if err := s.SetPending(e.ctx(), e.uid, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPending(e.ctx(), e.uid, old); !errors.Is(err, approval.ErrChanged) {
		t.Fatalf("stale claim: %v", err)
	}
	current, err := s.GetPending(e.ctx(), e.uid)
	if err != nil || current == nil {
		t.Fatalf("replacement lost: %v", err)
	}
	if old.ID == current.ID {
		t.Fatal("replacement reused proposal identity")
	}
	// A caller cannot alter the stored plan by mutating its local copy.
	current.Plan.Args = []string{"mutated"}
	claimed, err := s.ClaimPending(e.ctx(), e.uid, *current)
	if err != nil || claimed == nil || claimed.Plan.Args[0] != "hello" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if _, err := s.ClaimPending(e.ctx(), e.uid, *current); !errors.Is(err, approval.ErrChanged) {
		t.Fatalf("repeat claim: %v", err)
	}
}

func TestApprovalClaimChecksExpiryAtConsumption(t *testing.T) {
	e := newEnv(t, 9045)
	s := approval.RedisStore{}
	deadline := time.Now().Add(time.Second)
	if err := s.SetPending(e.ctx(), e.uid, approval.PendingAction{Action: "expires", ExpiresAt: deadline}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPending(e.ctx(), e.uid)
	if err != nil || p == nil {
		t.Fatalf("read pending: %v", err)
	}
	time.Sleep(time.Until(deadline) + 10*time.Millisecond)
	if _, err := s.ClaimPending(e.ctx(), e.uid, *p); !errors.Is(err, approval.ErrExpired) {
		t.Fatalf("expiry claim: %v", err)
	}
}

type failingClaim struct{ approval.RedisStore }

func (failingClaim) ClaimPending(context.Context, uint, approval.PendingAction) (*approval.PendingAction, error) {
	return nil, errors.New("injected claim failure")
}

type legacyApprovalStore struct{ approval.Store }

func TestApprovalClaimFailureNeverExecutes(t *testing.T) {
	e := newEnv(t, 9046)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)
	p := pendingForClaim(t, e)
	for _, store := range []approval.Store{failingClaim{}, legacyApprovalStore{approval.RedisStore{}}} {
		e.h.Approvals = store
		handled, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+p.ConfirmCode())
		if !handled || !strings.Contains(reply, "未获执行权") || len(exec.calls()) != 0 {
			t.Fatalf("handled=%v reply=%s", handled, reply)
		}
	}
	if p, err := (approval.RedisStore{}).GetPending(e.ctx(), e.uid); err != nil || p == nil {
		t.Fatalf("failed claim consumed proposal: %v", err)
	}
}
