package knowledge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxVectorScan 暴力检索的候选上限。为什么要有：暴力是 O(N) 的，必须有显式天花板，
// 超过就明确报错要求上 ANN，而不是悄悄扫完整个库把延迟拖垮。
const maxVectorScan = 20000

// ChunkEmbedding 一个分段向量。vector 是归一化后的 float32 小端字节（cosine == 点积）。
type ChunkEmbedding struct {
	ID            uint64 `gorm:"primaryKey"`
	ChunkID       uint64 `gorm:"column:chunk_id"`
	VersionID     uint64 `gorm:"column:version_id"`
	EmbeddingRule string `gorm:"column:embedding_rule"`
	Dim           int    `gorm:"column:dim"`
	Vector        []byte `gorm:"column:vector"`
}

func (ChunkEmbedding) TableName() string { return "knowledge_chunk_embeddings" }

// EmbeddingIndex 一个版本的向量索引记录（一个版本同一时刻只有一条有效规则）。
type EmbeddingIndex struct {
	VersionID     uint64    `gorm:"primaryKey"`
	EmbeddingRule string    `gorm:"column:embedding_rule"`
	Model         string    `gorm:"column:model"`
	Dim           int       `gorm:"column:dim"`
	ChunkCount    int       `gorm:"column:chunk_count"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
	// PromptTokens 本次嵌入消耗的 token；-1 表示端点未返回（不填零）。非数据库列，仅供计量。
	PromptTokens int `json:"prompt_tokens" gorm:"-"`
}

func (EmbeddingIndex) TableName() string { return "knowledge_embedding_indexes" }

// EmbeddingStatus 与分段索引同构的状态：missing / ready / outdated。
type EmbeddingStatus struct {
	State string         `json:"state"`
	Index EmbeddingIndex `json:"index"`
}

// VectorHit 一次向量召回的候选：定位到分段坐标 + 余弦分数。
// 返回坐标而不是正文，沿用 evidence 机制 —— 让模型自己去 read_source，而不是把召回当证据。
type VectorHit struct {
	SourceID   uint64  `json:"source_id"`
	VersionID  uint64  `json:"version_id"`
	ChunkStart int     `json:"chunk_start"`
	ChunkEnd   int     `json:"chunk_end"`
	Score      float64 `json:"score"`
}

// VectorResults indexed=false 表示本库没有该规则的就绪索引；调用方必须降级为关键词，
// 不能把"没有向量索引"当成"资料库里没有相关内容"。
type VectorResults struct {
	EmbeddingRule string      `json:"embedding_rule"`
	Indexed       bool        `json:"indexed"`
	Chunks        int         `json:"scanned_chunks"`
	Items         []VectorHit `json:"items"`
	// PromptTokens 查询嵌入消耗的 token；-1 表示未返回/未发生（不填零）。
	PromptTokens int `json:"prompt_tokens"`
}

// normalizeVector 归一化成单位向量，使 cosine 相似度退化成点积（省一次开方/除法）。
func normalizeVector(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := 1 / math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// encodeVector 序列化为 float32 小端字节。用不变长度而不是 JSON：省空间，也避免浮点文本精度抖动。
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func decodeVector(b []byte, dim int) ([]float32, error) {
	if dim <= 0 || len(b) != dim*4 {
		return nil, fmt.Errorf("向量字节长度与维度不匹配")
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}

func (s *Store) versionChunks(ctx context.Context, versionID uint64) ([]Chunk, error) {
	chunks := []Chunk{}
	err := s.db.WithContext(ctx).Where("version_id = ?", versionID).Order("ordinal ASC").Find(&chunks).Error
	return chunks, err
}

// EmbedVersion 为某个资料的当前版本建立向量索引（幂等：同版本重跑覆盖）。
//
// 关键取舍：网络调用放在事务外。事务里做网络是坏味道（长事务、失败不可回滚外部调用）；
// 向量与版本的正确性由事务内的"当前版本复核 + 先删后插 + 索引记录"保证。
func (s *Store) EmbedVersion(ctx context.Context, owner uint, library, source uint64, embedder Embedder) (EmbeddingIndex, error) {
	if embedder == nil {
		return EmbeddingIndex{}, fmt.Errorf("embedder 未配置")
	}
	if err := ctx.Err(); err != nil {
		return EmbeddingIndex{}, err
	}
	doc, err := s.Read(ctx, owner, library, source)
	if err != nil {
		return EmbeddingIndex{}, err
	}
	chunks, err := s.versionChunks(ctx, doc.Version.ID)
	if err != nil {
		return EmbeddingIndex{}, err
	}
	if len(chunks) == 0 {
		return EmbeddingIndex{}, fmt.Errorf("该版本没有分段，先建分段索引再嵌入")
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}
	emb, err := embedder.Embed(ctx, texts)
	if err != nil {
		return EmbeddingIndex{}, fmt.Errorf("embedding 调用失败: %w", err)
	}
	if len(emb.Vectors) != len(chunks) {
		return EmbeddingIndex{}, fmt.Errorf("向量数量 %d 与分段数 %d 不符", len(emb.Vectors), len(chunks))
	}
	rule := embedder.ID()
	dim := 0
	rows := make([]ChunkEmbedding, len(chunks))
	for i := range chunks {
		v := normalizeVector(emb.Vectors[i])
		if len(v) == 0 {
			return EmbeddingIndex{}, fmt.Errorf("embedding 返回空向量")
		}
		if dim == 0 {
			dim = len(v)
		}
		if len(v) != dim {
			return EmbeddingIndex{}, fmt.Errorf("向量维度不一致")
		}
		rows[i] = ChunkEmbedding{ChunkID: chunks[i].ID, VersionID: doc.Version.ID, EmbeddingRule: rule, Dim: dim, Vector: encodeVector(v)}
	}

	var result EmbeddingIndex
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owned(tx, owner, library, true); err != nil {
			return err
		}
		var current uint64
		if err := tx.Model(&Source{}).Where("id = ? AND library_id = ?", source, library).Pluck("current_version_id", &current).Error; err != nil {
			return err
		}
		if current != doc.Version.ID {
			return fmt.Errorf("资料在嵌入期间被更新，请重试")
		}
		// 一个版本同一时刻只保留一条规则的有效向量：先清旧规则，避免向量空间混存。
		if err := tx.Where("version_id = ?", doc.Version.ID).Delete(&ChunkEmbedding{}).Error; err != nil {
			return err
		}
		if err := tx.CreateInBatches(&rows, 100).Error; err != nil {
			return err
		}
		result = EmbeddingIndex{VersionID: doc.Version.ID, EmbeddingRule: rule, Model: embedder.Model(), Dim: dim, ChunkCount: len(rows), UpdatedAt: time.Now().UTC(), PromptTokens: emb.PromptTokens}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "version_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"embedding_rule", "model", "dim", "chunk_count", "updated_at"}),
		}).Create(&result).Error
	})
	return result, err
}

// EmbeddingStatus 报告某资料当前版本的向量索引状态。规则与传入口径不同即为 outdated。
func (s *Store) EmbeddingStatus(ctx context.Context, owner uint, library, source uint64, embedder Embedder) (EmbeddingStatus, error) {
	doc, err := s.Read(ctx, owner, library, source)
	if err != nil {
		return EmbeddingStatus{}, err
	}
	result := EmbeddingStatus{State: "missing", Index: EmbeddingIndex{VersionID: doc.Version.ID}}
	err = s.db.WithContext(ctx).Where("version_id = ?", doc.Version.ID).First(&result.Index).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		return EmbeddingStatus{}, err
	}
	result.State = "ready"
	// 按"模型"判断过期，而不是"模型:维度"：进程重启后维度可能尚未从响应学到，
	// 用 ID() 会误判成 outdated。同模型改维度这种少见情况由 VectorSearch 的维度校验兜住。
	if embedder != nil && result.Index.Model != embedder.Model() {
		result.State = "outdated"
	}
	return result, nil
}

// activeEmbeddingRules 返回本库当前版本、指定模型下已就绪的 embedding_rule 集合。
// 为什么按 model 而不是完整 rule 查：rule 含维度，而维度可能尚未学到（model:0），
// 用它查会匹配不到索引，把"已建索引"静默降级成"没索引"（假降级）。
func (s *Store) activeEmbeddingRules(db *gorm.DB, library uint64, model string) ([]string, error) {
	rules := []string{}
	err := db.Raw(`SELECT DISTINCT i.embedding_rule FROM knowledge_embedding_indexes i
	JOIN knowledge_sources s ON s.current_version_id = i.version_id
	WHERE s.library_id = ? AND i.model = ?`, library, model).Scan(&rules).Error
	return rules, err
}

// VectorSearch 对查询向量做暴力余弦召回（owner/library/当前版本/模型四重过滤）。
// 先按"模型"确认存在就绪索引再嵌入查询：既避免"没有索引却白花一次 embedding 调用"，
// 也避免依赖尚未从响应学到的维度（见 activeEmbeddingRules）。
func (s *Store) VectorSearch(ctx context.Context, owner uint, library uint64, embedder Embedder, query string, topK int) (VectorResults, error) {
	result := VectorResults{Items: []VectorHit{}, PromptTokens: -1}
	query = strings.TrimSpace(query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 2000 || strings.ContainsRune(query, 0) {
		return result, ErrInvalid
	}
	if embedder == nil {
		return result, fmt.Errorf("embedder 未配置")
	}
	if topK <= 0 {
		topK = 8
	}
	if topK > 50 {
		topK = 50
	}
	db := s.db.WithContext(ctx)
	if err := owned(db, owner, library, false); err != nil {
		return result, err
	}
	// 先按"模型"找就绪索引（不依赖已学到的维度），再嵌入查询；否则新进程首次直接
	// VectorSearch 会因 ID()="model:0" 匹配不到索引而静默降级。
	rules, err := s.activeEmbeddingRules(db, library, embedder.Model())
	if err != nil {
		return result, err
	}
	if len(rules) == 0 {
		return result, nil // indexed=false：本库没有该模型的就绪索引
	}
	result.EmbeddingRule = rules[0]
	emb, err := embedder.Embed(ctx, []string{query})
	if err != nil {
		return result, fmt.Errorf("查询 embedding 失败: %w", err)
	}
	if len(emb.Vectors) != 1 || len(emb.Vectors[0]) == 0 {
		return result, fmt.Errorf("查询 embedding 返回异常")
	}
	result.PromptTokens = emb.PromptTokens
	qv := normalizeVector(emb.Vectors[0])
	var rows []struct {
		SourceID   uint64 `gorm:"column:source_id"`
		VersionID  uint64 `gorm:"column:version_id"`
		ChunkStart int    `gorm:"column:start_offset"`
		ChunkEnd   int    `gorm:"column:end_offset"`
		Dim        int    `gorm:"column:dim"`
		Vector     []byte `gorm:"column:vector"`
	}
	scan := `SELECT s.id AS source_id, v.id AS version_id, c.start_offset, c.end_offset, e.dim, e.vector
	FROM knowledge_sources s
	JOIN knowledge_source_versions v ON v.id = s.current_version_id AND v.source_id = s.id
	JOIN knowledge_chunks c ON c.version_id = v.id
	JOIN knowledge_chunk_embeddings e ON e.chunk_id = c.id AND e.version_id = v.id AND e.embedding_rule IN ?
	WHERE s.library_id = ?
	ORDER BY s.id, c.ordinal
	LIMIT ?`
	if err := db.Raw(scan, rules, library, maxVectorScan+1).Scan(&rows).Error; err != nil {
		return result, err
	}
	if len(rows) > maxVectorScan {
		return result, fmt.Errorf("向量候选超过扫描上限 %d，需在 K-3 评测后决定是否引入 ANN", maxVectorScan)
	}
	result.Indexed = true
	result.Chunks = len(rows)
	hits := make([]VectorHit, 0, len(rows))
	matched := 0
	for _, row := range rows {
		if row.Dim != len(qv) {
			continue // 维度不符：同模型改了维度，属于陈旧行，跳过
		}
		v, err := decodeVector(row.Vector, row.Dim)
		if err != nil {
			continue
		}
		matched++
		hits = append(hits, VectorHit{SourceID: row.SourceID, VersionID: row.VersionID, ChunkStart: row.ChunkStart, ChunkEnd: row.ChunkEnd, Score: dot(qv, v)})
	}
	// 有索引却一条维度都对不上：说明查询模型维度与索引不符，明确报错要求重建，
	// 不能返回空腹结果让调用方误以为"没有相关内容"。
	if len(rows) > 0 && matched == 0 {
		return result, fmt.Errorf("查询向量维度 %d 与索引全部不符，需按当前模型重建向量索引", len(qv))
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].SourceID != hits[j].SourceID {
			return hits[i].SourceID < hits[j].SourceID
		}
		return hits[i].ChunkStart < hits[j].ChunkStart
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}
	result.Items = hits
	return result, nil
}
