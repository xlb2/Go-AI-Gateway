package assembly_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
)

type archiveStore struct {
	session.Store
	name  string
	owner uint
}

func (s *archiveStore) SearchArchival(_ context.Context, owner uint, _ string, _ int) ([]*schema.Message, error) {
	s.owner = owner
	return []*schema.Message{schema.UserMessage(s.name)}, nil
}

type pendingStore struct {
	approval.Store
	actions []approval.PendingAction
	owner   uint
}

func (s *pendingStore) SetPending(_ context.Context, owner uint, action approval.PendingAction) error {
	s.owner = owner
	s.actions = append(s.actions, action)
	return nil
}

type contentStore struct {
	name, content string
	owner         uint
	fail          bool
}

func TestStorageSpillThresholdPerResult(t *testing.T) {
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	store := &contentStore{name: "spill:fixture"}
	set, err := agent.NewStorageTools(&archiveStore{}, &pendingStore{}, store)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		text := strings.Repeat("中", 2000)
		msg := schema.ToolMessage(text, "call")
		if got := set.ToolResult(ctx, msg); got.Content != text {
			t.Fatalf("per-result boundary changed after %d calls", i)
		}
	}
	if store.content != "" {
		t.Fatal("2000-rune result unexpectedly spilled")
	}
	text := strings.Repeat("中", 2001)
	msg := schema.ToolMessage(text, "call")
	got := set.ToolResult(ctx, msg)
	if !strings.Contains(got.Content, "spill:fixture") || store.content != text || msg.Content != text {
		t.Fatal("spill did not preserve original model message")
	}
}

func (s *contentStore) SaveText(_ context.Context, owner uint, _, content string) (spill.Ref, error) {
	if s.fail {
		return spill.Ref{}, errors.New("store unavailable")
	}
	s.owner, s.content = owner, content
	return spill.Ref{Locator: s.name, Bytes: len(content), RetrievalHint: s.name}, nil
}
func (s *contentStore) LoadText(_ context.Context, locator string) (string, error) {
	if locator != s.name {
		return "", errors.New("wrong store")
	}
	return s.content, nil
}

func TestStorageToolsKeepStoresSeparate(t *testing.T) {
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	sets := make([]*agent.StorageTools, 2)
	archives := []*archiveStore{{name: "alpha"}, {name: "beta"}}
	pending := []*pendingStore{{}, {}}
	contents := []*contentStore{{name: "alpha"}, {name: "beta"}}
	for i := range sets {
		var err error
		sets[i], err = agent.NewStorageTools(archives[i], pending[i], contents[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, set := range sets {
		name := archives[i].name
		registry := map[string]tool.InvokableTool{}
		for _, item := range set.Tools() {
			info, err := item.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			registry[info.Name] = item
		}
		run := func(toolName, args string) string {
			t.Helper()
			out, err := registry[toolName].InvokableRun(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		if out := run("search_memory_archive", `{"query":"history"}`); !strings.Contains(out, name) {
			t.Fatal(out)
		}
		run("execute_system_defense", `{"emotion":"angry","threat_level":"high"}`)
		if len(pending[i].actions) != 1 || pending[i].actions[0].Plan.FilePath == "" {
			t.Fatal("missing defense proposal")
		}
		run("store_large_content", `{"name":"note","content":"saved"}`)
		if out := run("load_large_content", `{"locator":"`+name+`"}`); out != "saved" {
			t.Fatal(out)
		}
		p, err := guard.NewConfiguredPipeline(guard.Config{Ask: set.Ask})
		if err != nil {
			t.Fatal(err)
		}
		probe := &guardedProbe{name: "mcp__server__read"}
		if _, err := p.Wrap(probe).(tool.InvokableTool).InvokableRun(ctx, "{}"); err != nil {
			t.Fatal(err)
		}
		if probe.calls != 0 || len(pending[i].actions) != 2 || pending[i].actions[1].Action != probe.name {
			t.Fatal("approval used wrong store or executed tool")
		}
		if archives[i].owner != 7 || pending[i].owner != 7 || contents[i].owner != 7 {
			t.Fatal("owner lost")
		}
		if i == 0 && (len(pending[1].actions) != 0 || contents[1].content != "" || archives[1].owner != 0) {
			t.Fatal("other instance modified")
		}
	}
}

func TestStorageToolsAutomaticSpillUsesConfiguredStore(t *testing.T) {
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	content := &contentStore{name: "instance-locator"}
	set, err := agent.NewStorageTools(&archiveStore{}, &pendingStore{}, content)
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", 2100)
	msg := &schema.Message{Role: schema.Tool, Content: large, ToolCallID: "call", ToolName: "large"}
	dto := set.ToolResult(ctx, msg)
	if content.content != large || !strings.Contains(dto.Content, content.name) || dto.ToolCallID != "call" || dto.ToolName != "large" {
		t.Fatalf("wrong spill DTO: %+v", dto)
	}
	content.fail = true
	if dto := set.ToolResult(ctx, msg); dto.Content != large {
		t.Fatal("spill failure lost content")
	}
}

func TestStorageToolsRejectMissingStores(t *testing.T) {
	for _, tc := range []struct {
		s session.Store
		a approval.Store
		c spill.Store
	}{
		{nil, &pendingStore{}, &contentStore{}},
		{&archiveStore{}, nil, &contentStore{}},
		{&archiveStore{}, &pendingStore{}, nil},
	} {
		if _, err := agent.NewStorageTools(tc.s, tc.a, tc.c); err == nil {
			t.Fatal("missing store accepted")
		}
	}
}
