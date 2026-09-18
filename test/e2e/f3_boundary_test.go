package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/appserver"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/subagent"
	"go_im_gateway/test/fakemodel"
)

func TestF3BoundaryCrossEntrypoints(t *testing.T) {
	h, dial, entered, canceled, closed := legacyWSFixture(t, 9110)
	chat := dial()
	wsSend(t, chat, map[string]any{"type": "chat", "to_user_id": 999, "content": "hello"})
	wsAwait(t, entered)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		appserver.ServeWS(req.Context(), conn, h, 9110)
	}))
	t.Cleanup(server.Close)
	rpc, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	request := func(id int, method string, params any) appserver.Message {
		t.Helper()
		wsSend(t, rpc, map[string]any{"version": "v1", "id": id, "method": method, "params": params})
		var reply appserver.Message
		if err := json.Unmarshal([]byte(wsRead(t, rpc)), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID == nil || *reply.ID != int64(id) {
			t.Fatalf("unexpected reply=%+v", reply)
		}
		return reply
	}
	status := request(1, "agent/status", nil)
	var state struct {
		Active bool            `json:"active"`
		Run    harness.RunInfo `json:"run"`
	}
	if err := json.Unmarshal(status.Result, &state); err != nil || !state.Active || state.Run.UserID != 9110 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	busy := request(2, "agent/run", map[string]string{"content": "overlap"})
	if busy.Error == nil || !strings.Contains(busy.Error.Message, harness.ErrRunBusy.Error()) {
		t.Fatalf("cross-entry overlap accepted: %+v", busy)
	}
	ack := request(3, "agent/cancel", map[string]string{"run_id": state.Run.ID})
	if string(ack.Result) != `{"cancel_requested":true}` {
		t.Fatal(string(ack.Result))
	}
	wsAwait(t, canceled)
	if reply := wsRead(t, chat); !strings.Contains(reply, "context canceled") {
		t.Fatal(reply)
	}
	if active := request(4, "agent/status", nil); string(active.Result) != `{"active":false}` {
		t.Fatal(string(active.Result))
	}
	chat.Close()
	wsAwait(t, closed)
}

func TestF3BoundaryNestedChildrenAndApproval(t *testing.T) {
	e := newEnv(t, 9111)
	var err error
	e.h, err = harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "done"}, Rules: []fakemodel.Rule{
		{Match: "root task", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "delegate_task", Arguments: `{"task":"middle task"}`}}}},
		{Match: "middle task", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "delegate_tasks", Arguments: `{"tasks":["defense child","leaf child"]}`}}}},
		{Match: "defense child", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "execute_system_defense", Arguments: `{"emotion":"angry","threat_level":"low"}`}}}},
	}})
	e.run("root task")
	parent := e.log()
	if len(parent) == 0 || parent[0].Run == nil {
		t.Fatal("missing root")
	}
	s := session.RedisStore{}
	rootID := parent[0].Run.RunID
	ids, err := s.ChildRunIDs(e.ctx(), e.uid, rootID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("middle=%v err=%v", ids, err)
	}
	middle, err := s.GetChildLog(e.ctx(), e.uid, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	var middleCall string
	for _, event := range middle {
		if event.Type == session.EventToolCall {
			middleCall = event.ToolCalls[0].ID
		}
	}
	if middleCall == "" {
		t.Fatal("middle delegation not recorded")
	}
	leaves, err := s.ChildRunIDs(e.ctx(), e.uid, ids[0])
	if err != nil || len(leaves) != 2 {
		t.Fatalf("leaves=%v err=%v", leaves, err)
	}
	blocked := false
	for _, id := range leaves {
		log, err := s.GetChildLog(e.ctx(), e.uid, id)
		if err != nil || len(log) == 0 {
			t.Fatalf("leaf=%v err=%v", log, err)
		}
		for _, event := range log {
			if event.Run == nil || event.Run.ParentRunID != ids[0] || event.Run.ParentToolCallID != middleCall {
				t.Fatal("grandchild attached to wrong parent")
			}
			if event.Type == session.EventToolResult && strings.Contains(event.Content, "请由主任务发起") {
				blocked = true
			}
		}
		if problems := session.ValidateLog(log); len(problems) != 0 {
			t.Fatal(problems)
		}
	}
	if !blocked {
		t.Fatal("child defense approval was not explicitly blocked")
	}
	if p, err := e.h.Approvals.GetPending(e.ctx(), e.uid); err != nil || p != nil {
		t.Fatalf("child polluted parent approval slot: %v %v", p, err)
	}
	if problems := session.ValidateLog(parent); len(problems) != 0 {
		t.Fatal(problems)
	}
	if problems := session.ValidateLog(middle); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestF3BoundaryCanceledChildrenDoNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	r := subagent.Runner(func(context.Context) (subagent.ChildAgent, error) { calls++; return traceChild{}, nil })
	results := r.RunParallel(ctx, []string{"one", "two", "three"}, 1)
	if calls != 0 {
		t.Fatal("canceled queued tasks reached factory")
	}
	for _, result := range results {
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("result=%+v", result)
		}
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	r = func(context.Context) (subagent.ChildAgent, error) { cancel(); return nil, nil }
	if _, err := r.Run(ctx, "during construction"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r = func(context.Context) (subagent.ChildAgent, error) { return nil, nil }
	if _, err := r.Run(context.Background(), "empty child"); err == nil {
		t.Fatal("empty child accepted")
	}
}
