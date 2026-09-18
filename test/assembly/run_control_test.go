package assembly_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
)

type controlledRunStore struct {
	dependencyStore
	writes  atomic.Int32
	resumes atomic.Int32
}

func (s *controlledRunStore) SaveMessage(context.Context, uint, *schema.Message) error {
	s.writes.Add(1)
	return nil
}
func (*controlledRunStore) Compact(context.Context, uint, session.CompactSummarizer) error {
	return nil
}
func (s *controlledRunStore) ResumePoint(context.Context, uint) (int, bool, error) {
	s.resumes.Add(1)
	return 0, false, nil
}

type runPendingStore struct {
	approval.RedisStore
	claims  atomic.Int32
	pending approval.PendingAction
}

func (s *runPendingStore) GetPending(context.Context, uint) (*approval.PendingAction, error) {
	p := s.pending
	return &p, nil
}
func (s *runPendingStore) ClaimPending(context.Context, uint, approval.PendingAction) (*approval.PendingAction, error) {
	s.claims.Add(1)
	return nil, errors.New("unexpected claim")
}

func runControlHarness(t *testing.T, factory func(context.Context) (agent.Loop, error)) (*harness.Harness, *controlledRunStore, *runPendingStore) {
	t.Helper()
	s := &controlledRunStore{}
	p := &runPendingStore{pending: approval.PendingAction{ID: "test-proposal"}}
	h, err := harness.NewWithDependencies(harness.Dependencies{Sessions: s, Approvals: p, Executor: sandbox.LocalExecutor{},
		NewLoop: factory, Summarize: func(context.Context, []string) (string, error) { return "", nil }, ToolNames: func(context.Context) []string { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, s, p
}

func awaitRunSignal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("run did not reach expected boundary")
	}
	var zero T
	return zero
}

func TestRunControlAdmissionAndCancellation(t *testing.T) {
	entered := make(chan context.Context, 2)
	release := make(chan struct{})
	h, store, approvals := runControlHarness(t, func(ctx context.Context) (agent.Loop, error) {
		entered <- ctx
		<-release
		return nil, ctx.Err()
	})
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	defer close(release)
	done := make(chan error, 1)
	go func() { _, err := h.RunAgentTurn(parent, 71, "hello", nil); done <- err }()
	ctx := awaitRunSignal(t, entered)
	first, ok := h.ActiveRun(71)
	if !ok || first.ID == "" || first.Kind != "turn" || first.UserID != 71 {
		t.Fatalf("run=%+v", first)
	}
	writes := store.writes.Load()
	if _, err := h.RunAgentTurn(parent, 71, "overlap", nil); !errors.Is(err, harness.ErrRunBusy) {
		t.Fatalf("overlap=%v", err)
	}
	if _, _, err := h.ResumeTurn(parent, 71, nil); !errors.Is(err, harness.ErrRunBusy) {
		t.Fatalf("resume=%v", err)
	}
	for _, command := range []string{"auth:approve " + approvals.pending.ConfirmCode(), "auth:reject"} {
		if handled, reply := h.HandleApprovalCommand(parent, 71, command); !handled || !strings.Contains(reply, harness.ErrRunBusy.Error()) {
			t.Fatal(reply)
		}
	}
	if store.writes.Load() != writes || store.resumes.Load() != 0 || approvals.claims.Load() != 0 {
		t.Fatal("busy entry accessed mutable state")
	}
	if h.CancelRun(72, first.ID) || h.CancelRun(71, "stale") {
		t.Fatal("wrong owner or ID canceled run")
	}
	if ctx.Err() != nil {
		t.Fatal("wrong cancellation reached context")
	}
	if !h.CancelRun(71, first.ID) {
		t.Fatal("cancel not accepted")
	}
	awaitRunSignal(t, ctx.Done())
	active, ok := h.ActiveRun(71)
	if !ok || !active.CancelRequested {
		t.Fatal("cancel released admission too early")
	}
	if _, err := h.RunAgentTurn(parent, 71, "still busy", nil); !errors.Is(err, harness.ErrRunBusy) {
		t.Fatal(err)
	}
	release <- struct{}{}
	if err := awaitRunSignal(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, ok := h.ActiveRun(71); ok {
		t.Fatal("finished run leaked")
	}
	go func() { _, err := h.RunAgentTurn(parent, 71, "next", nil); done <- err }()
	awaitRunSignal(t, entered)
	next, _ := h.ActiveRun(71)
	if next.ID == first.ID || h.CancelRun(71, first.ID) {
		t.Fatal("old ID affects next run")
	}
	h.CancelRun(71, next.ID)
	release <- struct{}{}
	awaitRunSignal(t, done)
}

func TestRunControlIndependentUsersAndExit(t *testing.T) {
	entered := make(chan context.Context, 2)
	h, store, _ := runControlHarness(t, func(ctx context.Context) (agent.Loop, error) {
		entered <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for _, uid := range []uint{81, 82} {
		go func(uid uint) { _, err := h.RunAgentTurn(ctx, uid, "hello", nil); done <- err }(uid)
	}
	awaitRunSignal(t, entered)
	awaitRunSignal(t, entered)
	a, okA := h.ActiveRun(81)
	b, okB := h.ActiveRun(82)
	if !okA || !okB || a.ID == b.ID {
		t.Fatal("different users cannot run independently")
	}
	cancel()
	for range 2 {
		if err := awaitRunSignal(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	for _, uid := range []uint{81, 82} {
		if _, ok := h.ActiveRun(uid); ok {
			t.Fatal("parent cancellation leaked slot")
		}
	}
	writes := store.writes.Load()
	if _, err := h.RunAgentTurn(ctx, 81, "canceled", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if store.writes.Load() != writes {
		t.Fatal("canceled admission wrote history")
	}
	if _, resumed, err := h.ResumeTurn(context.Background(), 81, nil); err != nil || resumed {
		t.Fatalf("resume=%v err=%v", resumed, err)
	}
	if _, ok := h.ActiveRun(81); ok {
		t.Fatal("no-op resume leaked slot")
	}
}
