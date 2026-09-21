//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go_im_gateway/internal/knowledge"
)

func TestKnowledgeSearchIsolationAndExactMatch(t *testing.T) {
	_, s, lib, _, _, _ := evidenceFixture(t)
	ctx := context.Background()
	body := "中文前缀🧭 Harness.RunAgentTurn %_ ' OR 1=1 --"
	src, _, err := s.Import(ctx, 11, lib.ID, "code.md", "md", body)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateLibrary(ctx, 11, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Import(ctx, 11, other.ID, "private.md", "md", "secret-only"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Harness.RunAgentTurn", "%_", "' OR 1=1 --", "中文前缀"} {
		result, err := s.SearchSources(ctx, 11, lib.ID, query, 0)
		if err != nil || len(result.Items) != 1 || result.Items[0].SourceID != src.ID {
			t.Fatalf("query %q: %+v %v", query, result, err)
		}
		hit := result.Items[0]
		if string([]rune(body)[hit.Offset:hit.MatchEnd]) != query {
			t.Fatalf("bad Unicode range: %+v", hit)
		}
	}
	for _, query := range []string{"harness.runagentturn", "secret-only", "absent"} {
		result, err := s.SearchSources(ctx, 11, lib.ID, query, 0)
		if err != nil || len(result.Items) != 0 {
			t.Fatalf("unexpected match %q: %+v %v", query, result, err)
		}
	}
	if _, err := s.SearchSources(ctx, 22, lib.ID, "Harness", 0); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("owner boundary: %v", err)
	}
	for _, query := range []string{" ", strings.Repeat("中", 129), "x\x00"} {
		if _, err := s.SearchSources(ctx, 11, lib.ID, query, 0); !errors.Is(err, knowledge.ErrInvalid) {
			t.Fatalf("invalid query accepted: %v", err)
		}
	}
	if _, err := knowledge.NewReadingTools(s, 11, lib.ID)[2].InvokableRun(ctx, `{"query":"secret-only","library_id":2}`); err == nil {
		t.Fatal("model selected library")
	}
}

func TestKnowledgeSearchPaginationAndVersions(t *testing.T) {
	db, s, lib, _, _, _ := evidenceFixture(t)
	ctx := context.Background()
	var first knowledge.Source
	for i := 0; i < 11; i++ {
		src, _, err := s.Import(ctx, 11, lib.ID, "match.md", "md", fmt.Sprintf("needle %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = src
		}
	}
	page, err := s.SearchSources(ctx, 11, lib.ID, "needle", 0)
	if err != nil || len(page.Items) != 10 || page.NextAfter != page.Items[9].SourceID {
		t.Fatalf("first page %+v %v", page, err)
	}
	next, err := s.SearchSources(ctx, 11, lib.ID, "needle", page.NextAfter)
	if err != nil || len(next.Items) != 1 || next.NextAfter != 0 || next.Items[0].SourceID <= page.NextAfter {
		t.Fatalf("next page %+v %v", next, err)
	}
	v2 := knowledge.Version{SourceID: first.ID, Version: 2, Body: "replacement"}
	if err := db.Create(&v2).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&knowledge.Source{}).Where("id = ?", first.ID).Update("current_version_id", v2.ID).Error; err != nil {
		t.Fatal(err)
	}
	page, err = s.SearchSources(ctx, 11, lib.ID, "needle", 0)
	if err != nil || len(page.Items) != 10 || page.NextAfter != 0 {
		t.Fatalf("old version searchable %+v %v", page, err)
	}
	for _, hit := range page.Items {
		if hit.SourceID == first.ID {
			t.Fatal("old version returned")
		}
	}
	replacement, err := s.SearchSources(ctx, 11, lib.ID, "replacement", 0)
	if err != nil || len(replacement.Items) != 1 || replacement.Items[0].VersionID != v2.ID {
		t.Fatalf("new version %+v %v", replacement, err)
	}
}

type searchModel struct {
	calls int
	hit   knowledge.KeywordHit
}

func (*searchModel) BindTools([]*schema.ToolInfo) error { return nil }
func (*searchModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}
func (m *searchModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	var reply *schema.Message
	switch m.calls {
	case 1:
		reply = &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "search", Function: schema.FunctionCall{Name: "search_sources", Arguments: `{"query":"原始证据"}`}}}}
	case 2:
		var result knowledge.KeywordResults
		if err := json.Unmarshal([]byte(messages[len(messages)-1].Content), &result); err != nil || len(result.Items) != 1 {
			return nil, errors.New("search did not return a candidate")
		}
		m.hit = result.Items[0]
		reply = &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "read", Function: schema.FunctionCall{Name: "read_source", Arguments: fmt.Sprintf(`{"source_id":%d,"version_id":%d,"offset":%d}`, m.hit.SourceID, m.hit.VersionID, m.hit.Offset)}}}}
	default:
		if !strings.Contains(messages[len(messages)-1].Content, "原始证据") {
			return nil, errors.New("original evidence missing")
		}
		reply = schema.AssistantMessage("依据 original.md v1 的原始证据回答", nil)
	}
	return schema.StreamReaderFromArray([]*schema.Message{reply}), nil
}

func TestKnowledgeSearchAgentEvidence(t *testing.T) {
	_, s, lib, _, _, _ := evidenceFixture(t)
	ctx := context.Background()
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := &searchModel{}
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", uint(11)) })
	knowledge.NewChat(s, func(context.Context) (model.ChatModel, error) { return m, nil }).Register(g)
	body, _ := json.Marshal(knowledge.TurnInput{RequestID: uuid.NewString(), Content: "找到原始证据并解释"})
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns", lib.ID, conv.ID), strings.NewReader(string(body))))
	if m.calls != 3 || !strings.Contains(out.Body.String(), `"type":"done"`) {
		t.Fatalf("search chain: calls=%d %s", m.calls, out.Body.String())
	}
	detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || len(detail.Turns[0].Evidence) != 1 {
		t.Fatalf("evidence %+v %v", detail, err)
	}
	ref := detail.Turns[0].Evidence[0]
	if m.hit.IndexRule != knowledge.ChunkRuleVersion {
		t.Fatalf("agent did not use chunk index: %+v", m.hit)
	}
	reading, err := s.Evidence(ctx, 11, lib.ID, conv.ID, detail.Turns[0].ID, ref.EventID)
	if err != nil || reading.SourceID != m.hit.SourceID || reading.VersionID != m.hit.VersionID || !strings.Contains(reading.Content, "原始证据") {
		t.Fatalf("candidate to original: %+v %v", reading, err)
	}
}
