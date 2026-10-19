package e2e

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
)

func TestHistorySourceBrowseCursor(t *testing.T) {
	e := newEnv(t, 9180)
	s := session.RedisStore{}
	empty, err := s.ListSources(e.ctx(), e.uid, "", 5)
	if err != nil || len(empty.Sources) != 0 || empty.HasMore {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	for i := 0; i < 7; i++ {
		if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListSources(e.ctx(), e.uid, "", 5)
	if err != nil || len(first.Sources) != 5 || !first.HasMore || first.Sources[0].Seq != 6 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("new")); err != nil {
		t.Fatal(err)
	}
	last, err := s.ListSources(e.ctx(), e.uid, first.Next, 5)
	if err != nil || len(last.Sources) != 2 || last.HasMore || last.Sources[0].Seq != 1 || last.Sources[1].Seq != 0 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	end, err := s.ListSources(e.ctx(), e.uid, last.Sources[1].ID, 5)
	if err != nil || len(end.Sources) != 0 || end.HasMore {
		t.Fatalf("end=%+v err=%v", end, err)
	}
	if _, err := s.ListSources(e.ctx(), e.uid+1, first.Next, 5); err == nil {
		t.Fatal("cross owner cursor")
	}
	if err := e.redis.Del(e.ctx(), fmt.Sprintf("agent:V2:history:%d", e.uid)).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("replacement")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListSources(e.ctx(), e.uid, first.Next, 5); err == nil {
		t.Fatal("stale cursor accepted")
	}
}

func TestHistorySourceBrowseTool(t *testing.T) {
	e := newEnv(t, 9181)
	s := session.RedisStore{}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("known fact")); err != nil {
		t.Fatal(err)
	}
	set, err := agent.NewStorageTools(s, approval.RedisStore{}, spill.RedisStore{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range set.Tools() {
		info, err := tool.Info(e.ctx())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != "browse_history_sources" {
			continue
		}
		out, err := tool.InvokableRun(e.ctx(), `{}`)
		if err != nil {
			t.Fatal(err)
		}
		var list session.SourceList
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Sources) != 1 || list.Sources[0].Text != "known fact" {
			t.Fatal(out)
		}
		args, _ := json.Marshal(map[string]string{"anchor": list.Sources[0].ID})
		out, err = tool.InvokableRun(e.ctx(), string(args))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Sources) != 1 {
			t.Fatalf("neighbors=%s %v", out, err)
		}
		page, err := s.ReadSource(e.ctx(), e.uid, list.Sources[0].ID, 0, 100)
		if err != nil || page.Text != "known fact" {
			t.Fatalf("read=%+v %v", page, err)
		}
		return
	}
	t.Fatal("browse tool absent")
}
