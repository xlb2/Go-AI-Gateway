package e2e

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
)

func TestHistorySourcePagesAndIdentity(t *testing.T) {
	e := newEnv(t, 9175)
	s := session.RedisStore{}
	content := strings.Repeat("中🙂尾", 1500)
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage(content)); err != nil {
		t.Fatal(err)
	}
	first, err := s.SourceAt(e.ctx(), e.uid, 0, 0, 777)
	if err != nil {
		t.Fatal(err)
	}
	got, offset := first.Text, first.Next
	for offset < first.Total {
		page, err := (session.RedisStore{}).ReadSource(e.ctx(), e.uid, first.ID, offset, 777)
		if err != nil {
			t.Fatal(err)
		}
		if page.ID != first.ID || page.Next <= offset {
			t.Fatalf("unstable page: %+v", page)
		}
		got += page.Text
		offset = page.Next
	}
	if got != content || first.Total != 4500 {
		t.Fatal("unicode page loss or duplication")
	}
	// A projection checkpoint must not alter the original seq or source ID.
	if err := s.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{Type: session.EventCompactionSummary, Content: "summary", CompactionFrom: 0, CompactionTo: 0}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadSource(e.ctx(), e.uid, first.ID, 4499, 10)
	if err != nil || page.Text != "尾" || page.HasMore {
		t.Fatalf("after summary: %+v %v", page, err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid+1, first.ID, 0, 10); !errors.Is(err, session.ErrSourceUnavailable) {
		t.Fatalf("cross owner: %v", err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid, first.ID, 0, 2001); err == nil {
		t.Fatal("unbounded page accepted")
	}
	key := fmt.Sprintf("agent:V2:history:%d", e.uid)
	if err := e.redis.Del(e.ctx(), key).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvents(e.ctx(), e.uid, []session.MemoryDTO{{Type: session.EventUserMessage, Role: "user", Content: "replacement"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid, first.ID, 0, 10); !errors.Is(err, session.ErrSourceUnavailable) {
		t.Fatalf("stale source read replacement: %v", err)
	}
}

func TestHistorySourceLegacyAndUnavailable(t *testing.T) {
	e := newEnv(t, 9176)
	s := session.RedisStore{}
	key := fmt.Sprintf("agent:V2:history:%d", e.uid)
	genKey := fmt.Sprintf("agent:V2:generation:%d", e.uid)
	t.Cleanup(func() { e.redis.Del(e.ctx(), genKey) })
	if err := e.redis.Del(e.ctx(), genKey).Err(); err != nil {
		t.Fatal(err)
	}
	raw := `{"role":"assistant","content":"old","interrupted":true}`
	if err := e.redis.RPush(e.ctx(), key, raw).Err(); err != nil {
		t.Fatal(err)
	}
	p, err := s.SourceAt(e.ctx(), e.uid, 0, 0, 10)
	if err != nil || p.Text != "old" || !p.Interrupted {
		t.Fatalf("legacy: %+v %v", p, err)
	}
	stored, err := e.redis.LIndex(e.ctx(), key, 0).Result()
	if err != nil || stored != raw {
		t.Fatal("legacy event rewritten")
	}
	if err := e.redis.Del(e.ctx(), genKey).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSource(e.ctx(), e.uid, p.ID, 0, 10); !errors.Is(err, session.ErrSourceUnavailable) {
		t.Fatalf("lost generation reused: %v", err)
	}
	if err := e.redis.RPush(e.ctx(), key, "broken", `{"v":999,"content":"future"}`).Err(); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int64{1, 2, 3} {
		if _, err := s.SourceAt(e.ctx(), e.uid, seq, 0, 10); err == nil {
			t.Fatalf("invalid source %d accepted", seq)
		}
	}
}
