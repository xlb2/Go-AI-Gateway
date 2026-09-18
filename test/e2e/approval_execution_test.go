package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
)

func requireExecution(t *testing.T, e *env, id string, state approval.ExecutionState) *approval.Execution {
	t.Helper()
	r, err := (approval.RedisStore{}).GetExecution(e.ctx(), e.uid, id)
	if err != nil || r == nil {
		t.Fatalf("execution=%v err=%v", r, err)
	}
	if r.State != state {
		t.Fatalf("state=%s want=%s", r.State, state)
	}
	return r
}

func TestApprovalExecutionClaimEvidence(t *testing.T) {
	e := newEnv(t, 9060)
	exec := &recordingExecutor{}
	e.h = e.newHarness(exec)
	p := pendingForClaim(t, e)
	s := approval.RedisStore{}
	claimed, err := s.ClaimPending(e.ctx(), e.uid, p)
	if err != nil {
		t.Fatal(err)
	}
	r := requireExecution(t, e, claimed.ID, approval.Claimed)
	if r.Proposal.Plan.Args[0] != "hello" {
		t.Fatal("original plan lost")
	}
	if pending, err := s.GetPending(e.ctx(), e.uid); err != nil || pending != nil {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	// Simulate a process stopping immediately after claiming: a fresh harness reads evidence.
	e.h = e.newHarness(exec)
	handled, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:status "+p.ID)
	if !handled || !strings.Contains(reply, "claimed") {
		t.Fatal(reply)
	}
	_, reply = e.h.HandleApprovalCommand(e.ctx(), e.uid+1, "auth:status "+p.ID)
	if !strings.Contains(reply, "未找到") {
		t.Fatal("cross-user record exposed: " + reply)
	}
	e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+p.ConfirmCode())
	if len(exec.calls()) != 0 {
		t.Fatal("claimed action replayed")
	}
	if err := s.TransitionExecution(e.ctx(), e.uid, p.ID, approval.Claimed, approval.Running); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionExecution(e.ctx(), e.uid, p.ID, approval.Claimed, approval.Running); !errors.Is(err, approval.ErrChanged) {
		t.Fatalf("duplicate begin: %v", err)
	}
	_, reply = e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:status "+p.ID)
	if !strings.Contains(reply, "结果尚未确认") {
		t.Fatal(reply)
	}
	if err := s.TransitionExecution(e.ctx(), e.uid, p.ID, approval.Running, approval.Claimed); err == nil {
		t.Fatal("rewind allowed")
	}
}

func TestApprovalExecutionClaimStorageFailure(t *testing.T) {
	e := newEnv(t, 9061)
	exec := &recordingExecutor{}
	e.h = e.newHarness(exec)
	p := pendingForClaim(t, e)
	// Redis scripts do not roll back writes on error: evidence must be written before DEL.
	if err := e.redis.Set(e.ctx(), fmt.Sprintf("agent:approval:executions:%d", e.uid), "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+p.ConfirmCode())
	if len(exec.calls()) != 0 {
		t.Fatal("executed without evidence")
	}
	pending, err := e.h.Approvals.GetPending(e.ctx(), e.uid)
	if err != nil || pending == nil || pending.ID != p.ID {
		t.Fatalf("proposal lost: %v %v", pending, err)
	}
}

type failingExecutionStore struct {
	approval.RedisStore
	fail approval.ExecutionState
}

type canceledApprovalExecutor struct {
	recordingExecutor
	cancel context.CancelFunc
}

func (e *canceledApprovalExecutor) Run(ctx context.Context, req sandbox.Request) (sandbox.Result, error) {
	e.recordingExecutor.Run(ctx, req)
	e.cancel()
	return sandbox.Result{}, context.Canceled
}

func (s failingExecutionStore) TransitionExecution(ctx context.Context, uid uint, id string, from, to approval.ExecutionState) error {
	if to == s.fail {
		return errors.New("injected execution state failure")
	}
	return s.RedisStore.TransitionExecution(ctx, uid, id, from, to)
}

func TestApprovalExecutionWriteBarriers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fail, want approval.ExecutionState
		calls      int
	}{
		{"begin", approval.Running, approval.Claimed, 0},
		{"finish", approval.Succeeded, approval.Running, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newToolApprovalFixture(t, 9062)
			f.e.h.Approvals = failingExecutionStore{fail: tc.fail}
			before := len(f.e.fake.Requests())
			_, reply := f.e.approve()
			if !strings.Contains(reply, "状态写入未确认") || len(f.calls) != tc.calls {
				t.Fatalf("reply=%s calls=%d", reply, len(f.calls))
			}
			requireExecution(t, f.e, f.pending.ID, tc.want)
			if len(f.e.fake.Requests()) != before {
				t.Fatal("model continued after uncertain state")
			}
			f.e.h.HandleApprovalCommand(f.e.ctx(), f.e.uid, "auth:approve "+f.pending.ConfirmCode())
			if len(f.calls) != tc.calls {
				t.Fatal("effect replayed")
			}
		})
	}
}

func TestApprovalExecutionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		failure, reject, resultFailure bool
		state                          approval.ExecutionState
		calls                          int
	}{
		{"success", false, false, false, approval.Succeeded, 1},
		{"tool-error", true, false, false, approval.Unknown, 1},
		{"reject", false, true, false, approval.Rejected, 0},
		{"result-write-error", false, false, true, approval.Succeeded, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newToolApprovalFixture(t, 9063)
			f.failure = tc.failure
			if tc.resultFailure {
				f.store.fail = session.EventToolResult
			}
			if tc.reject {
				f.e.h.HandleApprovalCommand(f.e.ctx(), f.e.uid, "auth:reject")
			} else {
				f.e.approve()
			}
			requireExecution(t, f.e, f.pending.ID, tc.state)
			if len(f.calls) != tc.calls {
				t.Fatalf("calls=%d", len(f.calls))
			}
		})
	}
	t.Run("sandbox", func(t *testing.T) {
		e := newEnv(t, 9064)
		exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
		e.h = e.newHarness(exec)
		p := pendingForClaim(t, e)
		e.approve()
		requireExecution(t, e, p.ID, approval.Succeeded)
		if len(exec.calls()) != 1 {
			t.Fatal("sandbox not dispatched once")
		}
	})
	t.Run("canceled-sandbox", func(t *testing.T) {
		e := newEnv(t, 9065)
		ctx, cancel := context.WithCancel(e.ctx())
		defer cancel()
		exec := &canceledApprovalExecutor{cancel: cancel}
		e.h = e.newHarness(exec)
		p := pendingForClaim(t, e)
		e.h.HandleApprovalCommand(ctx, e.uid, "auth:approve "+p.ConfirmCode())
		requireExecution(t, e, p.ID, approval.Unknown)
		if len(exec.calls()) != 1 {
			t.Fatal("sandbox not dispatched once")
		}
	})
}
