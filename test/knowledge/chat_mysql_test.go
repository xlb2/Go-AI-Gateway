//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go_im_gateway/internal/knowledge"
	"net/http/httptest"
	"strings"
	"testing"
)

type knowledgeChatModel struct {
	inputs [][]*schema.Message
	fail   bool
}

func (m *knowledgeChatModel) BindTools([]*schema.ToolInfo) error { return nil }
func (m *knowledgeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}
func (m *knowledgeChatModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	if m.fail {
		return nil, errors.New("private provider detail")
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
}

func TestKnowledgeChatHTTPIsolationAndStream(t *testing.T) {
	store := knowledge.NewStore(database(t))
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 11, "one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateLibrary(ctx, 22, "two")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := store.Import(ctx, 11, lib.ID, "fact.md", "md", "only-owner-eleven")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := store.Import(ctx, 22, other.ID, "secret.md", "md", "private-other-owner")
	if err != nil {
		t.Fatal(err)
	}
	m := &knowledgeChatModel{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("user_id", uint(11)); c.Next() })
	knowledge.NewChat(store, func(context.Context) (model.ChatModel, error) { return m, nil }).Register(g)
	request := func(library uint64, ids []uint64) *httptest.ResponseRecorder {
		owner := uint(11)
		if library == other.ID {
			owner = 22
		}
		conv, err := store.CreateConversation(ctx, owner, library)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(knowledge.TurnInput{RequestID: uuid.NewString(), Content: "question", SourceIDs: ids})
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns", library, conv.ID), strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	for _, pair := range [][2]uint64{{other.ID, foreign.ID}, {lib.ID, foreign.ID}} {
		if out := request(pair[0], []uint64{pair[1]}); out.Code != 404 {
			t.Fatalf("ownership status %d", out.Code)
		}
	}
	if len(m.inputs) != 0 {
		t.Fatal("unauthorized request reached model")
	}
	out := request(lib.ID, []uint64{src.ID})
	if out.Code != 200 {
		t.Fatalf("chat: %d %s", out.Code, out.Body.String())
	}
	var answer string
	done := false
	decoder := json.NewDecoder(out.Body)
	for decoder.More() {
		var event struct {
			Type    string
			Content string
		}
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "delta" {
			answer += event.Content
		}
		if event.Type == "done" {
			done = true
		}
	}
	if answer != "answer" || !done {
		t.Fatalf("incomplete response %q done=%v", answer, done)
	}
	if len(m.inputs) != 1 || !strings.Contains(m.inputs[0][1].Content, "only-owner-eleven") {
		t.Fatal("attachment not sent")
	}
	m.fail = true
	out = request(lib.ID, nil)
	if out.Code != 502 || strings.Contains(out.Body.String(), "private provider detail") {
		t.Fatal("provider error not sanitized")
	}
	m.fail = false
	if out = request(lib.ID, nil); out.Code != 200 {
		t.Fatal("active slot leaked after failure")
	}
	if len(m.inputs[len(m.inputs)-1]) != 2 || m.inputs[len(m.inputs)-1][1].Content != "question" {
		t.Fatal("previous history leaked")
	}
}
