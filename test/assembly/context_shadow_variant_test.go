package assembly_test

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/test/contextbaseline"
)

func TestContextShadowVariantSelectsOnlyValidWholeHistory(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("older omitted user"),
		schema.AssistantMessage("older omitted reply", nil),
		schema.UserMessage("older selected user"),
		schema.AssistantMessage("older selected reply", nil),
		schema.UserMessage("current user"),
		schema.AssistantMessage("current reply", nil),
	}
	shadow := agent.ContextShadow{
		SourceStatus:          "matched",
		Budget:                100,
		Original:              150,
		Candidate:             90,
		Status:                "within_budget",
		Dropped:               2,
		NeedsConstraintReview: true,
		Kept:                  []int{0, 3, 4, 5, 6},
	}

	got, application := contextbaseline.SelectShadowInput(messages, len(messages), shadow)
	if !application.Applied || application.InputMessages != 7 || application.SentMessages != 5 || application.DroppedMessages != 2 {
		t.Fatalf("application=%+v", application)
	}
	want := []*schema.Message{messages[0], messages[3], messages[4], messages[5], messages[6]}
	if len(got) != len(want) {
		t.Fatalf("selected messages=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selected message %d is not the original message at the expected index", i)
		}
	}
	if messages[1].Content != "older omitted user" || messages[2].Content != "older omitted reply" {
		t.Fatal("selection mutated the source history")
	}
}

func TestContextShadowVariantFailsClosed(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("older user"),
		schema.AssistantMessage("older reply", nil),
		schema.UserMessage("current user"),
	}
	valid := agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "within_budget", Dropped: 2, Kept: []int{0, 3}}
	cases := []struct {
		name   string
		shadow agent.ContextShadow
		count  int
	}{
		{name: "unmatched sources", shadow: agent.ContextShadow{SourceStatus: "mismatch", Budget: 20, Status: "within_budget", Kept: []int{0, 3}}, count: len(messages)},
		{name: "unresolved retrieval", shadow: agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "within_budget", UnresolvedRetrievals: 1, Kept: []int{0, 3}}, count: len(messages)},
		{name: "unresolved required source", shadow: agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "within_budget", UnresolvedRequirements: 1, Kept: []int{0, 3}}, count: len(messages)},
		{name: "current turn omitted", shadow: agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "within_budget", Dropped: 3, Kept: []int{0}}, count: len(messages)},
		{name: "invalid index", shadow: agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "within_budget", Kept: []int{0, 3, 8}}, count: len(messages)},
		{name: "input count changed", shadow: valid, count: len(messages) - 1},
		{name: "split turn", shadow: agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "within_budget", Dropped: 1, Kept: []int{0, 1, 3}}, count: len(messages)},
		{name: "unknown tool schema", shadow: agent.ContextShadow{SourceStatus: "matched", Budget: 20, Status: "unknown_tool_schema", Kept: []int{}}, count: len(messages)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, application := contextbaseline.SelectShadowInput(messages, tc.count, tc.shadow)
			if application.Applied || application.SentMessages != len(messages) || application.DroppedMessages != 0 {
				t.Fatalf("unsafe selection applied: %+v", application)
			}
			if len(got) != len(messages) {
				t.Fatalf("fallback length=%d, want %d", len(got), len(messages))
			}
			for i := range messages {
				if got[i] != messages[i] {
					t.Fatalf("fallback changed message %d", i)
				}
			}
		})
	}
}
