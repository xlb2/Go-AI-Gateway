package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

func TestCanceledTurnPersistsPartialReply(t *testing.T) {
	e := newEnv(t, 9310)
	e.h = e.newHarness(&recordingExecutor{})
	expected := strings.Repeat("partial-response-", 80)
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: expected}})

	ctx, cancel := context.WithCancel(e.ctx())
	defer cancel()
	reply, runErr := e.h.RunAgentTurn(ctx, e.uid, "cancel after first response chunk", func(chunk string) {
		if chunk != "" {
			cancel()
		}
	})
	if reply == "" || reply == expected || !strings.HasPrefix(expected, reply) {
		t.Fatalf("reply length=%d, want a non-empty partial response shorter than %d", len(reply), len(expected))
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("runErr=%v, want context.Canceled", runErr)
	}

	var saved bool
	for _, dto := range e.log() {
		if dto.Type == session.EventAssistantMessage {
			if dto.Content != reply || !dto.Interrupted {
				t.Fatalf("saved partial reply=%+v, want exact reply marked interrupted", dto)
			}
			saved = true
		}
		if dto.Type == session.EventTurnEnd && strings.Contains(dto.Content, string(session.TurnCompleted)) {
			t.Fatal("canceled turn was recorded as completed")
		}
	}
	if !saved {
		t.Fatal("canceled turn did not persist its partial assistant reply")
	}

	repaired, err := e.h.Sessions.Repair(e.ctx(), e.uid)
	if err != nil || repaired != 1 {
		t.Fatalf("Repair()=(%d, %v), want one interrupted turn repaired", repaired, err)
	}
}
