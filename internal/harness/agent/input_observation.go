package agent

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/tokenmeter"
)

// InputObservation describes the logical SDK input, not provider billing or wire
// serialization. Buckets are disjoint heuristic token estimates, without content.
// Tools=-1 means the tool-schema estimate is unavailable; Total then excludes it.
type InputObservation struct {
	Messages, System, User, Assistant, ToolResults int
	Reasoning, Structure, Tools, Total             int
}

// ModelUsage is reported once for a fully read model step with SDK usage.
// Absent usage is unknown. Cache-field presence is lost by the SDK, so it is not
// reported as zero. Retries inside the model wrapper are outside this observation.
type ModelUsage struct{ Prompt, Completion int }

func observeInput(ctx context.Context, messages []*schema.Message, tools int) {
	reportProgress(ctx, Progress{Kind: ProgressModel, Input: estimateInput(messages, tools)})
}

func estimateInput(messages []*schema.Message, tools int) *InputObservation {
	o := &InputObservation{Tools: tools}
	for _, m := range messages {
		if m == nil {
			continue
		}
		o.Messages++
		n := tokenmeter.Estimate(m.Content)
		switch m.Role {
		case schema.System:
			o.System += n
		case schema.User:
			o.User += n
		case schema.Assistant:
			o.Assistant += n
		case schema.Tool:
			o.ToolResults += n
		default:
			o.Structure += n
		}
		o.Reasoning += tokenmeter.Estimate(m.ReasoningContent)
		// EstimateMessages includes content and call arguments but not reasoning
		// or call IDs. Keep those fields in distinct, additive buckets.
		o.Structure += tokenmeter.EstimateMessages([]*schema.Message{m}) - n
		for _, call := range m.ToolCalls {
			o.Structure += tokenmeter.Estimate(call.ID)
		}
	}
	o.Total = o.System + o.User + o.Assistant + o.ToolResults + o.Reasoning + o.Structure
	if tools >= 0 {
		o.Total += tools
	}
	return o
}

func estimateToolSchemas(infos []*schema.ToolInfo) int {
	if len(infos) == 0 {
		return 0
	}
	definitions := make([]any, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			return -1
		}
		params, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return -1
		}
		definitions = append(definitions, map[string]any{"type": "function", "function": map[string]any{
			"name": info.Name, "description": info.Desc, "parameters": params,
		}})
	}
	data, err := json.Marshal(definitions)
	if err != nil {
		return -1
	}
	return tokenmeter.Estimate(string(data))
}
