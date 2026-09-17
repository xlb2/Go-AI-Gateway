package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type budgetModel struct{ calls int }

func (*budgetModel) BindTools([]*schema.ToolInfo) error { return nil }
func (m *budgetModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls == 2 {
		return schema.AssistantMessage("ok", nil), nil
	}
	return nil, errors.New("status code: 429")
}
func (m *budgetModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func TestRetryBudgetSharedAcrossCalls(t *testing.T) {
	for _, initial := range []int{0, 1} {
		m := &budgetModel{}
		var observed []int
		wrapped := Wrap(m, Config{Policy: Policy{MaxAttempts: 2}, InitialAttempt: initial, Observer: func(n int, _ string, _ time.Duration) { observed = append(observed, n) }})
		r, err := wrapped.Stream(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		_, err = wrapped.Generate(context.Background(), nil)
		wantCalls := 4 - initial
		if err == nil || m.calls != wantCalls || len(observed) != 2-initial {
			t.Fatalf("initial=%d calls=%d retries=%v err=%v", initial, m.calls, observed, err)
		}
		for i, n := range observed {
			if n != initial+i+1 {
				t.Fatalf("non-monotonic attempts: %v", observed)
			}
		}
	}
}

func TestRetryCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &budgetModel{}
	wrapped := Wrap(m, Config{Policy: Policy{MaxAttempts: 2, BaseDelay: time.Hour, MaxDelay: time.Hour}, Observer: func(int, string, time.Duration) { cancel() }})
	_, err := wrapped.Stream(ctx, nil)
	if !errors.Is(err, context.Canceled) || m.calls != 1 || StatusCode(err) != 429 {
		t.Fatalf("calls=%d err=%v", m.calls, err)
	}
}
