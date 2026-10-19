//go:build knowledgeintegration && rageval

package knowledge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"go_im_gateway/internal/knowledge"
	"go_im_gateway/test/rag"
)

// TestRAGRetrievalComparison 产出"关键词基线 vs 向量 vs 混合"的 source-level recall@5 对照报告。
// 它真实调用远端 embedding API（语料嵌入 + 每问查询嵌入），会产生少量 token 费用。
// dev 用于迭代；holdout 用 KNOWLEDGE_RAG_SPLIT=holdout 只跑最终验收一次。
func TestRAGRetrievalComparison(t *testing.T) {
	output := os.Getenv("KNOWLEDGE_RAG_REPORT")
	if output == "" {
		t.Fatal("use scripts/eval-rag-compare.sh to select a report path")
	}
	split := os.Getenv("KNOWLEDGE_RAG_SPLIT")
	if split == "" {
		split = "dev"
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate corpus")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	workspace := filepath.Join(repo, "test/rag/corpus")
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

	manifestBytes := read(filepath.Join(repo, "test/rag/manifest.json"))
	var manifest struct {
		Version string `json:"version"`
		Sources []struct {
			ID     string `json:"id"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version == "" || len(manifest.Sources) == 0 {
		t.Fatal("empty corpus manifest")
	}
	bodies := map[string]string{}
	for _, source := range manifest.Sources {
		if source.ID == "" || bodies[source.ID] != "" || filepath.IsAbs(source.Path) {
			t.Fatal("invalid corpus source")
		}
		path := filepath.Join(workspace, filepath.FromSlash(source.Path))
		rel, err := filepath.Rel(workspace, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatal("corpus path leaves workspace")
		}
		data := read(path)
		if !strings.EqualFold(hash(data), source.SHA256) {
			t.Fatalf("corpus drift: %s; do not overwrite the manifest to hide changed content", source.ID)
		}
		bodies[source.ID] = string(data)
	}
	casesBytes := read(filepath.Join(repo, "test/rag/cases.json"))
	var cases struct {
		Version string     `json:"version"`
		Cases   []rag.Case `json:"cases"`
	}
	if err := json.Unmarshal(casesBytes, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases.Cases {
		if c.Split != split {
			continue
		}
		for _, id := range c.Sources {
			if _, ok := bodies[id]; !ok {
				t.Fatalf("unknown gold source %s", id)
			}
		}
	}

	embedder, err := knowledge.EmbedderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if embedder == nil {
		t.Fatal("对照评测需要 embedding：请在 WSL export EMBEDDING_BASE_URL / EMBEDDING_API_KEY / EMBEDDING_MODEL")
	}

	db := database(t)
	store := knowledge.NewStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	lib, err := store.CreateLibrary(ctx, 11, "K-0 retrieval comparison")
	if err != nil {
		t.Fatal(err)
	}
	labels := map[uint64]string{}
	docs := map[string]uint64{}
	for _, source := range manifest.Sources {
		doc, duplicate, err := store.Import(ctx, 11, lib.ID, filepath.Base(source.Path), "md", bodies[source.ID])
		if err != nil || duplicate {
			t.Fatalf("import %s: duplicate=%v err=%v", source.ID, duplicate, err)
		}
		labels[doc.ID] = source.ID
		docs[source.ID] = doc.ID
	}
	// 语料嵌入（真实 API 调用）
	corpusTokens := 0
	corpusTokensKnown := true
	for _, source := range manifest.Sources {
		idx, err := store.EmbedVersion(ctx, 11, lib.ID, docs[source.ID], embedder)
		if err != nil {
			t.Fatalf("embed %s: %v", source.ID, err)
		}
		if idx.PromptTokens < 0 {
			corpusTokensKnown = false
		} else {
			corpusTokens += idx.PromptTokens
		}
	}
	var corpusUsage any
	if corpusTokensKnown {
		corpusUsage = corpusTokens
	}

	label := func(dbID uint64) (string, bool) { id, ok := labels[dbID]; return id, ok }

	keywordRetrieve := func(ctx context.Context, question string) ([]string, error) {
		return rag.Rank(ctx, question, func(ctx context.Context, term string) ([]string, error) {
			// 翻页收齐该词全部候选（v2 语料 18 份，单词命中可超过单页 10 份）。
			items, err := store.SearchAllSources(ctx, 11, lib.ID, term)
			if err != nil {
				return nil, err
			}
			ids := []string{}
			for _, h := range items {
				if id, ok := label(h.SourceID); ok {
					ids = append(ids, id)
				}
			}
			return ids, nil
		})
	}
	vectorQueryTokens := 0
	vectorTokensKnown := true
	vectorRetrieve := func(ctx context.Context, question string) ([]string, error) {
		// 取足够多 chunk 后按“每个来源的最好 chunk 分”聚合，再取前 5 个来源 ——
		// 这样才与关键词/混合的“top-5 来源”同口径，而不是“top-5 chunk 去重后不足 5 个来源”。
		res, err := store.VectorSearch(ctx, 11, lib.ID, embedder, question, 50)
		if err != nil {
			return nil, err
		}
		if res.PromptTokens < 0 {
			vectorTokensKnown = false
		} else {
			vectorQueryTokens += res.PromptTokens
		}
		if !res.Indexed {
			return nil, nil // 没索引：返回空，recall=0，报告如实体现
		}
		best := map[uint64]float64{}
		for _, h := range res.Items {
			if s, ok := best[h.SourceID]; !ok || h.Score > s {
				best[h.SourceID] = h.Score
			}
		}
		type scored struct {
			id    uint64
			score float64
		}
		ranked := make([]scored, 0, len(best))
		for id, s := range best {
			ranked = append(ranked, scored{id, s})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].id < ranked[j].id
		})
		ids := []string{}
		for i, r := range ranked {
			if i >= 5 {
				break
			}
			if id, ok := label(r.id); ok {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
	hybridQueryTokens := 0
	hybridTokensKnown := true
	hybridRetrieve := func(ctx context.Context, question string) ([]string, error) {
		res, err := store.HybridSearch(ctx, 11, lib.ID, embedder, question, 5)
		if err != nil {
			return nil, err
		}
		if res.PromptTokens < 0 {
			hybridTokensKnown = false
		} else {
			hybridQueryTokens += res.PromptTokens
		}
		ids := []string{}
		for _, h := range res.Items {
			if id, ok := label(h.SourceID); ok {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}

	started := time.Now().UTC()
	keywordScores, err := rag.EvaluateSplit(ctx, cases.Cases, split, keywordRetrieve)
	if err != nil {
		t.Fatal(err)
	}
	vectorScores, err := rag.EvaluateSplit(ctx, cases.Cases, split, vectorRetrieve)
	if err != nil {
		t.Fatal(err)
	}
	hybridScores, err := rag.EvaluateSplit(ctx, cases.Cases, split, hybridRetrieve)
	if err != nil {
		t.Fatal(err)
	}

	queryUsage := func(known bool, tokens int) any {
		if known {
			return tokens
		}
		return nil
	}
	implementation := map[string]string{}
	for _, path := range []string{
		"internal/knowledge/search.go", "internal/knowledge/chunks.go",
		"internal/knowledge/vector.go", "internal/knowledge/hybrid.go", "internal/knowledge/embedding.go",
		"test/rag/evaluator.go",
	} {
		implementation[path] = hash(read(filepath.Join(repo, path)))
	}
	report := map[string]any{
		"started_at":         started,
		"duration_ms":        time.Since(started).Milliseconds(),
		"corpus":             manifest,
		"cases_version":      cases.Version,
		"cases_sha256":       hash(casesBytes),
		"manifest_sha256":    hash(manifestBytes),
		"split":              split,
		"query_policy":       rag.QueryPolicy,
		"chunk_rule":         knowledge.ChunkRuleVersion,
		"embedding":          map[string]any{"model": embedder.Model(), "rule": embedder.ID(), "corpus_prompt_tokens": corpusUsage},
		"keyword_query_tokens": nil,
		"vector_query_tokens":  queryUsage(vectorTokensKnown, vectorQueryTokens),
		"hybrid_query_tokens":  queryUsage(hybridTokensKnown, hybridQueryTokens),
		"scores": map[string]any{
			"keyword": keywordScores,
			"vector":  vectorScores,
			"hybrid":  hybridScores,
		},
		"implementation_sha256": implementation,
		"limitations": "source-level recall@5 only; no model answer, no answer correctness; keyword side uses a deterministic policy (no model); vector/hybrid call a real embedding API; PASS is not a quality threshold; only the requested split is scored",
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	t.Logf("split=%s recall@5 keyword=%.3f vector=%.3f hybrid=%.3f | recall@1 keyword=%.3f vector=%.3f hybrid=%.3f",
		split,
		keywordScores.MeanRecall, vectorScores.MeanRecall, hybridScores.MeanRecall,
		keywordScores.MeanRecallAt1, vectorScores.MeanRecallAt1, hybridScores.MeanRecallAt1)
	t.Logf("report: %s (execution PASS is not a quality threshold)", output)
}
