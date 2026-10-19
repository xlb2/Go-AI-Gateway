package e2e

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

func TestContextShadowRetainsRequiredUserSource(t *testing.T) {
	for _, mode := range []string{"present", "missing", "assistant"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, 9190)
			e.h = e.newHarness(&recordingExecutor{})
			store := session.RedisStore{}
			if err := store.SaveMessage(e.ctx(), e.uid, schema.UserMessage("confirmed constraint XZ-17")); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveReply(e.ctx(), e.uid, "assistant suggestion YQ-88", false); err != nil {
				t.Fatal(err)
			}
			sources, err := store.GetHistorySources(e.ctx(), e.uid, 0)
			if err != nil || len(sources) != 2 {
				t.Fatalf("sources=%+v err=%v", sources, err)
			}
			id := sources[0].ID
			if mode == "missing" {
				id = "history:v1:9190:00000000000000000000000000000000:99"
			} else if mode == "assistant" {
				id = sources[1].ID
			}
			e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "ok"}})
			var shadow *agent.ContextShadow
			ctx := agent.WithRequiredHistorySources(agent.WithContextShadow(e.ctx(), 1), id)
			ctx = agent.WithProgress(ctx, func(p agent.Progress) {
				if p.Shadow != nil {
					shadow = p.Shadow
				}
			})
			if _, err := e.h.RunAgentTurn(ctx, e.uid, "current question", nil); err != nil {
				t.Fatal(err)
			}
			if shadow == nil {
				t.Fatal("shadow missing")
			}
			if mode != "present" {
				if shadow.UnresolvedRequirements != 1 {
					t.Fatalf("missing constraint: %+v", shadow)
				}
				for _, source := range shadow.Sources {
					if source.ID == id && source.Required {
						t.Fatalf("unknown source marked required: %+v", source)
					}
				}
				return
			}
			if shadow.RequiredSources != 1 || shadow.UnresolvedRequirements != 0 || shadow.Status != "required_over_budget" {
				t.Fatalf("constraint not enforced in candidate: %+v", shadow)
			}
			found, kept := false, false
			for _, source := range shadow.Sources {
				if source.ID == id {
					found, kept = true, source.Required && source.Kept
				}
			}
			if !found || !kept {
				t.Fatalf("required source absent: %+v", shadow)
			}
			actual := false
			for _, message := range e.fake.Requests()[0].Messages {
				if strings.Contains(message.Content, "confirmed constraint XZ-17") {
					actual = true
				}
			}
			if !actual {
				t.Fatal("actual SDK input lost the user source")
			}
		})
	}
}
