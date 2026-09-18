package harness

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"go_im_gateway/internal/harness/runstate"
)

var ErrRunBusy = errors.New("当前会话已有任务运行，请等待结束或取消该任务")

// RunInfo identifies one active operation. Until multi-session storage is added,
// UserID identifies the sole legacy session as well as its owner.
type RunInfo = runstate.Info

type activeRun struct {
	info   RunInfo
	cancel context.CancelFunc
}

type runRegistry struct {
	mu     sync.Mutex
	active map[uint]*activeRun
}

func (h *Harness) beginRun(ctx context.Context, uid uint, kind string) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	if h.runs.active[uid] != nil {
		return nil, nil, ErrRunBusy
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &activeRun{info: RunInfo{ID: uuid.NewString(), UserID: uid, SessionID: runstate.LegacySessionID(uid), Kind: kind, StartedAt: time.Now().UTC()}, cancel: cancel}
	ctx = runstate.WithRef(ctx, runstate.Ref{UserID: uid, SessionID: r.info.SessionID, RunID: r.info.ID})
	if h.runs.active == nil {
		h.runs.active = make(map[uint]*activeRun)
	}
	h.runs.active[uid] = r
	return ctx, func() {
		cancel()
		h.runs.mu.Lock()
		defer h.runs.mu.Unlock()
		if h.runs.active[uid] == r {
			delete(h.runs.active, uid)
		}
	}, nil
}

// ActiveRun returns a value copy, never the cancellation capability itself.
func (h *Harness) ActiveRun(uid uint) (RunInfo, bool) {
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	r := h.runs.active[uid]
	if r == nil {
		return RunInfo{}, false
	}
	return r.info, true
}

// CancelRun only requests cancellation of this exact run owned by uid.
// Admission stays closed until the operation actually returns, even if a tool
// ignores context cancellation. A stale ID cannot cancel a newer run.
func (h *Harness) CancelRun(uid uint, id string) bool {
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	r := h.runs.active[uid]
	if r == nil || r.info.ID != id {
		return false
	}
	r.info.CancelRequested = true
	r.cancel()
	return true
}
