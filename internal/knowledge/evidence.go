package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"go_im_gateway/internal/harness/session"
	"gorm.io/gorm"
)

// ReadingEvidence is a recorded tool observation, not a verified answer citation.
type ReadingEvidence struct {
	EventID    uint64 `json:"event_id"`
	TurnID     uint64 `json:"turn_id"`
	SourceID   uint64 `json:"source_id"`
	VersionID  uint64 `json:"version_id"`
	Version    int    `json:"version"`
	Title      string `json:"title"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	Content    string `json:"content,omitempty"`
}

type readingArguments struct {
	SourceID  uint64 `json:"source_id"`
	VersionID uint64 `json:"version_id"`
	Offset    int    `json:"offset"`
}

func recordedReadings(rows []TurnEvent) []ReadingEvidence {
	result := []ReadingEvidence{}
	type key struct {
		turn uint64
		call string
	}
	pending := map[key]readingArguments{}
	for _, row := range rows {
		var event session.MemoryDTO
		if json.Unmarshal([]byte(row.Payload), &event) != nil || event.V > session.LogFormatVersion {
			continue
		}
		if event.Type == session.EventToolCall {
			for _, call := range event.ToolCalls {
				k := key{row.TurnID, call.ID}
				delete(pending, k)
				var args readingArguments
				if call.Name == "read_source" && call.ID != "" && decodeToolArgs(call.Arguments, &args) == nil {
					pending[k] = args
				}
			}
			continue
		}
		if event.Type != session.EventToolResult {
			continue
		}
		k := key{row.TurnID, event.ToolCallID}
		args, ok := pending[k]
		delete(pending, k)
		if !ok || event.ToolName != "read_source" {
			continue
		}
		var reading ReadingEvidence
		if json.Unmarshal([]byte(event.Content), &reading) != nil {
			continue
		}
		length := utf8.RuneCountInString(reading.Content)
		if reading.SourceID == 0 || reading.VersionID == 0 || reading.Version <= 0 || reading.Title == "" || length == 0 || length > 4000 || reading.Offset < 0 || reading.NextOffset-reading.Offset != length || reading.SourceID != args.SourceID || reading.VersionID != args.VersionID || reading.Offset != args.Offset {
			continue
		}
		reading.EventID, reading.TurnID = row.ID, row.TurnID
		result = append(result, reading)
	}
	return result
}

func (s *Store) attachEvidence(ctx context.Context, turns []ChatTurn) error {
	if len(turns) == 0 {
		return nil
	}
	ids := make([]uint64, len(turns))
	byID := make(map[uint64]int, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
		byID[ids[i]] = i
		turns[i].Evidence = []ReadingEvidence{}
	}
	var rows []TurnEvent
	if err := s.db.WithContext(ctx).Where("turn_id IN ?", ids).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	for _, ref := range recordedReadings(rows) {
		ref.Content = ""
		i := byID[ref.TurnID]
		turns[i].Evidence = append(turns[i].Evidence, ref)
	}
	return nil
}

// Evidence returns the exact persisted excerpt, never today's current version.
func (s *Store) Evidence(ctx context.Context, owner uint, library, conversation, turn, event uint64) (ReadingEvidence, error) {
	db := s.db.WithContext(ctx)
	if _, err := conversationOwned(db, owner, library, conversation, false); err != nil {
		return ReadingEvidence{}, err
	}
	var record ChatTurn
	if err := db.Where("id = ? AND conversation_id = ?", turn, conversation).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrNotFound
		}
		return ReadingEvidence{}, err
	}
	var rows []TurnEvent
	if err := db.Where("turn_id = ? AND id <= ?", turn, event).Order("id ASC").Find(&rows).Error; err != nil {
		return ReadingEvidence{}, err
	}
	for _, reading := range recordedReadings(rows) {
		if reading.EventID == event {
			return reading, nil
		}
	}
	return ReadingEvidence{}, ErrNotFound
}
