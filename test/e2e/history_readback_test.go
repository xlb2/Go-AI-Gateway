package e2e

import (
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
)

func TestHistorySourceNeighbors(t *testing.T) {
	e := newEnv(t, 9177)
	s := session.RedisStore{}
	for _, body := range []string{"before", strings.Repeat("中", 250), "after"} {
		if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage(body)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.SourceAt(e.ctx(), e.uid, 1, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := s.SourceNeighbors(e.ctx(), e.uid, p.ID, 5, 5)
	if err != nil || len(pages) != 3 {
		t.Fatalf("neighbors=%+v err=%v", pages, err)
	}
	if pages[0].Text != "before" || pages[2].Text != "after" || pages[1].ID != p.ID || len([]rune(pages[1].Text)) != 200 || !pages[1].HasMore {
		t.Fatalf("bad preview: %+v", pages)
	}
	tail, err := s.ReadSource(e.ctx(), e.uid, pages[1].ID, pages[1].Next, 200)
	if err != nil || len([]rune(tail.Text)) != 50 || tail.HasMore {
		t.Fatalf("tail=%+v err=%v", tail, err)
	}
	if _, err := s.SourceNeighbors(e.ctx(), e.uid+1, p.ID, 1, 1); !errors.Is(err, session.ErrSourceUnavailable) {
		t.Fatalf("owner: %v", err)
	}
	if _, err := s.SourceNeighbors(e.ctx(), e.uid, p.ID, 6, 0); err == nil {
		t.Fatal("unbounded neighbors")
	}
}

func TestSpillOwnedPages(t *testing.T) {
	e := newEnv(t, 9178)
	s := spill.RedisStore{}
	body := strings.Repeat("中🙂尾", 1000)
	ref, err := s.SaveText(e.ctx(), e.uid, "fixture", body)
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for offset := 0; ; {
		p, err := s.ReadPage(e.ctx(), e.uid, ref.Locator, offset, 701)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(p.Text)
		if !p.HasMore {
			break
		}
		if p.Next <= offset {
			t.Fatal("page did not advance")
		}
		offset = p.Next
	}
	if got.String() != body {
		t.Fatal("lost or duplicated content")
	}
	if _, err := s.ReadPage(e.ctx(), e.uid+1, ref.Locator, 0, 10); err == nil {
		t.Fatal("cross owner read")
	}
	if _, err := s.ReadPage(e.ctx(), e.uid, ref.Locator, 0, 2001); err == nil {
		t.Fatal("unbounded page")
	}
	if err := e.redis.Del(e.ctx(), ref.Locator).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadPage(e.ctx(), e.uid, ref.Locator, 0, 10); err == nil {
		t.Fatal("missing content returned as success")
	}
}
