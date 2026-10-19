package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Embedder 是可替换的 embedding 端口（设计文档 test/rag/vector-retrieval-design.md 第 2 节）。
// 为什么单独抽一层：换模型或换本地实现时只改这里的实现与装配，callers 不动。
type Embedder interface {
	// Model 返回模型名（用于索引记录与排查）。
	Model() string
	// ID 返回 embedding_rule 指纹（模型:维度）。写入索引与查询必须一致 ——
	// 不同向量空间混查不会报错，只会静默给出无意义结果，所以必须在存储层锁定。
	ID() string
	Embed(ctx context.Context, texts []string) (Embedding, error)
}

// Embedding 一次 embedding 结果。PromptTokens < 0 表示服务端未返回用量（记为未知，不填零）。
type Embedding struct {
	Vectors      [][]float32
	PromptTokens int
}

const (
	// defaultEmbedBatch 实测端点单请求最多 10 条（10 条 OK；25/30 条返回 400）。
	defaultEmbedBatch = 10
	embedHTTPTimeout  = 60 * time.Second
)

// RemoteEmbedder 调用 OpenAI 兼容的 /embeddings 端点。
type RemoteEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	batch   int
	client  *http.Client

	mu  sync.Mutex
	dim int // 0 = 尚未从响应学到
}

// EmbedderFromEnv 按环境变量装配。三个必要变量全空 → (nil, nil) 表示"未启用向量"；
// 只配了一部分 → 报错，避免半配置静默降级成关键词（那样使用者会以为向量已生效）。
func EmbedderFromEnv() (Embedder, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("EMBEDDING_BASE_URL")), "/")
	key := strings.TrimSpace(os.Getenv("EMBEDDING_API_KEY"))
	model := strings.TrimSpace(os.Getenv("EMBEDDING_MODEL"))
	if base == "" && key == "" && model == "" {
		return nil, nil
	}
	if base == "" || key == "" || model == "" {
		return nil, fmt.Errorf("embedding 配置不完整：EMBEDDING_BASE_URL / EMBEDDING_API_KEY / EMBEDDING_MODEL 需同时提供")
	}
	batch := defaultEmbedBatch
	if n, ok := embEnvInt("KNOWLEDGE_EMBED_BATCH"); ok && n > 0 {
		batch = n
	}
	e := &RemoteEmbedder{baseURL: base, apiKey: key, model: model, batch: batch, client: &http.Client{Timeout: embedHTTPTimeout}}
	if n, ok := embEnvInt("EMBEDDING_DIM"); ok && n > 0 {
		e.dim = n
	}
	return e, nil
}

func embEnvInt(key string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (e *RemoteEmbedder) Model() string { return e.model }

func (e *RemoteEmbedder) ID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("%s:%d", e.model, e.dim)
}

func (e *RemoteEmbedder) setDim(dim int) {
	e.mu.Lock()
	e.dim = dim
	e.mu.Unlock()
}

// Embed 按批次调用端点，返回与 texts 顺序一致的向量。批次之间有一处小停顿没有加：
// 端点限流未观测到，先按最小实现走，遇到 429 再补（不做没有依据的重试）。
func (e *RemoteEmbedder) Embed(ctx context.Context, texts []string) (Embedding, error) {
	result := Embedding{PromptTokens: -1}
	if len(texts) == 0 {
		return result, nil
	}
	for start := 0; start < len(texts); start += e.batch {
		end := min(start+e.batch, len(texts))
		vectors, tokens, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return Embedding{}, err
		}
		result.Vectors = append(result.Vectors, vectors...)
		if tokens >= 0 {
			if result.PromptTokens < 0 {
				result.PromptTokens = 0
			}
			result.PromptTokens += tokens
		}
	}
	return result, nil
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (e *RemoteEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, int, error) {
	payload, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, -1, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, -1, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, -1, fmt.Errorf("embedding 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, -1, err
	}
	if resp.StatusCode != http.StatusOK {
		// 只回显有限的响应体，避免把整页错误或密钥回带给上层日志。
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return nil, -1, fmt.Errorf("embedding 端点返回 %d: %s", resp.StatusCode, snippet)
	}
	var parsed embedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, -1, fmt.Errorf("embedding 响应解析失败: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, -1, fmt.Errorf("embedding 返回 %d 条，期望 %d 条", len(parsed.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for pos, item := range parsed.Data {
		idx := item.Index
		// 端点可能乱序或省略 index；越界/重复时回退到返回顺序，不静默丢条。
		if idx < 0 || idx >= len(texts) || vectors[idx] != nil {
			idx = pos
		}
		if idx < 0 || idx >= len(texts) || vectors[idx] != nil {
			return nil, -1, fmt.Errorf("embedding 响应索引异常")
		}
		vectors[idx] = item.Embedding
	}
	for _, v := range vectors {
		if len(v) == 0 {
			return nil, -1, fmt.Errorf("embedding 返回空向量")
		}
		if e.dim == 0 {
			e.setDim(len(v))
		}
		if len(v) != e.dim {
			return nil, -1, fmt.Errorf("embedding 维度不一致：得到 %d，期望 %d", len(v), e.dim)
		}
	}
	// text-embedding-v3 只返回 total_tokens；优先 prompt_tokens，缺失则用 total_tokens 兜底，
	// 两者都缺才记未知（-1），不把"没返回"误记成 0。
	tokens := -1
	if parsed.Usage != nil {
		switch {
		case parsed.Usage.PromptTokens > 0:
			tokens = parsed.Usage.PromptTokens
		case parsed.Usage.TotalTokens > 0:
			tokens = parsed.Usage.TotalTokens
		}
	}
	return vectors, tokens, nil
}
