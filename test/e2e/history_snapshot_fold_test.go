package e2e

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
)

func TestHistorySnapshotVerifiedFold(t *testing.T) {
	e := newEnv(t, 9187)
	s := session.RedisStore{}
	key := fmt.Sprintf("agent:V2:history:%d", e.uid)
	fold := fmt.Sprintf("agent:V2:fold:%d", e.uid)
	if err := s.AppendEvents(e.ctx(), e.uid, []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: "covered"},
		{Type: session.EventUserMessage, Role: "user", Content: "tail"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "reply"},
		{Type: session.EventCompactionSummary, Content: "summary", CompactionFrom: 0, CompactionTo: 0},
	}); err != nil {
		t.Fatal(err)
	}
	full, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.redis.Set(e.ctx(), fold, 1, 0).Err(); err != nil {
		t.Fatal(err)
	}
	tail, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil || !reflect.DeepEqual(full, tail) {
		t.Fatalf("tail=%+v full=%+v err=%v", tail, full, err)
	}
	if len(tail) != 3 || tail[0].Seq != 3 || tail[1].Seq != 1 {
		t.Fatalf("seq drift: %+v", tail)
	}
	p, err := s.ReadSource(e.ctx(), e.uid, tail[1].ID, 0, 20)
	if err != nil || p.Text != "tail" {
		t.Fatalf("read=%+v err=%v", p, err)
	}
	// A covered corrupt prefix is intentionally not loaded or validated.
	// This distinguishes a real tail read from a full read with equal output.
	if err := e.redis.LSet(e.ctx(), key, 0, "broken").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetHistorySources(e.ctx(), e.uid, 0); err != nil {
		t.Fatalf("covered prefix read: %v", err)
	}
	if err := e.redis.LSet(e.ctx(), key, 2, "broken").Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetHistorySources(e.ctx(), e.uid, 0); err == nil || got != nil {
		t.Fatalf("active corruption accepted: %+v %v", got, err)
	}
}

func TestHistorySnapshotUnprovenFoldFallback(t *testing.T) {
	e := newEnv(t, 9188)
	s := session.RedisStore{}
	key := fmt.Sprintf("agent:V2:history:%d", e.uid)
	fold := fmt.Sprintf("agent:V2:fold:%d", e.uid)
	if err := s.AppendEvents(e.ctx(), e.uid, []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: "covered"},
		{Type: session.EventUserMessage, Role: "user", Content: "must keep"},
		{Type: session.EventCompactionSummary, Content: "summary", CompactionFrom: 0, CompactionTo: 0},
	}); err != nil {
		t.Fatal(err)
	}
	want, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 2 is the legacy summary-position hint, not the covered-prefix end.
	for _, hint := range []string{"2", "100", "-1", "1.5", "bad"} {
		if err := e.redis.Set(e.ctx(), fold, hint, 0).Err(); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetHistorySources(e.ctx(), e.uid, 0)
		if err != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("hint=%s got=%+v err=%v", hint, got, err)
		}
	}
	// Keep a numerically valid stale fold hint while rebuilding the log.
	if err := e.redis.Set(e.ctx(), fold, 1, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := e.redis.Del(e.ctx(), key).Err(); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"new first", "new second"} {
		if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage(content)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil || len(got) != 2 || got[0].Seq != 0 || got[0].Message.Content != "new first" {
		t.Fatalf("stale fold skipped new prefix: %+v %v", got, err)
	}
}
