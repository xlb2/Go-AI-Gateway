package contextbaseline

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
)

type ShadowInputApplication struct {
	Applied                 bool   `json:"applied"`
	Reason                  string `json:"reason"`
	CandidateStatus         string `json:"candidate_status,omitempty"`
	InputMessages           int    `json:"input_messages"`
	SentMessages            int    `json:"sent_messages"`
	DroppedMessages         int    `json:"dropped_messages"`
	OriginalEstimateTokens  int    `json:"original_estimate_tokens,omitempty"`
	CandidateEstimateTokens int    `json:"candidate_estimate_tokens,omitempty"`
	SentEstimateTokens      int    `json:"sent_estimate_tokens,omitempty"`
	EstimateComplete        bool   `json:"estimate_complete"`
}

// SelectShadowInput accepts only a whole-turn candidate built from a matched
// source snapshot. Every rejected candidate returns the original slice.
func SelectShadowInput(messages []*schema.Message, inputCount int, shadow agent.ContextShadow) ([]*schema.Message, ShadowInputApplication) {
	application := ShadowInputApplication{
		InputMessages: len(messages), SentMessages: len(messages),
		CandidateStatus: shadow.Status, Reason: "candidate_rejected",
	}
	if inputCount != len(messages) {
		application.Reason = "input_count_changed"
		return messages, application
	}
	if shadow.SourceStatus != "matched" {
		application.Reason = "sources_unmatched"
		return messages, application
	}
	if shadow.Budget <= 0 || shadow.Status != "within_budget" || shadow.Candidate > shadow.Budget {
		application.Reason = "candidate_not_within_budget"
		return messages, application
	}
	// NeedsConstraintReview is a quality diagnostic for omitted context; a
	// candidate must still be applied in evaluation to measure that impact.
	if shadow.UnresolvedRequirements != 0 || shadow.UnresolvedRetrievals != 0 {
		application.Reason = "constraint_review_required"
		return messages, application
	}
	kept := make([]bool, len(messages))
	last := -1
	for index, message := range messages {
		if message == nil {
			application.Reason = "invalid_history"
			return messages, application
		}
		if message.Role == schema.User {
			last = index
		}
	}
	if last < 0 {
		application.Reason = "current_turn_missing"
		return messages, application
	}
	previous := -1
	for _, index := range shadow.Kept {
		if index < 0 || index >= len(messages) || index <= previous || kept[index] {
			application.Reason = "invalid_indices"
			return messages, application
		}
		kept[index] = true
		previous = index
	}
	if shadow.Dropped != len(messages)-len(shadow.Kept) {
		application.Reason = "dropped_count_mismatch"
		return messages, application
	}
	for index, message := range messages {
		if message.Role == schema.System && !kept[index] {
			application.Reason = "system_message_dropped"
			return messages, application
		}
	}
	starts := make([]int, 0, 4)
	for index, message := range messages {
		if message.Role == schema.User {
			starts = append(starts, index)
		} else if message.Role != schema.System && len(starts) == 0 {
			application.Reason = "invalid_history"
			return messages, application
		}
	}
	for turn, start := range starts {
		end := len(messages)
		if turn+1 < len(starts) {
			end = starts[turn+1]
		}
		anyKept, allKept := false, true
		for index := start; index < end; index++ {
			if messages[index].Role == schema.System {
				continue
			}
			anyKept = anyKept || kept[index]
			allKept = allKept && kept[index]
		}
		if start == last && !allKept {
			application.Reason = "current_turn_dropped"
			return messages, application
		}
		if anyKept && !allKept {
			application.Reason = "split_turn"
			return messages, application
		}
	}
	selected := make([]*schema.Message, 0, len(shadow.Kept))
	for index, keep := range kept {
		if keep {
			selected = append(selected, messages[index])
		}
	}
	application.Applied = true
	application.Reason = "applied"
	application.SentMessages = len(selected)
	application.DroppedMessages = len(messages) - len(selected)
	return selected, application
}

type shadowApplicationKey struct{}

type shadowCandidate struct {
	inputCount int
	input      *agent.InputObservation
	shadow     *agent.ContextShadow
}

type shadowApplicationState struct {
	mu           sync.Mutex
	candidate    *shadowCandidate
	applications []ShadowInputApplication
}

func (s *shadowApplicationState) observe(progress agent.Progress) {
	if progress.Kind != agent.ProgressModel {
		return
	}
	candidate := &shadowCandidate{inputCount: -1}
	if progress.Input != nil {
		input := *progress.Input
		candidate.input = &input
		candidate.inputCount = input.Messages
	}
	if progress.Shadow != nil {
		shadow := *progress.Shadow
		shadow.Kept = append([]int(nil), progress.Shadow.Kept...)
		shadow.Sources = append([]agent.ShadowSource(nil), progress.Shadow.Sources...)
		candidate.shadow = &shadow
	}
	s.mu.Lock()
	s.candidate = candidate
	s.mu.Unlock()
}

func (s *shadowApplicationState) apply(messages []*schema.Message) []*schema.Message {
	s.mu.Lock()
	candidate := s.candidate
	if candidate != nil {
		copy := *candidate
		candidate = &copy
	}
	s.mu.Unlock()
	application := ShadowInputApplication{InputMessages: len(messages), SentMessages: len(messages), Reason: "candidate_missing"}
	if candidate != nil {
		if candidate.input != nil {
			application.OriginalEstimateTokens = candidate.input.Total
			application.SentEstimateTokens = candidate.input.Total
			application.EstimateComplete = candidate.input.Tools >= 0
		}
	}
	if candidate != nil && candidate.shadow != nil {
		selected, result := SelectShadowInput(messages, candidate.inputCount, *candidate.shadow)
		application = result
		if candidate.input != nil {
			application.OriginalEstimateTokens = candidate.input.Total
			application.EstimateComplete = candidate.input.Tools >= 0
		}
		application.CandidateEstimateTokens = candidate.shadow.Candidate
		application.SentEstimateTokens = application.OriginalEstimateTokens
		if application.Applied {
			application.SentEstimateTokens = candidate.shadow.Candidate
		}
		messages = selected
	} else if candidate != nil {
		application.Reason = "shadow_candidate_missing"
	}
	s.mu.Lock()
	s.applications = append(s.applications, application)
	s.mu.Unlock()
	return messages
}

func (s *shadowApplicationState) snapshot() []ShadowInputApplication {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ShadowInputApplication(nil), s.applications...)
}

// WrapShadowModelForEvaluation is inert outside RunWithOptions(ApplyShadow).
// The ordinary Harness and its Runtime factories do not install this wrapper.
func WrapShadowModelForEvaluation(ctx context.Context, base model.ChatModel) model.ChatModel {
	state, _ := ctx.Value(shadowApplicationKey{}).(*shadowApplicationState)
	if state == nil || base == nil {
		return base
	}
	return &shadowApplyingModel{ChatModel: base, state: state}
}

type shadowApplyingModel struct {
	model.ChatModel
	state *shadowApplicationState
}

func (m *shadowApplyingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.ChatModel.Generate(ctx, m.state.apply(input), opts...)
}

func (m *shadowApplyingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.ChatModel.Stream(ctx, m.state.apply(input), opts...)
}
