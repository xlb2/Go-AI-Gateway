//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/knowledge"
	"gorm.io/gorm"
)

func TestKnowledgeExecutionEventsHTTP(t *testing.T) {
	for _, fail := range []string{"", session.EventStepStart, session.EventStepEnd} {
		t.Run("fail="+fail, func(t *testing.T) {
			db := database(t)
			s := knowledge.NewStore(db)
			ctx := context.Background()
			lib, err := s.CreateLibrary(ctx, 11, "events")
			if err != nil {
				t.Fatal(err)
			}
			conv, err := s.CreateConversation(ctx, 11, lib.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fail != "" {
				err = db.Callback().Create().Before("gorm:create").Register("test:event_failure", func(tx *gorm.DB) {
					rows, ok := tx.Statement.Dest.(*[]knowledge.TurnEvent)
					if !ok {
						return
					}
					for _, row := range *rows {
						var event session.MemoryDTO
						if json.Unmarshal([]byte(row.Payload), &event) == nil && event.Type == fail {
							tx.AddError(errors.New("injected event failure"))
						}
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			m := &knowledgeChatModel{}
			r := gin.New()
			g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", uint(11)) })
			knowledge.NewChat(s, func(context.Context) (model.ChatModel, error) { return m, nil }).Register(g)
			body, _ := json.Marshal(knowledge.TurnInput{RequestID: uuid.NewString(), Content: "question"})
			out := httptest.NewRecorder()
			r.ServeHTTP(out, httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/conversations/%d/turns", lib.ID, conv.ID), strings.NewReader(string(body))))
			detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
			if err != nil || len(detail.Turns) != 1 {
				t.Fatalf("turn: %+v %v", detail, err)
			}
			var rows []knowledge.TurnEvent
			if err := db.Where("turn_id = ?", detail.Turns[0].ID).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if fail == "" {
				if detail.Turns[0].Status != "completed" || len(rows) != 2 || !strings.Contains(out.Body.String(), `"type":"done"`) {
					t.Fatalf("success: %+v %s", rows, out.Body.String())
				}
				for i, kind := range []string{session.EventStepStart, session.EventStepEnd} {
					var event session.MemoryDTO
					if err := json.Unmarshal([]byte(rows[i].Payload), &event); err != nil || event.Type != kind || event.V != session.LogFormatVersion {
						t.Fatalf("event: %+v %v", event, err)
					}
				}
			} else {
				if detail.Turns[0].Status != "failed" || strings.Contains(out.Body.String(), `"type":"done"`) || !strings.Contains(out.Body.String(), `"type":"error"`) {
					t.Fatalf("false success: %+v %s", detail, out.Body.String())
				}
				want := 0
				if fail == session.EventStepEnd {
					want = 1
				}
				if len(rows) != want {
					t.Fatalf("events=%d want=%d", len(rows), want)
				}
			}
		})
	}
}

func TestKnowledgeExecutionEventsFencing(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "events")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint64
	for i := 0; i < 2; i++ {
		conv, err := s.CreateConversation(ctx, 11, lib.ID)
		if err != nil {
			t.Fatal(err)
		}
		turn, _, _, err := s.BeginTurn(ctx, 11, lib.ID, conv.ID, knowledge.TurnInput{RequestID: uuid.NewString(), Content: "question"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, turn.ID)
	}
	events := []session.MemoryDTO{{Type: session.EventStepStart, Role: "system"}}
	if err := s.AppendTurnEvents(ctx, ids[0], events); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&knowledge.TurnEvent{}).Where("turn_id = ?", ids[1]).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("cross-turn event: %d %v", count, err)
	}
	if err := s.FinishTurn(ctx, ids[0], "canceled", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTurnEvents(ctx, ids[0], events); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatalf("finished writer: %v", err)
	}
	if err := db.Model(&knowledge.ChatTurn{}).Where("id = ?", ids[1]).Update("deadline_at", gorm.Expr("UTC_TIMESTAMP(3) - INTERVAL 1 SECOND")).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTurnEvents(ctx, ids[1], events); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatalf("expired writer: %v", err)
	}
}
