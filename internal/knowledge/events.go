package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go_im_gateway/internal/harness/session"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TurnEvent is execution evidence, not the conversation's model history.
// Ownership is inherited through turn -> conversation -> library.
type TurnEvent struct {
	ID        uint64    `json:"id"`
	TurnID    uint64    `json:"turn_id"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

func (TurnEvent) TableName() string { return "knowledge_turn_events" }

// AppendTurnEvents accepts only a server-issued turn ID. The row lock serializes
// appends against FinishTurn; expired or completed writers cannot add evidence.
func (s *Store) AppendTurnEvents(ctx context.Context, id uint64, events []session.MemoryDTO) error {
	if len(events) == 0 {
		return ErrInvalid
	}
	rows := make([]TurnEvent, 0, len(events))
	for _, event := range events {
		event.V = session.LogFormatVersion
		event.Time = time.Now().UTC()
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		rows = append(rows, TurnEvent{TurnID: id, Payload: string(payload)})
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var turn ChatTurn
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = 'running' AND deadline_at > UTC_TIMESTAMP(3)", id).First(&turn).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		return tx.Create(&rows).Error
	})
}
