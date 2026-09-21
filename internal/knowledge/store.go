package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxBodyBytes = 2 * 1024 * 1024

var ErrInvalid = errors.New("invalid knowledge input")
var ErrNotFound = errors.New("knowledge resource not found")

type Library struct {
	ID        uint64    `json:"id" gorm:"primaryKey"`
	OwnerID   uint      `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (Library) TableName() string { return "knowledge_libraries" }

type Source struct {
	ID               uint64    `json:"id" gorm:"primaryKey"`
	LibraryID        uint64    `json:"library_id"`
	Title            string    `json:"title"`
	Format           string    `json:"format"`
	SHA256           string    `json:"sha256" gorm:"column:sha256"`
	CurrentVersionID uint64    `json:"current_version_id"`
	CreatedAt        time.Time `json:"created_at"`
}

func (Source) TableName() string { return "knowledge_sources" }

type Version struct {
	ID        uint64    `json:"id" gorm:"primaryKey"`
	SourceID  uint64    `json:"source_id"`
	Version   int       `json:"version"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func (Version) TableName() string { return "knowledge_source_versions" }

type Document struct {
	Source  Source  `json:"source"`
	Version Version `json:"version"`
}

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func ValidateDocument(title, format, body string) error {
	if !utf8.ValidString(title) || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 200 || strings.ContainsRune(title, 0) {
		return fmt.Errorf("%w: title must contain 1-200 characters", ErrInvalid)
	}
	if format != "md" && format != "txt" {
		return fmt.Errorf("%w: format must be md or txt", ErrInvalid)
	}
	if len(body) > MaxBodyBytes || !utf8.ValidString(body) || strings.ContainsRune(body, 0) || strings.TrimSpace(strings.TrimPrefix(body, "\ufeff")) == "" {
		return fmt.Errorf("%w: body must be nonempty UTF-8 text, at most 2 MiB", ErrInvalid)
	}
	return nil
}

func (s *Store) CreateLibrary(ctx context.Context, owner uint, name string) (Library, error) {
	name = strings.TrimSpace(name)
	if owner == 0 || !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsRune(name, 0) {
		return Library{}, ErrInvalid
	}
	lib := Library{OwnerID: owner, Name: name}
	err := s.db.WithContext(ctx).Create(&lib).Error
	return lib, err
}

func pageSize(limit int) int {
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}

func (s *Store) Libraries(ctx context.Context, owner uint, after uint64, limit int) ([]Library, error) {
	items := []Library{}
	if owner == 0 {
		return items, ErrNotFound
	}
	err := s.db.WithContext(ctx).Where("owner_id = ? AND id > ?", owner, after).Order("id ASC").Limit(pageSize(limit)).Find(&items).Error
	return items, err
}

func owned(db *gorm.DB, owner uint, library uint64, lock bool) error {
	if owner == 0 {
		return ErrNotFound
	}
	q := db.Where("id = ? AND owner_id = ?", library, owner)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.First(&Library{}).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func (s *Store) Sources(ctx context.Context, owner uint, library, after uint64, limit int) ([]Source, error) {
	db := s.db.WithContext(ctx)
	if err := owned(db, owner, library, false); err != nil {
		return nil, err
	}
	items := []Source{}
	err := db.Where("library_id = ? AND id > ?", library, after).Order("id ASC").Limit(pageSize(limit)).Find(&items).Error
	return items, err
}

// Serialize imports in one library. The unique hash index also guards duplicates.
// The source and immutable first version are committed together.
func (s *Store) Import(ctx context.Context, owner uint, library uint64, title, format, body string) (Source, bool, error) {
	if err := ValidateDocument(title, format, body); err != nil {
		return Source{}, false, err
	}
	hash := sha256.Sum256([]byte(body))
	result := Source{LibraryID: library, Title: strings.TrimSpace(title), Format: format, SHA256: hex.EncodeToString(hash[:])}
	duplicate := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owned(tx, owner, library, true); err != nil {
			return err
		}
		err := tx.Where("library_id = ? AND sha256 = ?", library, result.SHA256).First(&result).Error
		if err == nil {
			duplicate = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		version := Version{SourceID: result.ID, Version: 1, Body: body}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		result.CurrentVersionID = version.ID
		if err := tx.Model(&result).Update("current_version_id", version.ID).Error; err != nil {
			return err
		}
		_, err = buildChunkIndex(tx, version)
		return err
	})
	return result, duplicate, err
}

func (s *Store) Read(ctx context.Context, owner uint, library, source uint64) (Document, error) {
	db := s.db.WithContext(ctx)
	if err := owned(db, owner, library, false); err != nil {
		return Document{}, err
	}
	var result Document
	err := db.Where("id = ? AND library_id = ?", source, library).First(&result.Source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	err = db.Where("id = ? AND source_id = ?", result.Source.CurrentVersionID, source).First(&result.Version).Error
	return result, err
}
