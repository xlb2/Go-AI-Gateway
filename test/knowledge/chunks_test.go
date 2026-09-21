package knowledge_test

import (
	"strings"
	"testing"

	"go_im_gateway/internal/knowledge"
)

func TestKnowledgeChunksOriginalCoordinates(t *testing.T) {
	for _, body := range []string{"", "\ufeff# 标题\r\n```go\r\n中🧭\r\n```", strings.Repeat("中🧭\r\n", 501)} {
		chunks, err := knowledge.SplitDocument(body)
		if err != nil {
			t.Fatal(err)
		}
		runes := []rune(body)
		covered := 0
		for i, chunk := range chunks {
			if chunk.Ordinal != i+1 || chunk.Start > covered || chunk.End <= chunk.Start || chunk.End-chunk.Start > 800 || chunk.Content != string(runes[chunk.Start:chunk.End]) {
				t.Fatalf("invalid chunk %+v", chunk)
			}
			if i > 0 && chunks[i-1].End-chunk.Start != 100 {
				t.Fatal("overlap lost")
			}
			covered = chunk.End
		}
		if covered != len(runes) {
			t.Fatal("source not fully covered")
		}
	}
	if _, err := knowledge.SplitDocument(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}
