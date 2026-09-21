package knowledge

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ChunkRuleVersion = "rune-window-800-overlap-100-v1"

const chunkWindow = 800
const chunkOverlap = 100

type Chunk struct {
	ID        uint64 `json:"id"`
	VersionID uint64 `json:"version_id"`
	Ordinal   int    `json:"ordinal"`
	Start     int    `json:"start" gorm:"column:start_offset"`
	End       int    `json:"end" gorm:"column:end_offset"`
	Content   string `json:"content"`
}

func (Chunk) TableName() string { return "knowledge_chunks" }

type ChunkIndex struct {
	VersionID   uint64    `json:"version_id" gorm:"primaryKey;autoIncrement:false"`
	RuleVersion string    `json:"rule_version"`
	ChunkCount  int       `json:"chunk_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (ChunkIndex) TableName() string { return "knowledge_chunk_indexes" }

type IndexStatus struct {
	State string     `json:"state"`
	Index ChunkIndex `json:"index"`
}

// SplitDocument preserves the original text, including BOM, CRLF and markup.
// Offsets use Unicode code points, matching the existing source reader.
func SplitDocument(body string) ([]Chunk, error) {
	if !utf8.ValidString(body) || len(body) > MaxBodyBytes {
		return nil, ErrInvalid
	}
	runes := []rune(body)
	chunks := []Chunk{}
	for start := 0; start < len(runes); start += chunkWindow - chunkOverlap {
		end := min(start+chunkWindow, len(runes))
		chunks = append(chunks, Chunk{Ordinal: len(chunks) + 1, Start: start, End: end, Content: string(runes[start:end])})
		if end == len(runes) {
			break
		}
	}
	return chunks, nil
}

// Must run inside the source transaction. Readers see the old or complete new
// index; neither partial replacement nor a ready marker without chunks is visible.
func buildChunkIndex(tx *gorm.DB, version Version) (ChunkIndex, error) {
	chunks, err := SplitDocument(version.Body)
	if err != nil {
		return ChunkIndex{}, err
	}
	if err := tx.Where("version_id = ?", version.ID).Delete(&Chunk{}).Error; err != nil {
		return ChunkIndex{}, err
	}
	for i := range chunks {
		chunks[i].VersionID = version.ID
	}
	if len(chunks) > 0 {
		if err := tx.CreateInBatches(&chunks, 100).Error; err != nil {
			return ChunkIndex{}, err
		}
	}
	index := ChunkIndex{VersionID: version.ID, RuleVersion: ChunkRuleVersion, ChunkCount: len(chunks), UpdatedAt: time.Now().UTC()}
	err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "version_id"}}, DoUpdates: clause.AssignmentColumns([]string{"rule_version", "chunk_count", "updated_at"})}).Create(&index).Error
	return index, err
}

// RebuildIndex serializes against imports/rebuilds and pins the current version.
func (s *Store) RebuildIndex(ctx context.Context, owner uint, library, source uint64) (ChunkIndex, error) {
	var result ChunkIndex
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owned(tx, owner, library, true); err != nil {
			return err
		}
		var src Source
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND library_id = ?", source, library).First(&src).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var version Version
		if err := tx.Where("id = ? AND source_id = ?", src.CurrentVersionID, src.ID).First(&version).Error; err != nil {
			return err
		}
		var err error
		result, err = buildChunkIndex(tx, version)
		return err
	})
	return result, err
}

func (s *Store) SourceIndex(ctx context.Context, owner uint, library, source uint64) (IndexStatus, error) {
	doc, err := s.Read(ctx, owner, library, source)
	if err != nil {
		return IndexStatus{}, err
	}
	result := IndexStatus{State: "missing", Index: ChunkIndex{VersionID: doc.Version.ID}}
	err = s.db.WithContext(ctx).Where("version_id = ?", doc.Version.ID).First(&result.Index).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		return IndexStatus{}, err
	}
	result.State = "ready"
	if result.Index.RuleVersion != ChunkRuleVersion {
		result.State = "outdated"
	}
	return result, nil
}

// Chunks requires an explicit immutable version, so pagination cannot drift
// when a new source version is published between requests.
func (s *Store) Chunks(ctx context.Context, owner uint, library, source, version uint64, after int) ([]Chunk, error) {
	if after < 0 {
		return nil, ErrInvalid
	}
	db := s.db.WithContext(ctx)
	if err := owned(db, owner, library, false); err != nil {
		return nil, err
	}
	var count int64
	if err := db.Table("knowledge_sources AS s").Joins("JOIN knowledge_source_versions AS v ON v.source_id = s.id").Where("s.id = ? AND s.library_id = ? AND v.id = ?", source, library, version).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, ErrNotFound
	}
	chunks := []Chunk{}
	err := db.Where("version_id = ? AND ordinal > ?", version, after).Order("ordinal ASC").Limit(50).Find(&chunks).Error
	return chunks, err
}
