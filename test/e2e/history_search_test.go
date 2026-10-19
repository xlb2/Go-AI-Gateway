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

func TestHistorySourceSearchPaging(t *testing.T) {
	e := newEnv(t, 9182)
	s := session.RedisStore{}
	for i := 0; i < 7; i++ {
		if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage(strings.Repeat("前", 250)+"来源ID-XZ_42尾")); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.SearchSources(e.ctx(), e.uid, "来源ID-XZ_42", "")
	if err != nil || len(first.Sources) != 5 || !first.HasMore {
		t.Fatalf("first=%+v %v", first, err)
	}
	if !strings.Contains(first.Sources[0].Text, "来源ID-XZ_42") || first.Sources[0].Offset != 210 {
		t.Fatalf("preview=%+v", first.Sources[0])
	}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("new 来源ID-XZ_42")); err != nil {
		t.Fatal(err)
	}
	last, err := s.SearchSources(e.ctx(), e.uid, "来源ID-XZ_42", first.Next)
	if err != nil || len(last.Sources) != 2 || last.HasMore || last.Sources[0].Seq != 1 {
		t.Fatalf("last=%+v %v", last, err)
	}
	if _, err := s.SearchSources(e.ctx(), e.uid+1, "来源", first.Next); err == nil {
		t.Fatal("cross owner cursor")
	}
	for _, q := range []string{" ", strings.Repeat("字", 101)} {
		if _, err := s.SearchSources(e.ctx(), e.uid, q, ""); err == nil {
			t.Fatal("invalid query")
		}
	}
	result, err := s.SearchSources(e.ctx(), e.uid, "xz_42", "")
	if err != nil || len(result.Sources) != 0 {
		t.Fatalf("case sensitive=%+v %v", result, err)
	}
}

func TestHistorySourceSearchToolAndCoverage(t *testing.T) {
	e := newEnv(t, 9183)
	s := session.RedisStore{}
	if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("needle old")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		if err := s.SaveMessage(e.ctx(), e.uid, schema.UserMessage("unrelated")); err != nil {
			t.Fatal(err)
		}
	}
	set, err := agent.NewStorageTools(s, approval.RedisStore{}, spill.RedisStore{})
	if err != nil {
		t.Fatal(err)
	}
	dto := set.ToolResult(e.ctx(), &schema.Message{Role: schema.Tool, Content: strings.Repeat("needle", 500), ToolCallID: "large"})
	if err := s.AppendEvent(e.ctx(), e.uid, dto); err != nil {
		t.Fatal(err)
	}
	for _, tool := range set.Tools() {
		info, err := tool.Info(e.ctx())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != "search_history_sources" {
			continue
		}
		out, err := tool.InvokableRun(e.ctx(), `{"query":"needle"}`)
		if err != nil {
			t.Fatal(err)
		}
		var result session.SourceSearch
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Sources) != 0 || result.Scanned != 50 || !result.HasMore || result.ExternalUnsearched != 1 {
			t.Fatal(out)
		}
		args, _ := json.Marshal(map[string]string{"query": "needle", "cursor": result.Next})
		out, err = tool.InvokableRun(e.ctx(), string(args))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Sources) != 1 || result.Sources[0].Text != "needle old" || result.HasMore {
			t.Fatal(out)
		}
		p, err := s.ReadSource(e.ctx(), e.uid, result.Sources[0].ID, 0, 100)
		if err != nil || p.Text != "needle old" {
			t.Fatalf("read=%+v %v", p, err)
		}
		return
	}
	t.Fatal("search tool absent")
}
