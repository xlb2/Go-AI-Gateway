//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go_im_gateway/internal/knowledge"
)

// TestKnowledgeHybridSearchFusion 守护 RRF 融合与降级：
// 两路都能命中时按融合排序；向量不可用时退化为仅关键词但明确 VectorIndexed=false；
// 新进程直接调用不得假降级；跨用户拒绝。
func TestKnowledgeHybridSearchFusion(t *testing.T) {
	db := database(t)
	store := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 51, "混合库")
	if err != nil {
		t.Fatal(err)
	}
	// 不带尾随空格：HybridSearch 会对 query 做 TrimSpace，带空格会让"查询文本 ≠ chunk 原文"，
	// A 对自己就不是余弦 1（这是测试数据的坑，不是融合逻辑问题）。
	bodyA := strings.Repeat("关键词A专用", 60)
	bodyB := strings.Repeat("关键词B专用", 60)
	srcA, _, err := store.Import(ctx, 51, lib.ID, "A", "txt", bodyA)
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := store.Import(ctx, 51, lib.ID, "B", "txt", bodyB)
	if err != nil {
		t.Fatal(err)
	}
	emb := fakeEmbedder{model: "fake", dim: 8}
	if _, err := store.EmbedVersion(ctx, 51, lib.ID, srcA.ID, emb); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EmbedVersion(ctx, 51, lib.ID, srcB.ID, emb); err != nil {
		t.Fatal(err)
	}

	// 用 A 的原文查询：向量侧对 A 余弦=1 必排第一，关键词侧 A 命中的词也更多。
	res, err := store.HybridSearch(ctx, 51, lib.ID, emb, bodyA, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !res.VectorIndexed {
		t.Fatal("vector side should be indexed")
	}
	if len(res.Items) == 0 || res.Items[0].SourceID != srcA.ID {
		t.Fatalf("top hit = %+v, want A", res.Items)
	}
	if res.Items[0].KeywordRank < 1 || res.Items[0].VectorRank != 1 {
		t.Fatalf("A ranks kw=%d vec=%d, want both sides", res.Items[0].KeywordRank, res.Items[0].VectorRank)
	}

	// 新进程：维度未知的 embedder 直接混合检索也不得假降级。
	fresh := &lazyEmbedder{model: "fake"}
	res2, err := store.HybridSearch(ctx, 51, lib.ID, fresh, bodyA, 5)
	if err != nil || len(res2.Items) == 0 || res2.Items[0].SourceID != srcA.ID {
		t.Fatalf("fresh hybrid: items=%+v err=%v", res2.Items, err)
	}

	// 跨用户拒绝。
	if _, err := store.HybridSearch(ctx, 99, lib.ID, emb, bodyA, 5); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("cross-user hybrid err=%v", err)
	}

	// 无向量索引的库：VectorIndexed=false，但关键词侧仍能返回结果（不谎报向量已生效）。
	empty, err := store.CreateLibrary(ctx, 51, "无索引库")
	if err != nil {
		t.Fatal(err)
	}
	srcC, _, err := store.Import(ctx, 51, empty.ID, "C", "txt", strings.Repeat("独有术语C ", 60))
	if err != nil {
		t.Fatal(err)
	}
	res3, err := store.HybridSearch(ctx, 51, empty.ID, emb, "独有术语C", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res3.VectorIndexed {
		t.Fatal("library without vector index must report VectorIndexed=false")
	}
	if len(res3.Items) == 0 || res3.Items[0].SourceID != srcC.ID {
		t.Fatalf("keyword-only hybrid failed: %+v", res3.Items)
	}
}

// TestKnowledgeHybridToolScope 守护工具契约：不传 embedder 时仍是 3 个工具（search_sources
// 契约不变）；传 embedder 才追加第 4 个 search_sources_hybrid，且授权仍按 owner/library。
func TestKnowledgeHybridToolScope(t *testing.T) {
	db := database(t)
	store := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 52, "工具库")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("混合检索工具验证 ", 60)
	src, _, err := store.Import(ctx, 52, lib.ID, "T", "txt", body)
	if err != nil {
		t.Fatal(err)
	}
	emb := fakeEmbedder{model: "fake", dim: 8}
	if _, err := store.EmbedVersion(ctx, 52, lib.ID, src.ID, emb); err != nil {
		t.Fatal(err)
	}

	if base := knowledge.NewReadingTools(store, 52, lib.ID); len(base) != 3 {
		t.Fatalf("base tools = %d, want 3 (search_sources contract unchanged)", len(base))
	}
	tools := knowledge.NewReadingToolsWithEmbedder(store, 52, lib.ID, emb)
	if len(tools) != 4 {
		t.Fatalf("tools = %d, want 4", len(tools))
	}
	info, err := tools[3].Info(ctx)
	if err != nil || info.Name != "search_sources_hybrid" {
		t.Fatalf("tool[3] info=%+v err=%v", info, err)
	}
	out, err := tools[3].InvokableRun(ctx, `{"query":"混合检索工具验证","top_k":3}`)
	if err != nil {
		t.Fatal(err)
	}
	var parsed knowledge.HybridResults
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) == 0 || parsed.Items[0].SourceID != src.ID {
		t.Fatalf("hybrid tool items = %+v", parsed.Items)
	}
	// 跨用户：工具仍按 owner/library 授权，不得越权。
	crossUser := knowledge.NewReadingToolsWithEmbedder(store, 99, lib.ID, emb)[3]
	if _, err := crossUser.InvokableRun(ctx, `{"query":"混合检索工具验证"}`); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("cross-user hybrid tool err=%v", err)
	}
}

// TestKnowledgeHybridKeywordMultiPage 守护关键词侧翻页：一个查询词命中超过单页 10 份时
// 必须翻页收齐候选（2026-10 v2 语料暴露：只取第一页会漏掉排名靠后的来源，
// 评测与混合排名都依赖完整计数）；超过全局页数上限才允许明确失败。
func TestKnowledgeHybridKeywordMultiPage(t *testing.T) {
	db := database(t)
	store := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 53, "翻页库")
	if err != nil {
		t.Fatal(err)
	}
	// 12 份来源都含共同词，单页 10 份装不下。
	for i := 0; i < 12; i++ {
		body := fmt.Sprintf("共同术语 第%02d份 ", i) + strings.Repeat("占位文本 ", 30)
		if _, _, err := store.Import(ctx, 53, lib.ID, fmt.Sprintf("p%02d", i), "txt", body); err != nil {
			t.Fatal(err)
		}
	}
	// nil embedder：纯关键词路径，向量侧明确未生效。
	res, err := store.HybridSearch(ctx, 53, lib.ID, nil, "共同术语", 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.VectorIndexed {
		t.Fatal("nil embedder must report VectorIndexed=false")
	}
	if res.KeywordSources != 12 {
		t.Fatalf("KeywordSources = %d, want 12（翻页收齐全部候选）", res.KeywordSources)
	}
	if len(res.Items) != 12 {
		t.Fatalf("items = %d, want 12", len(res.Items))
	}
}
