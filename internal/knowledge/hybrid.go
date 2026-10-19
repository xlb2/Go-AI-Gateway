package knowledge

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// hybridRRFK RRF 的平滑常数（业界常用 60）。RRF 只用排名、不用原始分数，
// 因此天然回避"关键词命中数"和"余弦相似度"量纲不可加的问题（见设计文档第 4 节）。
const hybridRRFK = 60.0

// maxHybridTerms 限制关键词侧的查询词数量：每个词一次 SearchSources 查询，
// 词太多会把一次检索放大成几十次 DB 往返。
const maxHybridTerms = 32

var queryIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_./:-]*`)

// HybridHit 一条混合召回候选：保留两侧各自的坐标供 read_source 定位，不返回正文。
// KeywordRank / VectorRank 为 1 起；0 表示该侧未命中。Score 是 RRF 融合分。
type HybridHit struct {
	SourceID    uint64  `json:"source_id"`
	VersionID   uint64  `json:"version_id"`
	Offset      int     `json:"offset,omitempty"`       // 关键词精确命中位置
	ChunkStart  int     `json:"chunk_start,omitempty"`  // 向量命中分段起点
	KeywordRank int     `json:"keyword_rank,omitempty"` // 1-based；0 = 未命中
	VectorRank  int     `json:"vector_rank,omitempty"`  // 1-based；0 = 未命中
	Score       float64 `json:"rrf_score"`
}

// HybridResults VectorIndexed=false 表示向量侧没有就绪索引，本次实际是"关键词单独"结果，
// 调用方要据此知道融合没有生效，不能当成"向量也查了"。
type HybridResults struct {
	Query          string      `json:"query"`
	Terms          []string    `json:"terms"`
	KeywordSources int         `json:"keyword_sources"`
	VectorIndexed  bool        `json:"vector_indexed"`
	Items          []HybridHit `json:"items"`
	// PromptTokens 向量侧查询嵌入消耗的 token；-1 表示未返回/未发生（不填零）。
	PromptTokens int `json:"prompt_tokens"`
}

// QueryTerms 从自然语言问题里抽取关键词侧使用的术语：英文/代码标识符 + 连续汉字的相邻双字词。
// 为什么不用整句做关键词：自然语言整句几乎不会字面命中，关键词侧的价值在于精确术语/代码名。
// 该算法与 test/rag/evaluator.go 的 QueryTerms 保持一致，保证评测里三路可同桌对比。
func QueryTerms(query string) []string {
	terms := []string{}
	seen := map[string]bool{}
	add := func(term string) {
		if !seen[term] && len(terms) < maxHybridTerms {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	for _, term := range queryIdentifier.FindAllString(query, -1) {
		add(term)
	}
	var previous rune
	for _, r := range query {
		if !unicode.Is(unicode.Han, r) {
			previous = 0
			continue
		}
		if previous != 0 {
			add(string([]rune{previous, r}))
		}
		previous = r
	}
	return terms
}

// HybridSearch 融合关键词与向量两路召回到 source 粒度（RRF）。
//
// 为什么停在 source 粒度：现有 K-0 题集按 source 标注标准来源，source 粒度让三路可直接对比
// （见设计文档第 4 节）；chunk 级只作为附加坐标带上，供 read_source 定位。
//
// embedder 为 nil 或向量索引不可用时，退化为"仅关键词"结果，并置 VectorIndexed=false。
func (s *Store) HybridSearch(ctx context.Context, owner uint, library uint64, embedder Embedder, query string, topK int) (HybridResults, error) {
	result := HybridResults{Query: query, Items: []HybridHit{}, PromptTokens: -1}
	query = strings.TrimSpace(query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 500 || strings.ContainsRune(query, 0) {
		return result, ErrInvalid
	}
	if topK <= 0 {
		topK = 8
	}
	if topK > 50 {
		topK = 50
	}

	// ---- 关键词侧：按"命中不同查询词数"排名（与评测 Rank 同口径）----
	terms := QueryTerms(query)
	result.Terms = terms
	kwCount := map[uint64]int{}
	kwHit := map[uint64]KeywordHit{}
	for _, term := range terms {
		// 翻页收齐该词的全部候选：混合排名依赖完整计数，只取第一页会漏掉排名靠后的来源。
		items, err := s.SearchAllSources(ctx, owner, library, term)
		if err != nil {
			return result, err
		}
		seenThisTerm := map[uint64]bool{}
		for _, h := range items {
			if !seenThisTerm[h.SourceID] {
				seenThisTerm[h.SourceID] = true
				kwCount[h.SourceID]++
			}
			if _, ok := kwHit[h.SourceID]; !ok {
				kwHit[h.SourceID] = h
			}
		}
	}
	kwRank := rankedSources(kwCount)
	result.KeywordSources = len(kwRank)

	// ---- 向量侧：chunk 级结果按最好分去重到 source ----
	vecRank := []uint64{}
	vecHit := map[uint64]VectorHit{}
	if embedder != nil {
		vec, err := s.VectorSearch(ctx, owner, library, embedder, query, topK*4)
		if err != nil {
			return result, err
		}
		result.VectorIndexed = vec.Indexed
		result.PromptTokens = vec.PromptTokens
		for _, h := range vec.Items {
			if _, ok := vecHit[h.SourceID]; !ok {
				vecHit[h.SourceID] = h
				vecRank = append(vecRank, h.SourceID)
			}
		}
	}

	// ---- RRF 融合：只加排名倒数，不加原始分 ----
	score := map[uint64]float64{}
	for rank, sid := range kwRank {
		score[sid] += 1 / (hybridRRFK + float64(rank+1))
	}
	for rank, sid := range vecRank {
		score[sid] += 1 / (hybridRRFK + float64(rank+1))
	}
	kwPos := rankPositions(kwRank)
	vecPos := rankPositions(vecRank)
	items := make([]HybridHit, 0, len(score))
	for sid, sc := range score {
		item := HybridHit{SourceID: sid, Score: sc}
		if p, ok := kwPos[sid]; ok {
			item.KeywordRank = p
			h := kwHit[sid]
			item.VersionID = h.VersionID
			item.Offset = h.Offset
		}
		if p, ok := vecPos[sid]; ok {
			item.VectorRank = p
			h := vecHit[sid]
			if item.VersionID == 0 {
				item.VersionID = h.VersionID
			}
			item.ChunkStart = h.ChunkStart
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].SourceID < items[j].SourceID
	})
	if len(items) > topK {
		items = items[:topK]
	}
	result.Items = items
	return result, nil
}

// rankedSources 把"source -> 命中词数"排序成排名列表：命中多者靠前，同分按 source id 稳定。
func rankedSources(counts map[uint64]int) []uint64 {
	ids := make([]uint64, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if counts[ids[i]] != counts[ids[j]] {
			return counts[ids[i]] > counts[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}

// rankPositions 把有序 source 列表转成 1-based 名次表。
func rankPositions(ordered []uint64) map[uint64]int {
	pos := make(map[uint64]int, len(ordered))
	for i, id := range ordered {
		if _, ok := pos[id]; !ok {
			pos[id] = i + 1
		}
	}
	return pos
}
