package appserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/appserver"
	"go_im_gateway/internal/harness/runstate"
)

var _ appserver.RunController = (*harness.Harness)(nil)

type controlledRunner struct {
	mu       sync.Mutex
	info     runstate.Info
	cancel   context.CancelFunc
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func newRunner() *controlledRunner {
	return &controlledRunner{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
}

func (r *controlledRunner) RunAgentTurn(ctx context.Context, uid uint, _ string, emit func(string)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.info, r.cancel = runstate.Info{ID: "run-one", UserID: uid, Kind: "turn"}, cancel
	r.mu.Unlock()
	close(r.entered)
	if emit != nil {
		for range 16 {
			emit("chunk")
		}
	}
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	r.mu.Lock()
	r.cancel = nil
	r.mu.Unlock()
	close(r.finished)
	return "", ctx.Err()
}

func (r *controlledRunner) HandleApprovalCommand(ctx context.Context, uid uint, _ string) (bool, string) {
	_, err := r.RunAgentTurn(ctx, uid, "", nil)
	return true, err.Error()
}

func (r *controlledRunner) ActiveRun(uid uint) (runstate.Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info, r.cancel != nil && r.info.UserID == uid
}

func (r *controlledRunner) CancelRun(uid uint, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil || r.info.UserID != uid || r.info.ID != id {
		return false
	}
	r.info.CancelRequested = true
	r.cancel()
	return true
}

func connect(t *testing.T, runner appserver.AgentRunner, uid uint) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		appserver.ServeWS(req.Context(), conn, runner, uid)
	}))
	t.Cleanup(s.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func send(t *testing.T, c *websocket.Conn, id int64, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := c.WriteJSON(appserver.Message{Version: "v1", ID: &id, Method: method, Params: raw}); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, c *websocket.Conn, id int64) appserver.Message {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var msg appserver.Message
		if err := c.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg.Method == "agent/chunk" {
			var p struct {
				RequestID int64 `json:"request_id"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil || p.RequestID != 1 {
				t.Fatalf("uncorrelated chunk: %s", msg.Params)
			}
			continue
		}
		if msg.ID == nil || *msg.ID != id {
			t.Fatalf("unexpected response: %+v", msg)
		}
		return msg
	}
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not reach boundary")
	}
}

func TestRPCControlDuringRun(t *testing.T) {
	r := newRunner()
	defer close(r.release)
	c := connect(t, r, 9)
	send(t, c, 1, "agent/run", map[string]string{"content": "hello"})
	await(t, r.entered)
	send(t, c, 2, "agent/status", nil)
	status := receive(t, c, 2)
	if !strings.Contains(string(status.Result), `"run_id":"run-one"`) {
		t.Fatal(string(status.Result))
	}
	send(t, c, 3, "agent/run", map[string]string{"content": "overlap"})
	if msg := receive(t, c, 3); msg.Error == nil || msg.Error.Code != -32001 {
		t.Fatalf("busy=%+v", msg)
	}
	other := connect(t, r, 10)
	send(t, other, 4, "agent/cancel", map[string]string{"run_id": "run-one"})
	if msg := receive(t, other, 4); string(msg.Result) != `{"cancel_requested":false}` {
		t.Fatal(string(msg.Result))
	}
	send(t, c, 5, "agent/cancel", map[string]string{"run_id": "stale"})
	if msg := receive(t, c, 5); string(msg.Result) != `{"cancel_requested":false}` {
		t.Fatal(string(msg.Result))
	}
	send(t, c, 6, "agent/cancel", map[string]string{"run_id": "run-one"})
	if msg := receive(t, c, 6); string(msg.Result) != `{"cancel_requested":true}` {
		t.Fatal(string(msg.Result))
	}
	await(t, r.canceled)
	send(t, c, 7, "agent/status", nil)
	if msg := receive(t, c, 7); !strings.Contains(string(msg.Result), `"cancel_requested":true`) {
		t.Fatal(string(msg.Result))
	}
	r.release <- struct{}{}
	if msg := receive(t, c, 1); msg.Error == nil {
		t.Fatal("canceled run returned success")
	}
	send(t, c, 8, "agent/status", nil)
	if msg := receive(t, c, 8); string(msg.Result) != `{"active":false}` {
		t.Fatal(string(msg.Result))
	}
}

func TestRPCDisconnectCancelsOperation(t *testing.T) {
	for _, method := range []string{"agent/run", "approval/command"} {
		t.Run(method, func(t *testing.T) {
			r := newRunner()
			defer close(r.release)
			c := connect(t, r, 9)
			send(t, c, 1, method, map[string]string{"content": "hello", "text": "auth:approve code"})
			await(t, r.entered)
			c.Close()
			await(t, r.canceled)
			r.release <- struct{}{}
			await(t, r.finished)
		})
	}
}

type legacyRunner struct{}

func (legacyRunner) RunAgentTurn(context.Context, uint, string, func(string)) (string, error) {
	return "ok", nil
}
func (legacyRunner) HandleApprovalCommand(context.Context, uint, string) (bool, string) {
	return false, ""
}

func TestRPCControlValidationAndCompatibility(t *testing.T) {
	c := connect(t, legacyRunner{}, 9)
	send(t, c, 1, "agent/status", nil)
	if msg := receive(t, c, 1); msg.Error == nil || msg.Error.Code != -32601 {
		t.Fatal("unsupported control not rejected")
	}
	send(t, c, 2, "agent/run", map[string]string{"content": "hello"})
	if msg := receive(t, c, 2); string(msg.Result) != `{"reply":"ok"}` {
		t.Fatal(string(msg.Result))
	}
	r := newRunner()
	c2 := connect(t, r, 9)
	send(t, c2, 3, "agent/cancel", map[string]string{})
	if msg := receive(t, c2, 3); msg.Error == nil || msg.Error.Code != -32602 {
		t.Fatal("missing run ID accepted")
	}
}
