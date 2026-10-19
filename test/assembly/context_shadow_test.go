package assembly_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
)

func TestContextShadowWholeTurns(t *testing.T) {
	input := []*schema.Message{
		schema.SystemMessage("system"), schema.UserMessage(strings.Repeat("old", 1000)), schema.AssistantMessage("answer", nil),
		schema.UserMessage("current"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "read", Arguments: `{}`}}}},
		{Role: schema.Tool, ToolCallID: "call", Content: "result"},
	}
	before := input[1].Content
	result := agent.EvaluateContextShadow(input, 100, 500)
	if result.Status != "within_budget" || result.Candidate > 500 || !reflect.DeepEqual(result.Kept, []int{0, 3, 4, 5}) || !result.NeedsConstraintReview {
		t.Fatalf("shadow=%+v", result)
	}
	if input[1].Content != before || len(input) != 6 {
		t.Fatal("shadow mutated input")
	}
	all := agent.EvaluateContextShadow(input, 100, 100000)
	if all.Dropped != 0 || all.Candidate != all.Original {
		t.Fatalf("all=%+v", all)
	}
}

func TestContextShadowMandatoryAndUnknown(t *testing.T) {
	input := []*schema.Message{schema.SystemMessage("rules"), schema.UserMessage(strings.Repeat("当前", 1000))}
	r := agent.EvaluateContextShadow(input, 100, 10)
	if r.Status != "mandatory_over_budget" || r.Dropped != 0 || r.Candidate <= r.Budget {
		t.Fatalf("mandatory=%+v", r)
	}
	r = agent.EvaluateContextShadow(input, -1, 500)
	if r.Status != "unknown_tool_schema" {
		t.Fatalf("unknown=%+v", r)
	}
	r = agent.EvaluateContextShadow([]*schema.Message{schema.SystemMessage(strings.Repeat("task", 1000))}, 0, 10)
	if r.Status != "mandatory_over_budget" || r.Dropped != 0 {
		t.Fatalf("no user=%+v", r)
	}
}

func TestContextShadowSelectsRetrievedWholeTurnBeforeRecentHistory(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("older lookup"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "old-call", Function: schema.FunctionCall{Name: "read_history_source", Arguments: `{}`}}}},
		schema.ToolMessage("retrieved evidence", "old-call"),
		schema.AssistantMessage("older reply", nil),
		schema.UserMessage("middle unrelated turn"),
		schema.AssistantMessage(strings.Repeat("unrelated ", 2000), nil),
		schema.UserMessage("current request"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "current-call", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}}}},
		schema.ToolMessage("current result", "current-call"),
	}

	plain := agent.EvaluateContextShadow(messages, 0, 500)
	if !reflect.DeepEqual(plain.Kept, []int{0, 7, 8, 9}) {
		t.Fatalf("plain candidate=%+v", plain)
	}
	withEvidence := agent.EvaluateContextShadowWithEvidence(messages, 0, 500, nil, []int{3})
	want := []int{0, 1, 2, 3, 4, 7, 8, 9}
	if withEvidence.Status != "within_budget" || withEvidence.RequiredSources != 0 || withEvidence.EvidenceTurns != 1 || withEvidence.OmittedEvidenceTurns != 0 || !reflect.DeepEqual(withEvidence.Kept, want) {
		t.Fatalf("retrieved evidence candidate=%+v", withEvidence)
	}

	tooLarge := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("很长的旧证据", 200)),
		schema.AssistantMessage("old answer", nil),
		schema.UserMessage("current request"),
		schema.AssistantMessage("current answer", nil),
	}
	overBudget := agent.EvaluateContextShadowWithEvidence(tooLarge, 0, 100, nil, []int{1})
	if overBudget.Status != "within_budget" || overBudget.EvidenceTurns != 1 || overBudget.OmittedEvidenceTurns != 1 || !overBudget.NeedsConstraintReview || !reflect.DeepEqual(overBudget.Kept, []int{0, 3, 4}) {
		t.Fatalf("over-budget evidence=%+v", overBudget)
	}
}
