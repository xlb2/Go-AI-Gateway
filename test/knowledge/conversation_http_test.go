//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go_im_gateway/internal/knowledge"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeConversationHTTPWriteBarriers(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "library")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := &knowledgeChatModel{}
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", uint(11)) })
	knowledge.NewChat(s, func(context.Context) (model.ChatModel, error) { return m, nil }).Register(g)
	request := func(in knowledge.TurnInput) *httptest.ResponseRecorder {
		data, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns", lib.ID, conv.ID), strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	in := knowledge.TurnInput{RequestID: uuid.NewString(), Content: "question"}
	if err := db.Callback().Create().Before("gorm:create").Register("test:turn_insert", func(tx *gorm.DB) {
		if tx.Statement.Table == "knowledge_chat_turns" {
			tx.AddError(errors.New("injected write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	out := request(in)
	db.Callback().Create().Remove("test:turn_insert")
	if out.Code != 500 || len(m.inputs) != 0 {
		t.Fatal("model ran before question committed")
	}
	if err := db.Callback().Update().Before("gorm:update").Register("test:turn_finish", func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]interface{}); ok && tx.Statement.Table == "knowledge_chat_turns" && fields["status"] == "completed" {
			tx.AddError(errors.New("injected finish failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	out = request(in)
	db.Callback().Update().Remove("test:turn_finish")
	if len(m.inputs) != 1 || !strings.Contains(out.Body.String(), `"type":"error"`) || strings.Contains(out.Body.String(), `"type":"done"`) {
		t.Fatalf("false completion: %s", out.Body.String())
	}
	detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || detail.Turns[0].Status != "running" {
		t.Fatalf("finish failure lost evidence: %v", err)
	}
	out = request(in)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"replayed":true`) || len(m.inputs) != 1 {
		t.Fatal("retry replayed model")
	}
}

func TestKnowledgeConversationHTTPCancellation(t *testing.T) {
	s := knowledge.NewStore(database(t))
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "library")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", uint(11)) })
	knowledge.NewChat(s, func(ctx context.Context) (model.ChatModel, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}).Register(g)
	data, _ := json.Marshal(knowledge.TurnInput{RequestID: uuid.NewString(), Content: "question"})
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns", lib.ID, conv.ID), strings.NewReader(string(data))).WithContext(requestCtx)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { defer close(done); r.ServeHTTP(httptest.NewRecorder(), req) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model not reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("cancellation did not return")
	}
	detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || detail.Turns[0].Status != "canceled" {
		t.Fatalf("cancel state not persisted: %+v %v", detail, err)
	}
}
