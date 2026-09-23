package agent

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/subagent"
)

// AttemptTotals count SDK calls, including attempts inside our retry wrapper.
// WithUsage counts calls with at least one usage snapshot, not completed calls.
// Errors count observed API/read errors; closing a stream early is not success.
// EstimatedInput is heuristic; unknown tool schemas are counted separately.
type AttemptTotals struct {
	Attempts, WithUsage, Errors    int
	Prompt, Completion             int
	EstimatedInput, UnknownSchemas int
}

type AccountingSnapshot struct{ Main, Child, Summary AttemptTotals }

// ModelAccounting is per CLI operation and contains no prompts or result bodies.
// Child calls share it through context; all mutations and snapshots are locked.
// It does not replace persisted execution/usage events or a provider invoice.
type ModelAccounting struct {
	mu     sync.Mutex
	totals AccountingSnapshot
}

type accountingKey struct{}

func WithModelAccounting(ctx context.Context) (context.Context, *ModelAccounting) {
	a := &ModelAccounting{}
	return context.WithValue(ctx, accountingKey{}, a), a
}

func (a *ModelAccounting) Snapshot() AccountingSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.totals
}

type accountedModel struct {
	model.ChatModel
	summary    bool
	toolTokens int
}

// Install below retry.Wrap, once per raw model. No extra goroutine, model call,
// stream buffering or persistent write is introduced by accounting.
func accountModel(m model.ChatModel, summary bool) model.ChatModel {
	return &accountedModel{ChatModel: m, summary: summary}
}

func (m *accountedModel) BindTools(infos []*schema.ToolInfo) error {
	if err := m.ChatModel.BindTools(infos); err != nil {
		return err
	}
	m.toolTokens = estimateToolSchemas(infos)
	return nil
}

type accountedAttempt struct {
	ledger             *ModelAccounting
	totals             *AttemptTotals
	sawUsage, sawError bool
	prompt, completion int
}

func (m *accountedModel) begin(ctx context.Context, input []*schema.Message) *accountedAttempt {
	a, _ := ctx.Value(accountingKey{}).(*ModelAccounting)
	if a == nil {
		return nil
	}
	o := estimateInput(input, m.toolTokens)
	a.mu.Lock()
	defer a.mu.Unlock()
	t := &a.totals.Main
	if m.summary {
		t = &a.totals.Summary
	} else if subagent.IsNested(ctx) {
		t = &a.totals.Child
	}
	t.Attempts++
	t.EstimatedInput += o.Total
	if o.Tools < 0 {
		t.UnknownSchemas++
	}
	return &accountedAttempt{ledger: a, totals: t}
}

// Usage chunks are cumulative snapshots in the supported SDK. Match its
// ConcatMessages maximum semantics, adding only deltas within one attempt.
func (a *accountedAttempt) observe(msg *schema.Message) {
	if a == nil || msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return
	}
	u := msg.ResponseMeta.Usage
	a.ledger.mu.Lock()
	defer a.ledger.mu.Unlock()
	if !a.sawUsage {
		a.sawUsage = true
		a.totals.WithUsage++
	}
	p, c := max(a.prompt, u.PromptTokens), max(a.completion, u.CompletionTokens)
	a.totals.Prompt += p - a.prompt
	a.totals.Completion += c - a.completion
	a.prompt, a.completion = p, c
}

func (a *accountedAttempt) failure(err error) error {
	if a == nil || err == nil {
		return err
	}
	a.ledger.mu.Lock()
	defer a.ledger.mu.Unlock()
	if !a.sawError {
		a.sawError = true
		a.totals.Errors++
	}
	return err
}

func (m *accountedModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	a := m.begin(ctx, input)
	out, err := m.ChatModel.Generate(ctx, input, opts...)
	a.observe(out)
	return out, a.failure(err)
}

func (m *accountedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	a := m.begin(ctx, input)
	r, err := m.ChatModel.Stream(ctx, input, opts...)
	if err != nil {
		return r, a.failure(err)
	}
	if a == nil || r == nil {
		return r, nil
	}
	return schema.StreamReaderWithConvert(r, func(msg *schema.Message) (*schema.Message, error) {
		a.observe(msg)
		return msg, nil
	}, schema.WithErrWrapper(a.failure)), nil
}
