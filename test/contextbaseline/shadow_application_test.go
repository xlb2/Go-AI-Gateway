package contextbaseline

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
)

func TestShadowApplyingModelUsesCurrentCandidateAcrossRetries(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("older user"),
		schema.AssistantMessage("older reply", nil),
		schema.UserMessage("current user"),
	}
	state := &shadowApplicationState{}
	ctx := context.WithValue(context.Background(), shadowApplicationKey{}, state)
	base := &inputCaptureModel{}
	wrapped := WrapShadowModelForEvaluation(ctx, base)
	shadow := agent.ContextShadow{SourceStatus: "matched", Budget: 100, Original: 150, Candidate: 80,
		Status: "within_budget", Dropped: 2, Kept: []int{0, 3}}
	state.observe(agent.Progress{Kind: agent.ProgressModel, Input: &agent.InputObservation{Messages: 4, Total: 150, Tools: 0}, Shadow: &shadow})

	for attempt := 0; attempt < 2; attempt++ {
		reader, err := wrapped.Stream(context.Background(), messages)
		if err != nil {
			t.Fatal(err)
		}
		reader.Close()
	}
	if len(base.inputs) != 2 {
		t.Fatalf("underlying calls=%d, want 2", len(base.inputs))
	}
	for _, input := range base.inputs {
		if len(input) != 2 || input[0] != messages[0] || input[1] != messages[3] {
			t.Fatalf("retry did not receive the same whole-turn selection: %#v", input)
		}
	}

	full := agent.ContextShadow{SourceStatus: "matched", Budget: 100, Original: 150, Candidate: 90,
		Status: "within_budget", Dropped: 0, Kept: []int{0, 1, 2, 3}}
	state.observe(agent.Progress{Kind: agent.ProgressModel, Input: &agent.InputObservation{Messages: 4, Total: 150, Tools: 0}, Shadow: &full})
	reader, err := wrapped.Stream(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if len(base.inputs[2]) != len(messages) {
		t.Fatalf("latest candidate was not used: messages=%d", len(base.inputs[2]))
	}

	applications := state.snapshot()
	if len(applications) != 3 || !applications[0].Applied || !applications[1].Applied || !applications[2].Applied {
		t.Fatalf("applications=%+v", applications)
	}
	if applications[0].SentEstimateTokens != 80 || applications[0].OriginalEstimateTokens != 150 || !applications[0].EstimateComplete {
		t.Fatalf("missing per-call estimate evidence: %+v", applications[0])
	}
}

func TestShadowModelWrapperIsInertWithoutEvaluationState(t *testing.T) {
	base := &inputCaptureModel{}
	if got := WrapShadowModelForEvaluation(context.Background(), base); got != base {
		t.Fatal("evaluation wrapper installed without an explicit application state")
	}
}

type inputCaptureModel struct {
	inputs [][]*schema.Message
}

func (*inputCaptureModel) BindTools([]*schema.ToolInfo) error { return nil }

func (*inputCaptureModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *inputCaptureModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}
