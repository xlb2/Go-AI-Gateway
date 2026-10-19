package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
)

func TestHistorySourceToolSpill(t *testing.T) {
	e := newEnv(t, 9179)
	s := session.RedisStore{}
	set, err := agent.NewStorageTools(s, approval.RedisStore{}, spill.RedisStore{})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("中🙂", 1200) + "TAIL"
	dto := set.ToolResult(e.ctx(), &schema.Message{Role: schema.Tool, Content: body, ToolCallID: "fixture"})
	if dto.SpillLocator == "" || dto.Content == body {
		t.Fatal("missing structured spill")
	}
	if err := s.AppendEvent(e.ctx(), e.uid, dto); err != nil {
		t.Fatal(err)
	}
	p, err := s.SourceAt(e.ctx(), e.uid, 0, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range set.Tools() {
		info, err := tool.Info(e.ctx())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != "read_history_source" {
			continue
		}
		args, _ := json.Marshal(map[string]any{"source_id": p.ID, "expand_spill": true, "offset": 2398, "limit": 10})
		out, err := tool.InvokableRun(e.ctx(), string(args))
		if err != nil || !strings.Contains(out, "中🙂TAIL") {
			t.Fatalf("out=%s err=%v", out, err)
		}
		if err := e.redis.Del(e.ctx(), dto.SpillLocator).Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := tool.InvokableRun(e.ctx(), string(args)); err == nil {
			t.Fatal("missing spill accepted")
		}
		// Plain text that resembles a locator never becomes an external reference.
		if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage(dto.Content)); err != nil {
			t.Fatal(err)
		}
		legacy, err := s.SourceAt(e.ctx(), e.uid, 1, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		args, _ = json.Marshal(map[string]any{"source_id": legacy.ID, "expand_spill": true})
		if _, err := tool.InvokableRun(e.ctx(), string(args)); err == nil {
			t.Fatal("parsed locator from prose")
		}
		return
	}
	t.Fatal("history tool not registered")
}
