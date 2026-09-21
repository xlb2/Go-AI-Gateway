package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrConflict = errors.New("会话状态已变化，请刷新后重试")

type Conversation struct {
	ID        uint64    `json:"id" gorm:"primaryKey"`
	LibraryID uint64    `json:"library_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

func (Conversation) TableName() string { return "knowledge_conversations" }

type ChatTurn struct {
	Evidence       []ReadingEvidence `json:"evidence" gorm:"-"`
	ID             uint64            `json:"id" gorm:"primaryKey"`
	ConversationID uint64            `json:"conversation_id"`
	RequestID      string            `json:"request_id"`
	InputHash      string            `json:"-"`
	Question       string            `json:"question"`
	Prompt         string            `json:"-"`
	SourcesJSON    string            `json:"sources_json"`
	Answer         string            `json:"answer"`
	Status         string            `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
	DeadlineAt     time.Time         `json:"deadline_at"`
}

func (ChatTurn) TableName() string { return "knowledge_chat_turns" }

type ConversationDetail struct {
	Conversation Conversation `json:"conversation"`
	Turns        []ChatTurn   `json:"turns"`
}
type TurnInput struct {
	RequestID      string   `json:"request_id"`
	ExpectedTurnID uint64   `json:"expected_turn_id"`
	Content        string   `json:"content"`
	SourceIDs      []uint64 `json:"source_ids"`
}
type SourceReference struct {
	ID        uint64 `json:"id"`
	VersionID uint64 `json:"version_id"`
	Version   int    `json:"version"`
	Title     string `json:"title"`
}

func conversationOwned(db *gorm.DB, owner uint, library, id uint64, lock bool) (Conversation, error) {
	if err := owned(db, owner, library, false); err != nil {
		return Conversation{}, err
	}
	q := db.Where("id = ? AND library_id = ?", id, library)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var conv Conversation
	err := q.First(&conv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return conv, err
}
func (s *Store) CreateConversation(ctx context.Context, owner uint, library uint64) (Conversation, error) {
	result := Conversation{LibraryID: library, Title: "新对话"}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owned(tx, owner, library, false); err != nil {
			return err
		}
		return tx.Create(&result).Error
	})
	return result, err
}
func (s *Store) Conversations(ctx context.Context, owner uint, library, before uint64) ([]Conversation, error) {
	db := s.db.WithContext(ctx)
	if err := owned(db, owner, library, false); err != nil {
		return nil, err
	}
	items := []Conversation{}
	q := db.Where("library_id = ?", library)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	err := q.Order("id DESC").Limit(50).Find(&items).Error
	return items, err
}
func expireTurns(db *gorm.DB, conversation uint64) error {
	return db.Model(&ChatTurn{}).Where("conversation_id = ? AND status = 'running' AND deadline_at <= UTC_TIMESTAMP(3)", conversation).Update("status", "interrupted").Error
}
func (s *Store) Conversation(ctx context.Context, owner uint, library, id uint64) (ConversationDetail, error) {
	result := ConversationDetail{Turns: []ChatTurn{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		conv, err := conversationOwned(tx, owner, library, id, true)
		if err != nil {
			return err
		}
		result.Conversation = conv
		if err := expireTurns(tx, id); err != nil {
			return err
		}
		return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ?", id).Order("id ASC").Find(&result.Turns).Error
	})
	if err == nil {
		err = s.attachEvidence(ctx, result.Turns)
	}
	return result, err
}

// The conversation row serializes admission across processes. A stable request ID
// deduplicates retries; expected_turn_id rejects a stale tab even after completion.
func (s *Store) BeginTurn(ctx context.Context, owner uint, library, id uint64, input TurnInput) (ChatTurn, []*schema.Message, bool, error) {
	var result ChatTurn
	var messages []*schema.Message
	duplicate := false
	parsed, err := uuid.Parse(input.RequestID)
	if err != nil || parsed.String() != input.RequestID || parsed == uuid.Nil || !utf8.ValidString(input.Content) || strings.TrimSpace(input.Content) == "" || utf8.RuneCountInString(input.Content) > 16000 || len(input.SourceIDs) > 4 {
		return result, nil, false, ErrInvalid
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return result, nil, false, err
	}
	hash := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(hash[:])
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := conversationOwned(tx, owner, library, id, true)
		if err != nil {
			return err
		}
		if err := expireTurns(tx, id); err != nil {
			return err
		}
		// Current reads must follow the row lock; MySQL's repeatable-read snapshot
		// may have been established by the earlier ownership lookup.
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ? AND request_id = ?", id, input.RequestID).First(&result).Error
		if err == nil {
			if result.InputHash != fingerprint {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		turns := []ChatTurn{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ?", id).Order("id ASC").Find(&turns).Error; err != nil {
			return err
		}
		var last uint64
		history := []ChatMessage{}
		prompts := []string{}
		for _, turn := range turns {
			last = turn.ID
			if turn.Status == "running" {
				return ErrConflict
			}
			if turn.Status == "completed" {
				history = append(history, ChatMessage{Role: "user", Content: turn.Question}, ChatMessage{Role: "assistant", Content: turn.Answer})
				prompts = append(prompts, turn.Prompt)
			}
		}
		if input.ExpectedTurnID != last {
			return ErrConflict
		}
		if len(turns) >= 100 {
			return fmt.Errorf("%w: 会话记录已达上限，请新建对话", ErrInvalid)
		}
		docs := []Document{}
		refs := []SourceReference{}
		seen := map[uint64]bool{}
		for _, source := range input.SourceIDs {
			if seen[source] {
				return ErrInvalid
			}
			seen[source] = true
			doc, err := NewStore(tx).Read(ctx, owner, library, source)
			if err != nil {
				return err
			}
			docs = append(docs, doc)
			refs = append(refs, SourceReference{ID: doc.Source.ID, Title: doc.Source.Title, VersionID: doc.Version.ID, Version: doc.Version.Version})
		}
		history = append(history, ChatMessage{Role: "user", Content: input.Content})
		messages, err = BuildChatMessages(ChatInput{Messages: history, SourceIDs: input.SourceIDs}, docs)
		if err != nil {
			return err
		}
		for i, prompt := range prompts {
			messages[1+i*2].Content = prompt
		}
		size := 0
		for _, message := range messages {
			size += len(message.Content)
		}
		if size > 256*1024 {
			return fmt.Errorf("%w: 附件历史过长，请新建对话", ErrInvalid)
		}
		references, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		var clock struct{ Now time.Time }
		if err := tx.Raw("SELECT UTC_TIMESTAMP(3) AS now").Scan(&clock).Error; err != nil {
			return err
		}
		result = ChatTurn{ConversationID: id, RequestID: input.RequestID, InputHash: fingerprint, Question: input.Content, Prompt: messages[len(messages)-1].Content, SourcesJSON: string(references), Status: "running", DeadlineAt: clock.Now.Add(3 * time.Minute)}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		if len(turns) == 0 {
			title := []rune(strings.TrimSpace(input.Content))
			if len(title) > 60 {
				title = title[:60]
			}
			return tx.Model(&Conversation{}).Where("id = ?", id).Update("title", string(title)).Error
		}
		return nil
	})
	return result, messages, duplicate, err
}

// Call only with a server-issued turn ID, never a client-selected update target.
// The deadline is also a fencing boundary: an expired writer cannot report success.
func (s *Store) FinishTurn(ctx context.Context, id uint64, status, answer string) error {
	if status != "completed" && status != "failed" && status != "canceled" {
		return ErrInvalid
	}
	if len(answer) > 64*1024 || !utf8.ValidString(answer) {
		return ErrInvalid
	}
	result := s.db.WithContext(ctx).Model(&ChatTurn{}).Where("id = ? AND status = 'running' AND deadline_at > UTC_TIMESTAMP(3)", id).Updates(map[string]any{"status": status, "answer": answer})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}
