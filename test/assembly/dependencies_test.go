package assembly_test

import (
	"context"
	"strings"
	"testing"

	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
)

type dependencyStore struct {
	session.Store
	replies []string
	summary string
	prompt  string
}

func (s *dependencyStore) SaveReply(_ context.Context, _ uint, text string, _ bool) error {
	s.replies = append(s.replies, text)
	return nil
}
func (*dependencyStore) AppendEvent(context.Context, uint, session.MemoryDTO) error { return nil }
func (*dependencyStore) HasEvent(context.Context, uint, string) (bool, error)       { return false, nil }
func (s *dependencyStore) SaveMessage(_ context.Context, _ uint, msg *schema.Message) error {
	if msg.Role == schema.System {
		s.prompt = msg.Content
	}
	return nil
}
func (*dependencyStore) Repair(context.Context, uint) (int, error)                   { return 0, nil }
func (*dependencyStore) GetHistory(context.Context, uint) ([]*schema.Message, error) { return nil, nil }
func (s *dependencyStore) Compact(ctx context.Context, _ uint, summarize session.CompactSummarizer) error {
	var err error
	s.summary, err = summarize(ctx, []string{"history"})
	return err
}

type dependencyLoop struct{ text string }

func (l dependencyLoop) Stream(context.Context, []*schema.Message, ...einoagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(l.text, nil)}), nil
}

func TestHarnessDependenciesAreInstanceScoped(t *testing.T) {
	t.Setenv("AGENT_LOOP", "invalid") // 显式实例不应进入默认环境装配路径。
	stores := []*dependencyStore{{}, {}}
	instances := make([]*harness.Harness, 2)
	for i, name := range []string{"alpha", "beta"} {
		h, err := harness.NewWithDependencies(harness.Dependencies{Sessions: stores[i], Approvals: approval.RedisStore{}, Executor: sandbox.FromEnv(),
			NewLoop:   func(context.Context) (agent.Loop, error) { return dependencyLoop{text: name}, nil },
			Summarize: func(context.Context, []string) (string, error) { return name + " summary", nil },
			ToolNames: func(context.Context) []string { return []string{name} },
		})
		if err != nil {
			t.Fatal(err)
		}
		instances[i] = h
	}
	for i, name := range []string{"alpha", "beta"} {
		h := instances[i]
		text, err := h.RunAgentTurn(context.Background(), 7, "hello", nil)
		if err != nil || text != name || len(stores[i].replies) != 1 || stores[i].replies[0] != name {
			t.Fatalf("instance %s: text=%q err=%v", name, text, err)
		}
		if stores[i].summary != name+" summary" {
			t.Fatalf("summary=%q", stores[i].summary)
		}
		if !strings.Contains(stores[i].prompt, name) {
			t.Fatal("prompt ignored instance tools")
		}
	}
}

func TestHarnessDependenciesRejectMissing(t *testing.T) {
	if _, err := harness.NewWithDependencies(harness.Dependencies{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}
