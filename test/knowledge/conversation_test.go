//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"errors"
	"go_im_gateway/internal/knowledge"
	"strings"
	"sync"
	"testing"
)

func TestKnowledgeConversationPersistenceAndOwnership(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "library")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := knowledge.TurnInput{RequestID: "00000000-0000-4000-8000-000000000001", Content: "remember marker"}
	source, _, err := s.Import(ctx, 11, lib.ID, "reference.md", "md", "original-version-fact")
	if err != nil {
		t.Fatal(err)
	}
	input.SourceIDs = []uint64{source.ID}
	turn, _, duplicate, err := s.BeginTurn(ctx, 11, lib.ID, conv.ID, input)
	if err != nil || duplicate {
		t.Fatalf("begin: %v", err)
	}
	if err = s.FinishTurn(ctx, turn.ID, "completed", "remembered"); err != nil {
		t.Fatal(err)
	}
	rebuilt := knowledge.NewStore(db)
	detail, err := rebuilt.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 || detail.Turns[0].Answer != "remembered" {
		t.Fatalf("read: %+v %v", detail, err)
	}
	if !strings.Contains(detail.Turns[0].SourcesJSON, "reference.md") {
		t.Fatal("attachment reference lost")
	}
	replacement := knowledge.Version{SourceID: source.ID, Version: 2, Body: "replacement-fact"}
	if err = db.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&knowledge.Source{}).Where("id = ?", source.ID).Update("current_version_id", replacement.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = rebuilt.Conversation(ctx, 22, lib.ID, conv.ID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatal("cross-owner read")
	}
	other, err := s.CreateLibrary(ctx, 11, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Conversation(ctx, 11, other.ID, conv.ID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatal("cross-library read")
	}
	input.RequestID = "00000000-0000-4000-8000-000000000002"
	input.ExpectedTurnID = turn.ID
	input.Content = "follow up"
	input.SourceIDs = nil
	next, messages, _, err := s.BeginTurn(ctx, 11, lib.ID, conv.ID, input)
	if err != nil || len(messages) != 4 || !strings.Contains(messages[1].Content, "original-version-fact") || strings.Contains(messages[1].Content, "replacement-fact") {
		t.Fatalf("history: %v", err)
	}
	if err = s.FinishTurn(ctx, next.ID, "canceled", "partial"); err != nil {
		t.Fatal(err)
	}
	input.RequestID = "00000000-0000-4000-8000-000000000003"
	input.ExpectedTurnID = next.ID
	_, messages, _, err = s.BeginTurn(ctx, 11, lib.ID, conv.ID, input)
	if err != nil || len(messages) != 4 {
		t.Fatalf("canceled history included: %v", err)
	}
}

func TestKnowledgeConversationConcurrentAndDuplicate(t *testing.T) {
	s := knowledge.NewStore(database(t))
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "library")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := knowledge.TurnInput{RequestID: "00000000-0000-4000-8000-000000000004", Content: "hello"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, duplicates := 0, 0
	var id uint64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			turn, _, dup, err := s.BeginTurn(ctx, 11, lib.ID, conv.ID, in)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			id = turn.ID
			if dup {
				duplicates++
			} else {
				created++
			}
		}()
	}
	wg.Wait()
	if created != 1 || duplicates != 3 {
		t.Fatalf("created=%d duplicates=%d", created, duplicates)
	}
	in.Content = "changed"
	if _, _, _, err = s.BeginTurn(ctx, 11, lib.ID, conv.ID, in); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatal("changed duplicate accepted")
	}
	in.RequestID = "00000000-0000-4000-8000-000000000005"
	if _, _, _, err = s.BeginTurn(ctx, 11, lib.ID, conv.ID, in); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatal("concurrent request accepted")
	}
	if err = s.FinishTurn(ctx, id, "completed", "answer"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.BeginTurn(ctx, 11, lib.ID, conv.ID, in); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatal("stale client appended")
	}
}

func TestKnowledgeConversationExpiredAndWriteBarriers(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "library")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, 11, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := knowledge.TurnInput{RequestID: "00000000-0000-4000-8000-000000000006", Content: "question"}
	turn, _, _, err := s.BeginTurn(ctx, 11, lib.ID, conv.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE knowledge_chat_turns SET deadline_at = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE id = ?", turn.ID).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || detail.Turns[0].Status != "interrupted" {
		t.Fatalf("expiry: %v", err)
	}
	if err = s.FinishTurn(ctx, turn.ID, "completed", "late"); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatal("expired writer accepted")
	}
	in.RequestID = "00000000-0000-4000-8000-000000000007"
	in.ExpectedTurnID = turn.ID
	in.Content = strings.Repeat("x", 16001)
	if _, _, _, err = s.BeginTurn(ctx, 11, lib.ID, conv.ID, in); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatal("invalid input persisted")
	}
	detail, err = s.Conversation(ctx, 11, lib.ID, conv.ID)
	if err != nil || len(detail.Turns) != 1 {
		t.Fatal("invalid input created turn")
	}
}
