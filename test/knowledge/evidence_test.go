//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/knowledge"
	"gorm.io/gorm"
)

func evidenceFixture(t *testing.T) (*gorm.DB, *knowledge.Store, knowledge.Library, knowledge.Conversation, knowledge.ChatTurn, knowledge.Source) {
	t.Helper()
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := s.Import(ctx, 11, lib.ID, "original.md", "md", "原始证据<script>alert(1)</script>")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.BeginTurn(ctx, 11, lib.ID, conv.ID, knowledge.TurnInput{RequestID: uuid.NewString(), Content: "read"})
	if err != nil {
		t.Fatal(err)
	}
	args := fmt.Sprintf(`{"source_id":%d,"version_id":%d}`, src.ID, src.CurrentVersionID)
	result, err := knowledge.NewReadingTools(s, 11, lib.ID)[1].InvokableRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	err = s.AppendTurnEvents(ctx, turn.ID, []session.MemoryDTO{
		{Type: session.EventToolCall, ToolCalls: []session.ToolCallData{{ID: "read", Name: "read_source", Arguments: args}}},
		{Type: session.EventToolResult, ToolCallID: "read", ToolName: "read_source", Content: result},
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, s, lib, conv, turn, src
}

func TestKnowledgeEvidenceSnapshot(t *testing.T) {
	db, s, lib, conv, turn, src := evidenceFixture(t)
	ctx := context.Background()
	if err := s.FinishTurn(ctx, turn.ID, "completed", "模型声称引用 other.md v99"); err != nil {
		t.Fatal(err)
	}
	version := knowledge.Version{SourceID: src.ID, Version: 2, Body: "new content"}
	if err := db.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&knowledge.Source{}).Where("id = ?", src.ID).Update("current_version_id", version.ID).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := knowledge.NewStore(db).Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || len(detail.Turns[0].Evidence) != 1 {
		t.Fatalf("evidence: %+v %v", detail, err)
	}
	ref := detail.Turns[0].Evidence[0]
	if ref.Title != "original.md" || ref.Version != 1 || ref.Content != "" || ref.TurnID != turn.ID {
		t.Fatalf("bad metadata: %+v", ref)
	}
	reading, err := s.Evidence(ctx, 11, lib.ID, conv.ID, turn.ID, ref.EventID)
	if err != nil || reading.Content != "原始证据<script>alert(1)</script>" || reading.VersionID != src.CurrentVersionID {
		t.Fatalf("snapshot changed: %+v %v", reading, err)
	}
}

func TestKnowledgeEvidenceHTTPIsolation(t *testing.T) {
	_, s, lib, conv, turn, _ := evidenceFixture(t)
	ctx := context.Background()
	detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || len(detail.Turns[0].Evidence) != 1 {
		t.Fatal(err)
	}
	event := detail.Turns[0].Evidence[0].EventID
	otherLib, err := s.CreateLibrary(ctx, 11, "other")
	if err != nil {
		t.Fatal(err)
	}
	otherConv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		owner                  uint
		lib, conv, turn, event uint64
		status                 int
	}{
		{11, lib.ID, conv.ID, turn.ID, event, 200},
		{22, lib.ID, conv.ID, turn.ID, event, 404},
		{11, otherLib.ID, conv.ID, turn.ID, event, 404},
		{11, lib.ID, otherConv.ID, turn.ID, event, 404},
		{11, lib.ID, conv.ID, turn.ID + 100, event, 404},
		{11, lib.ID, conv.ID, turn.ID, event - 1, 404},
	} {
		r := gin.New()
		g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", tc.owner) })
		knowledge.NewChat(s, nil).Register(g)
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest("GET", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns/%d/evidence/%d", tc.lib, tc.conv, tc.turn, tc.event), nil))
		if out.Code != tc.status {
			t.Fatalf("case %+v: %d %s", tc, out.Code, out.Body.String())
		}
		if tc.status == 200 && out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable evidence")
		}
		if tc.status != 200 && strings.Contains(out.Body.String(), "原始证据") {
			t.Fatal("evidence leaked")
		}
	}
}

func TestKnowledgeEvidenceRejectsUnpairedResults(t *testing.T) {
	_, s, lib, conv, turn, src := evidenceFixture(t)
	ctx := context.Background()
	args := fmt.Sprintf(`{"source_id":%d,"version_id":%d}`, src.ID, src.CurrentVersionID)
	result, err := knowledge.NewReadingTools(s, 11, lib.ID)[1].InvokableRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	var wrong map[string]any
	if err := json.Unmarshal([]byte(result), &wrong); err != nil {
		t.Fatal(err)
	}
	wrong["version_id"] = src.CurrentVersionID + 100
	wrongJSON, _ := json.Marshal(wrong)
	err = s.AppendTurnEvents(ctx, turn.ID, []session.MemoryDTO{
		{Type: session.EventToolResult, ToolCallID: "orphan", ToolName: "read_source", Content: result},
		{Type: session.EventToolCall, ToolCalls: []session.ToolCallData{{ID: "failed", Name: "read_source", Arguments: args}, {ID: "mismatch", Name: "read_source", Arguments: args}}},
		{Type: session.EventToolResult, ToolCallID: "failed", ToolName: "read_source", Content: "工具执行失败"},
		{Type: session.EventToolResult, ToolCallID: "mismatch", ToolName: "read_source", Content: string(wrongJSON)},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || len(detail.Turns[0].Evidence) != 1 {
		t.Fatalf("untrusted evidence: %+v %v", detail, err)
	}
}
