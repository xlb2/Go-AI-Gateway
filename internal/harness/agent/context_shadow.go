package agent

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// ContextShadow is diagnostic only: indices refer to the current SDK input,
// not persisted seq/source IDs. No candidate is sent to the model.
type ContextShadow struct {
	SourceStatus           string         `json:"source_status"`
	Sources                []ShadowSource `json:"sources,omitempty"`
	UnmappedMessages       int            `json:"unmapped_messages"`
	RequiredSources        int            `json:"required_sources"`
	UnresolvedRequirements int            `json:"unresolved_requirements"`
	RetrievedSources       int            `json:"retrieved_sources"`
	UnresolvedRetrievals   int            `json:"unresolved_retrievals"`
	EvidenceTurns          int            `json:"evidence_turns"`
	OmittedEvidenceTurns   int            `json:"omitted_evidence_turns"`
	Budget                 int            `json:"budget"`
	Original               int            `json:"original_estimate"`
	Candidate              int            `json:"candidate_estimate"`
	Kept                   []int          `json:"kept_indices"`
	Dropped                int            `json:"dropped_messages"`
	Status                 string         `json:"status"`
	NeedsConstraintReview  bool           `json:"needs_constraint_review"`
}

type shadowBudgetKey struct{}

// WithContextShadow enables per-request diagnostic calculation. No default
// budget or production input policy is changed. Nonpositive budgets disable it.
func WithContextShadow(ctx context.Context, budget int) context.Context {
	return context.WithValue(ctx, shadowBudgetKey{}, budget)
}

// EvaluateContextShadow keeps all system messages and the last user turn
// including all tool steps; older user turns are added whole, newest first.
// This assumes an already valid protocol sequence and does not repair it.
// Without a user boundary (e.g. a child task), the whole input is mandatory.
func EvaluateContextShadow(messages []*schema.Message, tools, budget int) ContextShadow {
	return EvaluateContextShadowWithRequired(messages, tools, budget, nil)
}

// EvaluateContextShadowWithRequired preserves each explicitly required user
// source and the complete chronological suffix through the current turn. An
// invalid index/role is reported unresolved and never treated as a constraint.
func EvaluateContextShadowWithRequired(messages []*schema.Message, tools, budget int, requiredIndices []int) ContextShadow {
	return EvaluateContextShadowWithEvidence(messages, tools, budget, requiredIndices, nil)
}

// EvaluateContextShadowWithEvidence keeps complete turns containing successfully
// retrieved sources before filling the remaining budget with recent turns. Read
// evidence is a candidate, never a required user constraint.
func EvaluateContextShadowWithEvidence(messages []*schema.Message, tools, budget int, requiredIndices, evidenceIndices []int) ContextShadow {
	r := ContextShadow{Budget: budget, Original: estimateInput(messages, tools).Total, Kept: []int{}}
	if budget <= 0 {
		r.Status = "disabled"
		return r
	}
	if tools < 0 {
		r.Status = "unknown_tool_schema"
		return r
	}
	last := -1
	for i, m := range messages {
		if m != nil && m.Role == schema.User {
			last = i
		}
	}
	mandatoryStart := last
	evidenceTurns, retrievedSources, unresolvedEvidence := evidenceTurnRanges(messages, evidenceIndices)
	r.EvidenceTurns = len(evidenceTurns)
	r.RetrievedSources = retrievedSources
	r.UnresolvedRetrievals = unresolvedEvidence
	validRequired := map[int]bool{}
	for _, index := range requiredIndices {
		if index < 0 || index >= len(messages) || messages[index] == nil || messages[index].Role != schema.User {
			r.UnresolvedRequirements++
			continue
		}
		if validRequired[index] {
			continue
		}
		validRequired[index] = true
		r.RequiredSources++
		if mandatoryStart < 0 || index < mandatoryStart {
			mandatoryStart = index
		}
	}
	keep := make([]bool, len(messages))
	for i, m := range messages {
		keep[i] = mandatoryStart < 0 || i >= mandatoryStart || (m != nil && m.Role == schema.System)
	}
	estimate := func() int {
		selected := make([]*schema.Message, 0, len(messages))
		for i, m := range messages {
			if keep[i] {
				selected = append(selected, m)
			}
		}
		return estimateInput(selected, tools).Total
	}
	r.Status = "within_budget"
	if estimate() > budget {
		if r.RequiredSources > 0 {
			r.Status = "required_over_budget"
		} else {
			r.Status = "mandatory_over_budget"
		}
		r.OmittedEvidenceTurns = len(evidenceTurns)
	} else {
		for i, turn := range evidenceTurns {
			changed := make([]int, 0, turn.end-turn.start)
			for index := turn.start; index < turn.end; index++ {
				if !keep[index] {
					keep[index] = true
					changed = append(changed, index)
				}
			}
			if estimate() > budget {
				for _, index := range changed {
					keep[index] = false
				}
				r.OmittedEvidenceTurns = len(evidenceTurns) - i
				break
			}
		}
		end := mandatoryStart
		for end > 0 {
			start := end - 1
			for start > 0 && (messages[start] == nil || messages[start].Role != schema.User) {
				start--
			}
			changed := []int{}
			for i := start; i < end; i++ {
				if !keep[i] {
					keep[i] = true
					changed = append(changed, i)
				}
			}
			if estimate() > budget {
				for _, i := range changed {
					keep[i] = false
				}
				break // contiguous recent turns; do not cherry-pick older cheap turns
			}
			end = start
		}
	}
	r.Candidate = estimate()
	for i := range messages {
		if keep[i] {
			r.Kept = append(r.Kept, i)
		} else {
			r.Dropped++
		}
	}
	r.NeedsConstraintReview = r.Dropped > 0 || r.UnresolvedRequirements > 0 || r.UnresolvedRetrievals > 0 || r.OmittedEvidenceTurns > 0
	return r
}

type evidenceTurn struct{ start, end int }

func evidenceTurnRanges(messages []*schema.Message, indices []int) ([]evidenceTurn, int, int) {
	turns := make([]evidenceTurn, 0, len(indices))
	seenIndices := make(map[int]bool, len(indices))
	seenTurns := make(map[evidenceTurn]bool, len(indices))
	var retrievedSources, unresolved int
	for _, index := range indices {
		if seenIndices[index] {
			continue
		}
		seenIndices[index] = true
		if index < 0 || index >= len(messages) || messages[index] == nil || messages[index].Role == schema.System {
			unresolved++
			continue
		}
		start := -1
		for i := index; i >= 0; i-- {
			if messages[i] != nil && messages[i].Role == schema.User {
				start = i
				break
			}
		}
		if start < 0 {
			for i, message := range messages {
				if message != nil && message.Role != schema.System {
					start = i
					break
				}
			}
		}
		if start < 0 || start > index {
			unresolved++
			continue
		}
		retrievedSources++
		end := index + 1
		for end < len(messages) {
			if messages[end] != nil && messages[end].Role == schema.User {
				break
			}
			end++
		}
		turn := evidenceTurn{start: start, end: end}
		if !seenTurns[turn] {
			seenTurns[turn] = true
			turns = append(turns, turn)
		}
	}
	return turns, retrievedSources, unresolved
}
