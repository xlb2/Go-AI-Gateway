package e2e

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/tokenmeter"
)

func TestHistorySnapshotTrimAndReadback(t *testing.T) {
	e := newEnv(t, 9185)
	s := session.RedisStore{}
	events := []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: strings.Repeat("old", 100)},
		{Type: session.EventUserMessage, Role: "user", Content: "same"},
		{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "a", Name: "read"}}},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "a", Content: "same"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "same"},
	}
	if err := s.AppendEvents(e.ctx(), e.uid, events); err != nil {
		t.Fatal(err)
	}
	all, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("snapshot=%+v err=%v", all, err)
	}
	limit := tokenmeter.Used([]*schema.Message{all[4].Message})
	trimmed, err := s.GetHistorySources(e.ctx(), e.uid, limit)
	if err != nil || len(trimmed) != 1 || trimmed[0].ID != all[4].ID || trimmed[0].Seq != 4 {
		t.Fatalf("trim=%+v err=%v", trimmed, err)
	}
	p, err := s.ReadSource(e.ctx(), e.uid, trimmed[0].ID, 0, 20)
	if err != nil || p.Role != "assistant" || p.Text != "same" {
		t.Fatalf("read=%+v err=%v", p, err)
	}
	// Budget lands at a tool result: trimming must drop the orphan result
	// without attaching its identical content's source to the final answer.
	limit = tokenmeter.Used([]*schema.Message{all[3].Message, all[4].Message})
	paired, err := s.GetHistorySources(e.ctx(), e.uid, limit)
	if err != nil || len(paired) != 1 || paired[0].ID != all[4].ID {
		t.Fatalf("tool boundary=%+v err=%v", paired, err)
	}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("append")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid, all[4].ID, 0, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid+1, all[4].ID, 0, 20); !errors.Is(err, session.ErrSourceUnavailable) {
		t.Fatal(err)
	}
	key := fmt.Sprintf("agent:V2:history:%d", e.uid)
	if err := e.redis.Del(e.ctx(), key).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("replacement")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid, all[0].ID, 0, 20); !errors.Is(err, session.ErrSourceUnavailable) {
		t.Fatalf("stale=%v", err)
	}
}

func TestHistorySnapshotSummaryAndInvalid(t *testing.T) {
	e := newEnv(t, 9186)
	s := session.RedisStore{}
	empty, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	if err := s.AppendEvents(e.ctx(), e.uid, []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: "original"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "partial", Interrupted: true},
		{Type: session.EventCompactionSummary, Content: "summary", CompactionFrom: 0, CompactionTo: 0},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil || len(got) != 2 || got[0].Seq != 2 || !got[0].Summary || !got[1].Interrupted {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
	p, err := s.ReadSource(e.ctx(), e.uid, got[0].ID, 0, 20)
	if err != nil || p.Text != "summary" {
		t.Fatalf("summary read=%+v err=%v", p, err)
	}
	key := fmt.Sprintf("agent:V2:history:%d", e.uid)
	for _, raw := range []string{"broken", `{"v":999,"role":"user","content":"future"}`} {
		if err := e.redis.RPush(e.ctx(), key, raw).Err(); err != nil {
			t.Fatal(err)
		}
		if result, err := s.GetHistorySources(e.ctx(), e.uid, 0); err == nil || result != nil {
			t.Fatalf("invalid accepted: %+v %v", result, err)
		}
		if err := e.redis.RPop(e.ctx(), key).Err(); err != nil {
			t.Fatal(err)
		}
	}
}
