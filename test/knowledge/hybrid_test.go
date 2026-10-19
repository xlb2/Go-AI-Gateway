package knowledge_test

import (
	"strings"
	"testing"

	"go_im_gateway/internal/knowledge"
)

func hasTerm(terms []string, want string) bool {
	for _, term := range terms {
		if term == want {
			return true
		}
	}
	return false
}

// TestKnowledgeQueryTerms 守护关键词侧的查询规划口径：英文/代码标识符 + 连续汉字双字词，
// 去重、有上限。它必须与 test/rag/evaluator.go 的 QueryTerms 同口径，三路评测才可比。
func TestKnowledgeQueryTerms(t *testing.T) {
	terms := knowledge.QueryTerms("RunAgentTurn 里 CompactionTo 和保留线的区别")
	for _, want := range []string{"RunAgentTurn", "CompactionTo", "保留", "留线"} {
		if !hasTerm(terms, want) {
			t.Fatalf("QueryTerms missing %q: %v", want, terms)
		}
	}
	dup := knowledge.QueryTerms("保留保留")
	count := 0
	for _, term := range dup {
		if term == "保留" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("bigram not deduplicated: %v", dup)
	}
	if got := len(knowledge.QueryTerms(strings.Repeat("测试", 100))); got > 32 {
		t.Fatalf("term cap exceeded: %d", got)
	}
	if len(knowledge.QueryTerms("   ")) != 0 {
		t.Fatal("whitespace produced terms")
	}
}
