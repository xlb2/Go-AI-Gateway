package e2e

import (
	"strings"
	"sync"
	"testing"

	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
)

func TestContextShadowApprovalEvidence(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "failure"} {
		t.Run(decision, func(t *testing.T) {
			f := newToolApprovalFixture(t, 9184)
			f.failure = decision == "failure"
			sources, err := (session.RedisStore{}).GetHistorySources(f.e.ctx(), f.e.uid, 0)
			if err != nil {
				t.Fatal(err)
			}
			var requiredID string
			for _, source := range sources {
				if source.EventType == session.EventUserMessage && source.Message != nil && source.Message.Content == "request approval" && !source.Interrupted {
					requiredID = source.ID
					break
				}
			}
			if requiredID == "" {
				t.Fatalf("approval user source missing: %+v", sources)
			}
			var mu sync.Mutex
			var shadows []agent.ContextShadow
			ctx := agent.WithRequiredHistorySources(agent.WithContextShadow(f.e.ctx(), 1), requiredID)
			ctx = agent.WithProgress(ctx, func(p agent.Progress) {
				if p.Shadow != nil {
					mu.Lock()
					shadows = append(shadows, *p.Shadow)
					mu.Unlock()
				}
			})
			command := "auth:approve " + f.pending.ConfirmCode()
			if decision == "reject" {
				command = "auth:reject"
			}
			handled, reply := f.e.h.HandleApprovalCommand(ctx, f.e.uid, command)
			if !handled {
				t.Fatal("approval not handled")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(shadows) != 1 || shadows[0].Status != "required_over_budget" || shadows[0].RequiredSources != 1 || shadows[0].UnresolvedRequirements != 0 {
				t.Fatalf("shadows=%+v reply=%s", shadows, reply)
			}
			requests := f.e.fake.Requests()
			if len(requests) != 3 {
				t.Fatalf("unexpected calls %d", len(requests))
			}
			last := requests[len(requests)-1]
			if shadows[0].SourceStatus != "matched" {
				t.Fatalf("sources: %+v", shadows[0])
			}
			foundRequired := false
			requiredIndex := -1
			for _, source := range shadows[0].Sources {
				if source.ID == requiredID {
					foundRequired = source.Required && source.Kept
					requiredIndex = source.Index
				}
			}
			if !foundRequired || requiredIndex < 0 || requiredIndex >= len(last.Messages) {
				t.Fatalf("approval user source not required in candidate: id=%s shadow=%+v", requiredID, shadows[0])
			}
			mapped := map[int]bool{}
			for _, source := range shadows[0].Sources {
				page, err := (session.RedisStore{}).ReadSource(ctx, f.e.uid, source.ID, 0, 2000)
				if err != nil || page.Text != last.Messages[source.Index].Content {
					t.Fatalf("source=%+v err=%v", source, err)
				}
				mapped[source.Index] = source.Kept
			}
			want := "approved-result"
			if decision == "reject" {
				want = "审批已拒绝"
			}
			if decision == "failure" {
				want = "fixture tool failure"
			}
			kept := map[int]bool{}
			for _, i := range shadows[0].Kept {
				kept[i] = true
			}
			for i := requiredIndex; i < len(last.Messages); i++ {
				if !kept[i] {
					t.Fatalf("required approval suffix omitted at index %d: kept=%v", i, shadows[0].Kept)
				}
			}
			foundOld, foundResult := false, false
			for i, m := range last.Messages {
				if m.Role == "user" && m.Content == "request approval" {
					foundOld = true
				}
				if m.Role == "tool" && strings.Contains(m.Content, want) {
					foundResult = true
					if !kept[i] || i == 0 || !kept[i-1] {
						t.Fatal("approval tool pair omitted from candidate")
					}
					if !mapped[i] || !mapped[i-1] {
						t.Fatal("approval evidence source missing")
					}
				}
			}
			if !foundOld || !foundResult {
				t.Fatalf("actual input lost history/evidence: %+v", last.Messages)
			}
			wantCalls := 1
			if decision == "reject" {
				wantCalls = 0
			}
			if len(f.calls) != wantCalls {
				t.Fatalf("executions=%d", len(f.calls))
			}
		})
	}
}
