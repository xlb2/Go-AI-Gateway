package agent

import (
	"context"
	"reflect"
	"sync"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
)

type ShadowSource struct {
	Index       int    `json:"index"`
	ID          string `json:"source_id"`
	Kept        bool   `json:"kept"`
	Summary     bool   `json:"summary"`
	Interrupted bool   `json:"interrupted"`
	Required    bool   `json:"required"`
	Retrieved   bool   `json:"retrieved"`
}

type shadowSourcesKey struct{}
type shadowSourceBinding struct {
	source  session.HistorySource
	message *schema.Message
}

type readbackEvidence struct {
	mu   sync.Mutex
	ids  []string
	seen map[string]struct{}
}

func (r *readbackEvidence) add(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.seen[id]; exists {
		return
	}
	r.seen[id] = struct{}{}
	r.ids = append(r.ids, id)
}

func (r *readbackEvidence) snapshot() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

type shadowSources struct {
	status             string
	byMessage          map[*schema.Message]session.HistorySource
	byID               map[string]shadowSourceBinding
	requiredByMessage  map[*schema.Message]bool
	unresolvedRequired int
	readbacks          *readbackEvidence
}
type requiredHistorySourcesKey struct{}
type requiredHistorySources struct {
	ids     []string
	invalid int
}
type historySourceReader interface {
	GetHistorySources(context.Context, uint, int) ([]session.HistorySource, error)
}

// ObserveHistorySources adds diagnostics only when shadow mode is enabled.
// Compare the entire ordered projection, including tool metadata, before
// binding its sources to the actual message objects. Never substitute input.
// A failed or racing snapshot does not fail the user's turn.
func ObserveHistorySources(ctx context.Context, store any, owner uint, history []*schema.Message) context.Context {
	if budget, _ := ctx.Value(shadowBudgetKey{}).(int); budget <= 0 {
		return ctx
	}
	requested, _ := ctx.Value(requiredHistorySourcesKey{}).(requiredHistorySources)
	o := shadowSources{
		status:             "unsupported",
		unresolvedRequired: requested.invalid,
		readbacks:          &readbackEvidence{seen: make(map[string]struct{})},
	}
	reader, ok := store.(historySourceReader)
	if ok {
		sources, err := reader.GetHistorySources(ctx, owner, session.GetBudget().ContextWindow)
		o.status = "unavailable"
		if err == nil {
			o.status = "mismatch"
			matched := len(sources) == len(history)
			for i := 0; matched && i < len(history); i++ {
				matched = sources[i].ID != "" && history[i] != nil && sources[i].Message != nil && reflect.DeepEqual(history[i], sources[i].Message)
			}
			if matched {
				o.status = "matched"
				o.byMessage = make(map[*schema.Message]session.HistorySource, len(history))
				o.byID = make(map[string]shadowSourceBinding, len(history))
				for i, msg := range history {
					o.byMessage[msg] = sources[i]
					o.byID[sources[i].ID] = shadowSourceBinding{source: sources[i], message: msg}
				}
				o.requiredByMessage = make(map[*schema.Message]bool)
				for _, id := range requested.ids {
					binding, found := o.byID[id]
					if found && binding.source.EventType == session.EventUserMessage && binding.message.Role == schema.User && !binding.source.Interrupted {
						// The snapshot has separate message pointers; bind to the
						// equivalent object used by the live Harness history.
						o.requiredByMessage[binding.message] = true
						continue
					}
					o.unresolvedRequired++
				}
			}
		}
	}
	if o.status != "matched" {
		o.unresolvedRequired += len(requested.ids)
	}
	return context.WithValue(ctx, shadowSourcesKey{}, o)
}

// WithRequiredHistorySources marks exact source IDs selected by the user or a
// trusted caller. IDs are only considered when snapshot role/type prove they
// refer to a complete user-authored message. The model cannot set this context.
func WithRequiredHistorySources(ctx context.Context, ids ...string) context.Context {
	r := requiredHistorySources{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if len(r.ids) == 16 {
			r.invalid++
			continue
		}
		r.ids = append(r.ids, id)
	}
	return context.WithValue(ctx, requiredHistorySourcesKey{}, r)
}

func requiredShadowSelection(ctx context.Context, messages []*schema.Message) ([]int, int) {
	required, _ := ctx.Value(requiredHistorySourcesKey{}).(requiredHistorySources)
	observed, ok := ctx.Value(shadowSourcesKey{}).(shadowSources)
	if !ok {
		return nil, len(required.ids) + required.invalid
	}
	indices := make([]int, 0, len(observed.requiredByMessage))
	for index, msg := range messages {
		if observed.requiredByMessage[msg] {
			indices = append(indices, index)
		}
	}
	return indices, observed.unresolvedRequired
}

func retrievedShadowSelection(ctx context.Context, messages []*schema.Message) ([]int, map[*schema.Message]bool, int) {
	observed, ok := ctx.Value(shadowSourcesKey{}).(shadowSources)
	if !ok || observed.readbacks == nil {
		return nil, nil, 0
	}
	ids := observed.readbacks.snapshot()
	if len(ids) == 0 {
		return nil, nil, 0
	}
	if observed.status != "matched" {
		return nil, nil, len(ids)
	}

	indices := make([]int, 0, len(ids))
	retrieved := make(map[*schema.Message]bool, len(ids))
	unresolved := 0
	for _, id := range ids {
		binding, found := observed.byID[id]
		if !found || binding.message == nil || binding.source.Interrupted || binding.message.Role == schema.System {
			unresolved++
			continue
		}
		index := -1
		ambiguous := false
		for i, message := range messages {
			if message != binding.message {
				continue
			}
			if index >= 0 {
				ambiguous = true
				break
			}
			index = i
		}
		if index < 0 || ambiguous {
			unresolved++
			continue
		}
		indices = append(indices, index)
		retrieved[binding.message] = true
	}
	return indices, retrieved, unresolved
}

func recordReadbackEvidence(ctx context.Context, id string) {
	observed, ok := ctx.Value(shadowSourcesKey{}).(shadowSources)
	if ok {
		observed.readbacks.add(id)
	}
}

func observeShadowSources(ctx context.Context, messages []*schema.Message, shadow *ContextShadow, retrieved map[*schema.Message]bool) {
	o, ok := ctx.Value(shadowSourcesKey{}).(shadowSources)
	shadow.SourceStatus = "not_observed"
	if ok {
		shadow.SourceStatus = o.status
		if shadow.UnresolvedRequirements < o.unresolvedRequired {
			shadow.UnresolvedRequirements = o.unresolvedRequired
		}
	}
	kept := make(map[int]bool, len(shadow.Kept))
	for _, index := range shadow.Kept {
		kept[index] = true
	}
	for i, msg := range messages {
		source, found := o.byMessage[msg]
		if !found {
			shadow.UnmappedMessages++
			continue
		}
		shadow.Sources = append(shadow.Sources, ShadowSource{Index: i, ID: source.ID, Kept: kept[i], Summary: source.Summary, Interrupted: source.Interrupted, Required: o.requiredByMessage[msg], Retrieved: retrieved[msg]})
	}
}
