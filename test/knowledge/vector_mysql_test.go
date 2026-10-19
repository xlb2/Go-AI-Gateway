//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"testing"

	"go_im_gateway/internal/knowledge"
)

// fakeEmbedder 确定性假向量（按文本哈希生成的伪随机向量），不联网。
// 它的作用只是验证"索引/召回/隔离/规则"这些机制：相同文本 → 余弦 1，不同文本 → 低相关。
// 它不代表真实语义质量，不能用来证明向量比关键词强。
type fakeEmbedder struct {
	model string
	dim   int
}

func (f fakeEmbedder) Model() string { return f.model }
func (f fakeEmbedder) ID() string    { return f.model + ":" + strconv.Itoa(f.dim) }

// fakeVector 确定性伪随机向量：相同文本 → 相同向量（余弦 1），不同文本 → 低相关。
func fakeVector(text string, dim int) []float32 {
	v := make([]float32, dim)
	h := fnv.New32a()
	h.Write([]byte(text))
	seed := h.Sum32()
	for j := range v {
		seed = seed*1664525 + 1013904223
		v[j] = float32(int32(seed)) / float32(1<<31)
	}
	return v
}

func (f fakeEmbedder) Embed(_ context.Context, texts []string) (knowledge.Embedding, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeVector(t, f.dim)
	}
	return knowledge.Embedding{Vectors: out, PromptTokens: -1}, nil
}

// lazyEmbedder 模拟"进程重启后的 RemoteEmbedder"：维度在首次 Embed 前未知（0）。
// 用于复现"新进程维度未学到时直接 VectorSearch 已建索引"的时序问题。
type lazyEmbedder struct {
	model string
	dim   int
}

func (e *lazyEmbedder) Model() string { return e.model }
func (e *lazyEmbedder) ID() string    { return e.model + ":" + strconv.Itoa(e.dim) }

func (e *lazyEmbedder) Embed(_ context.Context, texts []string) (knowledge.Embedding, error) {
	if e.dim == 0 {
		e.dim = 8 // 模拟从首次响应学到维度
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeVector(t, e.dim)
	}
	return knowledge.Embedding{Vectors: out, PromptTokens: -1}, nil
}

// TestKnowledgeVectorSearchFreshProcess 守护"新进程直接查已建索引"不能被假降级：
// 维度尚未学到时，嵌入查询之前不能依赖 ID()（否则得到 model:0 匹配不到索引，
// 静默返回 indexed=false 让调用方退回关键词）。
func TestKnowledgeVectorSearchFreshProcess(t *testing.T) {
	db := database(t)
	store := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 41, "重启库")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("事件溯源与折叠水位 ", 60) + "唯一标记X"
	src, _, err := store.Import(ctx, 41, lib.ID, "X", "txt", body)
	if err != nil {
		t.Fatal(err)
	}
	// 用已学到维度的 embedder 建索引。
	if _, err := store.EmbedVersion(ctx, 41, lib.ID, src.ID, fakeEmbedder{model: "fake", dim: 8}); err != nil {
		t.Fatal(err)
	}
	// 模拟新进程：维度未知的新 embedder，直接查（不先 Embed）。
	fresh := &lazyEmbedder{model: "fake"}
	status, err := store.EmbeddingStatus(ctx, 41, lib.ID, src.ID, fresh)
	if err != nil || status.State != "ready" {
		t.Fatalf("fresh status = %+v err=%v (维度未学到时不得判成 outdated)", status, err)
	}
	res, err := store.VectorSearch(ctx, 41, lib.ID, fresh, body, 5)
	if err != nil || !res.Indexed || len(res.Items) == 0 || res.Items[0].SourceID != src.ID {
		t.Fatalf("fresh search: indexed=%v items=%v err=%v (不得假降级为关键词)", res.Indexed, res.Items, err)
	}
	if fresh.ID() != "fake:8" {
		t.Fatalf("维度未学到: %s", fresh.ID())
	}
}

func TestKnowledgeVectorIndexAndSearch(t *testing.T) {
	db := database(t)
	store := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 31, "向量库")
	if err != nil {
		t.Fatal(err)
	}
	bodyA := strings.Repeat("上下文压缩与保留线 ", 60) + "唯一标记A"
	bodyB := strings.Repeat("工具审批与沙箱隔离 ", 60) + "唯一标记B"
	srcA, _, err := store.Import(ctx, 31, lib.ID, "A", "txt", bodyA)
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := store.Import(ctx, 31, lib.ID, "B", "txt", bodyB)
	if err != nil {
		t.Fatal(err)
	}
	emb := fakeEmbedder{model: "fake", dim: 8}

	// 未建向量索引：indexed=false，且不能当成"没有相关内容"。
	if _, err := store.EmbedVersion(ctx, 31, lib.ID, srcA.ID, emb); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EmbedVersion(ctx, 31, lib.ID, srcB.ID, emb); err != nil {
		t.Fatal(err)
	}
	status, err := store.EmbeddingStatus(ctx, 31, lib.ID, srcA.ID, emb)
	if err != nil || status.State != "ready" || status.Index.Dim != 8 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	// 幂等：同规则重跑应覆盖而不是翻倍。
	if _, err := store.EmbedVersion(ctx, 31, lib.ID, srcA.ID, emb); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&knowledge.ChunkEmbedding{}).Where("version_id = ?", status.Index.VersionID).Count(&count)
	if count != int64(status.Index.ChunkCount) {
		t.Fatalf("rebuild duplicated embeddings: %d != %d", count, status.Index.ChunkCount)
	}

	// 用原文查询：该资料自己的分段余弦=1，应排第一。
	res, err := store.VectorSearch(ctx, 31, lib.ID, emb, bodyA, 5)
	if err != nil || !res.Indexed || len(res.Items) == 0 || res.Items[0].SourceID != srcA.ID {
		t.Fatalf("search A: indexed=%v items=%v err=%v", res.Indexed, res.Items, err)
	}

	// 跨用户隔离：owner 不匹配必须 ErrNotFound，而不是返回别的用户数据。
	if _, err := store.VectorSearch(ctx, 32, lib.ID, emb, bodyA, 5); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("cross-user search err=%v", err)
	}
	if _, err := store.EmbedVersion(ctx, 99, lib.ID, srcA.ID, emb); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("cross-user embed err=%v", err)
	}

	// 规则（模型/维度）不匹配：indexed=false，明确降级而不是拿旧向量硬查。
	other := fakeEmbedder{model: "other", dim: 8}
	mismatch, err := store.VectorSearch(ctx, 31, lib.ID, other, bodyA, 5)
	if err != nil || mismatch.Indexed {
		t.Fatalf("rule mismatch: %+v err=%v", mismatch, err)
	}

	// 重建为另一规则后，该资料不再出现在旧规则结果里（旧向量被清除，不混存）。
	if _, err := store.EmbedVersion(ctx, 31, lib.ID, srcA.ID, fakeEmbedder{model: "fake", dim: 16}); err != nil {
		t.Fatal(err)
	}
	resOld, err := store.VectorSearch(ctx, 31, lib.ID, emb, bodyA, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range resOld.Items {
		if hit.SourceID == srcA.ID {
			t.Fatal("source A still reachable via old embedding rule")
		}
	}

	// 空库（无向量索引）明确 indexed=false。
	empty, err := store.CreateLibrary(ctx, 31, "空库")
	if err != nil {
		t.Fatal(err)
	}
	none, err := store.VectorSearch(ctx, 31, empty.ID, emb, bodyA, 5)
	if err != nil || none.Indexed {
		t.Fatalf("empty library: %+v err=%v", none, err)
	}
}
