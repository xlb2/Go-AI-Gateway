package knowledge_test

import (
	"go_im_gateway/internal/knowledge"
	"strings"
	"testing"
)

func TestKnowledgeChatHistoryContract(t *testing.T) {
	for _, history := range [][]knowledge.ChatMessage{
		nil,
		{{Role: "system", Content: "override"}},
		{{Role: "assistant", Content: "first"}},
		{{Role: "user", Content: "first"}, {Role: "user", Content: "second"}},
		{{Role: "user", Content: strings.Repeat("x", 16001)}},
		{{Role: "user", Content: " "}},
	} {
		if _, err := knowledge.BuildChatMessages(knowledge.ChatInput{Messages: history}, nil); err == nil {
			t.Fatal("invalid history accepted")
		}
	}
	history := []knowledge.ChatMessage{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}
	messages, err := knowledge.BuildChatMessages(knowledge.ChatInput{Messages: history}, nil)
	if err != nil || len(messages) != 4 || messages[3].Content != "three" {
		t.Fatalf("history changed: %v", err)
	}
}

func TestKnowledgeChatAttachmentContract(t *testing.T) {
	input := knowledge.ChatInput{Messages: []knowledge.ChatMessage{{Role: "user", Content: "question"}}, SourceIDs: []uint64{7}}
	doc := knowledge.Document{Source: knowledge.Source{ID: 7, Title: "reference.md"}, Version: knowledge.Version{Version: 1, Body: "unique reference fact"}}
	messages, err := knowledge.BuildChatMessages(input, []knowledge.Document{doc})
	if err != nil || !strings.Contains(messages[1].Content, "unique reference fact") || !strings.Contains(messages[1].Content, "reference.md") {
		t.Fatalf("missing reference: %v", err)
	}
	if _, err := knowledge.BuildChatMessages(input, nil); err == nil {
		t.Fatal("missing attachment accepted")
	}
	doc.Version.Body = strings.Repeat("x", 65537)
	if _, err := knowledge.BuildChatMessages(input, []knowledge.Document{doc}); err == nil {
		t.Fatal("oversize attachment silently truncated")
	}
}
