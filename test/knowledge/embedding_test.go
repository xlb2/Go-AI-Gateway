package knowledge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go_im_gateway/internal/knowledge"
)

// TestEmbedderFromEnv 守护"配置边界"这条不变量：
// 全空 = 明确未启用（nil,nil）；只配一半 = 明确报错，绝不静默降级成关键词。
func TestEmbedderFromEnv(t *testing.T) {
	t.Setenv("EMBEDDING_BASE_URL", "")
	t.Setenv("EMBEDDING_API_KEY", "")
	t.Setenv("EMBEDDING_MODEL", "")
	if e, err := knowledge.EmbedderFromEnv(); err != nil || e != nil {
		t.Fatalf("empty config should disable embedding: e=%v err=%v", e, err)
	}
	t.Setenv("EMBEDDING_BASE_URL", "https://example.invalid/v1")
	if _, err := knowledge.EmbedderFromEnv(); err == nil {
		t.Fatal("partial config must error, not silently fall back")
	}
	t.Setenv("EMBEDDING_API_KEY", "k")
	t.Setenv("EMBEDDING_MODEL", "m")
	e, err := knowledge.EmbedderFromEnv()
	if err != nil || e == nil || e.Model() != "m" {
		t.Fatalf("full config: e=%v err=%v", e, err)
	}
}

// TestRemoteEmbedderBatchesAndOrders 守护三件事：按批调用（<=batch）、按 index 归位（乱序也能对）、
// 从响应学习维度并拼出稳定的 ID 指纹。
func TestRemoteEmbedderBatchesAndOrders(t *testing.T) {
	var mu sync.Mutex
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		batches = append(batches, len(req.Input))
		mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := []item{}
		// 故意逆序返回，验证 EmbedBatch 按 index 归位而不是按返回顺序。
		for i := len(req.Input) - 1; i >= 0; i-- {
			data = append(data, item{Index: i, Embedding: []float32{float32(i), 1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": len(req.Input)}})
	}))
	defer srv.Close()

	t.Setenv("EMBEDDING_BASE_URL", srv.URL)
	t.Setenv("EMBEDDING_API_KEY", "k")
	t.Setenv("EMBEDDING_MODEL", "test-model")
	t.Setenv("EMBEDDING_DIM", "")
	t.Setenv("KNOWLEDGE_EMBED_BATCH", "10")
	e, err := knowledge.EmbedderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 25)
	for i := range texts {
		texts[i] = "t"
	}
	emb, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Vectors) != 25 {
		t.Fatalf("got %d vectors", len(emb.Vectors))
	}
	if emb.PromptTokens != 25 {
		t.Fatalf("prompt tokens = %d, want 25", emb.PromptTokens)
	}
	if len(batches) != 3 || batches[0] != 10 || batches[1] != 10 || batches[2] != 5 {
		t.Fatalf("batch sizes = %v", batches)
	}
	if e.ID() != "test-model:4" {
		t.Fatalf("id = %s, want test-model:4 (dim learned from response)", e.ID())
	}
	// 每个请求的 index 是"批内局部下标"（OpenAI /embeddings 语义），不是全局下标。
	// 断言按局部下标归位：若实现按返回顺序放置，反向返回会立刻暴露。
	for start := 0; start < len(texts); {
		size := min(10, len(texts)-start)
		for k := 0; k < size; k++ {
			v := emb.Vectors[start+k]
			if len(v) != 4 || v[0] != float32(k) {
				t.Fatalf("vector[%d] = %v, batch-local index not honored", start+k, v)
			}
		}
		start += size
	}
}

// TestRemoteEmbedderRejectsBadResponse 守护"不静默接受坏数据"：条数不符或非 200 必须报错。
func TestRemoteEmbedderRejectsBadResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()
	t.Setenv("EMBEDDING_BASE_URL", srv.URL)
	t.Setenv("EMBEDDING_API_KEY", "k")
	t.Setenv("EMBEDDING_MODEL", "m")
	t.Setenv("EMBEDDING_DIM", "")
	e, err := knowledge.EmbedderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("count mismatch must error")
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer fail.Close()
	t.Setenv("EMBEDDING_BASE_URL", fail.URL)
	e2, err := knowledge.EmbedderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("non-200 must error")
	}
}

// TestRemoteEmbedderUsageTotalFallback 守护用量口径：只返回 total_tokens 时也要采集，
// 不能记成 0（0 会被当成"真实花费为零"）；两者都缺才记未知（-1）。
func TestRemoteEmbedderUsageTotalFallback(t *testing.T) {
	totalOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"index": 0, "embedding": []float32{1, 0, 0}}},
			"usage": map[string]int{"total_tokens": 7},
		})
	}))
	defer totalOnly.Close()
	t.Setenv("EMBEDDING_BASE_URL", totalOnly.URL)
	t.Setenv("EMBEDDING_API_KEY", "k")
	t.Setenv("EMBEDDING_MODEL", "m")
	t.Setenv("EMBEDDING_DIM", "3")
	e, err := knowledge.EmbedderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	emb, err := e.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if emb.PromptTokens != 7 {
		t.Fatalf("tokens = %d, want 7 (fallback to total_tokens)", emb.PromptTokens)
	}

	noUsage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0, 0}}},
		})
	}))
	defer noUsage.Close()
	t.Setenv("EMBEDDING_BASE_URL", noUsage.URL)
	e2, err := knowledge.EmbedderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	emb2, err := e2.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if emb2.PromptTokens != -1 {
		t.Fatalf("tokens = %d, want -1 (unknown)", emb2.PromptTokens)
	}
}
