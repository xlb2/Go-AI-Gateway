package e2e

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"go_im_gateway/internal/handler"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/model"
)

type wsMessages struct{}

func (wsMessages) PullOfflineMessages(uint) ([]model.Message, error) { return nil, nil }
func (wsMessages) SendPrivateMessage(uint, uint, string) error       { return nil }

func legacyWSFixture(t *testing.T, uid uint) (*harness.Harness, func() *websocket.Conn, <-chan struct{}, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	e := newEnv(t, uid)
	entered, canceled, closed := make(chan struct{}, 2), make(chan struct{}, 2), make(chan struct{}, 4)
	h, err := harness.NewWithDependencies(harness.Dependencies{Sessions: session.RedisStore{}, Approvals: approval.RedisStore{}, Executor: sandbox.LocalExecutor{},
		NewLoop: func(ctx context.Context) (agent.Loop, error) {
			entered <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return nil, ctx.Err()
		},
		Summarize: func(context.Context, []string) (string, error) { return "", nil }, ToolNames: func(context.Context) []string { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	endpoint := handler.ConnectWSWithRunner(wsMessages{}, e.redis, h)
	router.GET("/ws", func(c *gin.Context) { defer func() { closed <- struct{}{} }(); endpoint(c) })
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	// Supply a test-only key; production has no default signing secret.
	secret := strings.Repeat("legacy-ws-test-", 3)
	t.Setenv("JWT_SECRET", secret)
	claims := handler.CustomClaims{UserID: uid, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	dial := func() *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?token="+token, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	return h, dial, entered, canceled, closed
}

func wsAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatal("WS lifecycle did not reach boundary")
	}
}

func wsSend(t *testing.T, c *websocket.Conn, payload map[string]any) {
	t.Helper()
	c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := c.WriteJSON(payload); err != nil {
		t.Fatal(err)
	}
}

func wsRead(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLegacyWSControlDuringRun(t *testing.T) {
	h, dial, entered, canceled, closed := legacyWSFixture(t, 9080)
	c := dial()
	wsSend(t, c, map[string]any{"type": "chat", "to_user_id": 999, "content": "hello"})
	wsAwait(t, entered)
	wsSend(t, c, map[string]any{"type": "ping"})
	if reply := wsRead(t, c); !strings.Contains(reply, "pong") {
		t.Fatal(reply)
	}
	wsSend(t, c, map[string]any{"type": "agent/status"})
	var status struct {
		Active bool            `json:"active"`
		Run    harness.RunInfo `json:"run"`
	}
	if err := json.Unmarshal([]byte(wsRead(t, c)), &status); err != nil || !status.Active || status.Run.ID == "" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	wsSend(t, c, map[string]any{"type": "chat", "to_user_id": 999, "content": "overlap"})
	if reply := wsRead(t, c); !strings.Contains(reply, "已有任务") {
		t.Fatal(reply)
	}
	wsSend(t, c, map[string]any{"type": "agent/cancel", "run_id": "stale"})
	if reply := wsRead(t, c); !strings.Contains(reply, `"cancel_requested":false`) {
		t.Fatal(reply)
	}
	wsSend(t, c, map[string]any{"type": "agent/cancel", "run_id": status.Run.ID})
	// Cancellation acknowledgement and the run's failure reply may arrive in either order.
	ack, ended := false, false
	for range 2 {
		reply := wsRead(t, c)
		ack = ack || strings.Contains(reply, `"cancel_requested":true`)
		ended = ended || strings.Contains(reply, "context canceled")
	}
	if !ack || !ended {
		t.Fatal("missing cancellation acknowledgement or final failure")
	}
	wsAwait(t, canceled)
	if _, active := h.ActiveRun(9080); active {
		t.Fatal("run slot leaked")
	}
	c.Close()
	wsAwait(t, closed)
}

func TestLegacyWSReplacementCancelsOldRun(t *testing.T) {
	_, dial, entered, canceled, closed := legacyWSFixture(t, 9081)
	old := dial()
	wsSend(t, old, map[string]any{"type": "chat", "to_user_id": 999, "content": "hello"})
	wsAwait(t, entered)
	next := dial()
	wsAwait(t, canceled)
	wsAwait(t, closed)
	wsSend(t, next, map[string]any{"type": "ping"})
	if reply := wsRead(t, next); !strings.Contains(reply, "pong") {
		t.Fatal(reply)
	}
	handler.ClientMUtex.RLock()
	present := handler.ClientManager[9081] != nil
	handler.ClientMUtex.RUnlock()
	if !present {
		t.Fatal("old connection removed replacement from registry")
	}
	next.Close()
	wsAwait(t, closed)
	handler.ClientMUtex.RLock()
	present = handler.ClientManager[9081] != nil
	handler.ClientMUtex.RUnlock()
	if present {
		t.Fatal("closed replacement remained registered")
	}
}
