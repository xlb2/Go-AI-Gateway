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
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/knowledge"
	"gorm.io/gorm"
)

func TestKnowledgeReadingToolsScopeAndVersions(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateLibrary(ctx, 11, "same owner other library")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := s.Import(ctx, 11, lib.ID, "fact.md", "md", strings.Repeat("中", 4001))
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := s.Import(ctx, 11, other.ID, "secret.md", "md", "private")
	if err != nil {
		t.Fatal(err)
	}
	tools := knowledge.NewReadingTools(s, 11, lib.ID)
	args := func(source, version uint64, offset int) string {
		return fmt.Sprintf(`{"source_id":%d,"version_id":%d,"offset":%d}`, source, version, offset)
	}
	for _, raw := range []string{args(foreign.ID, foreign.CurrentVersionID, 0), args(src.ID, foreign.CurrentVersionID, 0), `{"source_id":1,"owner_id":11}`, `null`, args(src.ID, src.CurrentVersionID, -1)} {
		if _, err := tools[1].InvokableRun(ctx, raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := knowledge.NewReadingTools(s, 22, lib.ID)[1].InvokableRun(ctx, args(src.ID, src.CurrentVersionID, 0)); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("wrong owner: %v", err)
	}
	first, err := tools[1].InvokableRun(ctx, args(src.ID, src.CurrentVersionID, 0))
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Content string
		Next    int `json:"next_offset"`
		EOF     bool
	}
	if err := json.Unmarshal([]byte(first), &page); err != nil || len([]rune(page.Content)) != 4000 || page.Next != 4000 || page.EOF {
		t.Fatalf("page: %+v %v", page, err)
	}
	v2 := knowledge.Version{SourceID: src.ID, Version: 2, Body: "replacement"}
	if err := db.Create(&v2).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&knowledge.Source{}).Where("id = ?", src.ID).Update("current_version_id", v2.ID).Error; err != nil {
		t.Fatal(err)
	}
	last, err := tools[1].InvokableRun(ctx, args(src.ID, src.CurrentVersionID, page.Next))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(last), &page); err != nil || page.Content != "中" || !page.EOF {
		t.Fatalf("version changed: %s %v", last, err)
	}
	denied := false
	for i := 0; i < 10; i++ {
		if _, err := tools[1].InvokableRun(ctx, args(src.ID, src.CurrentVersionID, 0)); err != nil {
			denied = true
			break
		}
	}
	if !denied {
		t.Fatal("read budget unbounded")
	}
	if _, err := knowledge.NewReadingTools(s, 11, lib.ID)[1].InvokableRun(ctx, args(src.ID, src.CurrentVersionID, 0)); err != nil {
		t.Fatal("budget leaked across turns", err)
	}
}

func TestKnowledgeReadingToolsPagination(t *testing.T) {
	s := knowledge.NewStore(database(t))
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "pages")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 21; i++ {
		if _, _, err := s.Import(ctx, 11, lib.ID, "entry.md", "md", fmt.Sprintf("entry %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	lister := knowledge.NewReadingTools(s, 11, lib.ID)[0]
	raw, err := lister.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []knowledge.Source
		Next  uint64 `json:"next_after"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Items) != 20 || page.Next != page.Items[19].ID {
		t.Fatalf("first page %s %v", raw, err)
	}
	lastID := page.Next
	raw, err = lister.InvokableRun(ctx, fmt.Sprintf(`{"after":%d}`, lastID))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID <= lastID || page.Next != 0 {
		t.Fatalf("last page %s %v", raw, err)
	}
}

type readingModel struct {
	source   knowledge.Source
	calls    int
	received string
}

func (*readingModel) BindTools([]*schema.ToolInfo) error { return nil }
func (*readingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}
func (m *readingModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	var msg *schema.Message
	switch m.calls {
	case 1:
		msg = &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "list", Function: schema.FunctionCall{Name: "list_sources", Arguments: `{}`}}}}
	case 2:
		msg = &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "read", Function: schema.FunctionCall{Name: "read_source", Arguments: fmt.Sprintf(`{"source_id":%d,"version_id":%d}`, m.source.ID, m.source.CurrentVersionID)}}}}
	default:
		m.received = messages[len(messages)-1].Content
		msg = schema.AssistantMessage("answer based on fact.md v1", nil)
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func TestKnowledgeReadingToolsHTTPAndWriteBarrier(t *testing.T) {
	for _, fail := range []string{"", session.EventToolCall, session.EventToolResult} {
		t.Run("fail="+fail, func(t *testing.T) {
			db := database(t)
			s := knowledge.NewStore(db)
			ctx := context.Background()
			lib, err := s.CreateLibrary(ctx, 11, "tools")
			if err != nil {
				t.Fatal(err)
			}
			src, _, err := s.Import(ctx, 11, lib.ID, "fact.md", "md", "unique retrieved evidence")
			if err != nil {
				t.Fatal(err)
			}
			conv, err := s.CreateConversation(ctx, 11, lib.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fail != "" {
				if err := db.Callback().Create().Before("gorm:create").Register("test:tool_event_fail", func(tx *gorm.DB) {
					if rows, ok := tx.Statement.Dest.(*[]knowledge.TurnEvent); ok {
						for _, row := range *rows {
							var event session.MemoryDTO
							if json.Unmarshal([]byte(row.Payload), &event) == nil && event.Type == fail {
								tx.AddError(errors.New("injected"))
							}
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			m := &readingModel{source: src}
			r := gin.New()
			g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", uint(11)) })
			knowledge.NewChat(s, func(context.Context) (model.ChatModel, error) { return m, nil }).Register(g)
			body, _ := json.Marshal(knowledge.TurnInput{RequestID: uuid.NewString(), Content: "read my library"})
			out := httptest.NewRecorder()
			r.ServeHTTP(out, httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns", lib.ID, conv.ID), strings.NewReader(string(body))))
			detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
			if err != nil || len(detail.Turns) != 1 {
				t.Fatalf("detail %+v %v", detail, err)
			}
			if fail != "" {
				if m.calls != 1 || detail.Turns[0].Status != "failed" || strings.Contains(out.Body.String(), `"type":"done"`) {
					t.Fatalf("write failure continued: calls=%d %s", m.calls, out.Body.String())
				}
				return
			}
			if m.calls != 3 || !strings.Contains(m.received, "unique retrieved evidence") || detail.Turns[0].Status != "completed" || !strings.Contains(out.Body.String(), `"type":"done"`) {
				t.Fatalf("tools not connected: calls=%d %s", m.calls, out.Body.String())
			}
			var rows []knowledge.TurnEvent
			if err := db.Where("turn_id = ?", detail.Turns[0].ID).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			results := map[string]string{}
			for _, row := range rows {
				var event session.MemoryDTO
				if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == session.EventToolResult {
					results[event.ToolCallID] = event.Content
				}
			}
			if len(results) != 2 || !strings.Contains(results["list"], "fact.md") || !strings.Contains(results["read"], "unique retrieved evidence") {
				t.Fatalf("missing tool evidence: %+v", results)
			}
		})
	}
}
