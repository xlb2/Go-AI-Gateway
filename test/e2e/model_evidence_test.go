package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/test/fakemodel"
)

func TestModelEvidenceLargeResultAcrossTurns(t *testing.T) {
	e := newEnv(t, 9170)
	text := strings.Repeat("资料正文", 600) + "END-9170"
	ref, err := (spill.RedisStore{}).SaveText(e.ctx(), e.uid, "fixture", text)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"locator": ref.Locator})
	e.fake.SetScenario(fakemodel.Scenario{Rules: []fakemodel.Rule{{Match: "retrieve", MaxFires: 2, Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "load_large_content", Arguments: string(args)}}}}}})
	e.h, err = harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	e.run("retrieve first")
	requests := e.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	assertToolText := func(req fakemodel.RequestInfo) {
		t.Helper()
		for _, m := range req.Messages {
			if m.Role == "tool" && m.Content == text {
				return
			}
		}
		t.Fatal("provider did not receive complete loaded text")
	}
	assertToolText(requests[1])
	var persisted string
	for _, event := range e.log() {
		if event.Type == session.EventToolResult {
			persisted = event.Content
		}
	}
	if !strings.Contains(persisted, "spill:") || strings.Contains(persisted, "END-9170") {
		t.Fatalf("unexpected history representation: %s", persisted)
	}
	e.run("retrieve again")
	requests = e.fake.Requests()
	if len(requests) != 4 {
		t.Fatalf("requests=%d", len(requests))
	}
	found := false
	for _, m := range requests[2].Messages {
		if m.Role == "tool" {
			if m.Content == text {
				t.Fatal("historical large result was not projected as locator")
			}
			if m.Content == persisted {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("next turn missing persisted locator")
	}
	assertToolText(requests[3])
}

func TestModelEvidenceReasoningRoles(t *testing.T) {
	e := newEnv(t, 9171)
	const reasoning = "REASONING-ONLY-9171"
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "final"}, Rules: []fakemodel.Rule{{Match: "inspect", Reply: fakemodel.Reply{Reasoning: reasoning, ToolCalls: []fakemodel.ToolCall{{Name: "search_memory_archive", Arguments: `{"query":"absent-9171"}`}}}}}})
	var err error
	e.h, err = harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	var observed strings.Builder
	ctx := harness.WithReasoning(e.ctx(), func(s string) { observed.WriteString(s) })
	if _, err := e.h.RunAgentTurn(ctx, e.uid, "inspect", nil); err != nil {
		t.Fatal(err)
	}
	if observed.String() != reasoning {
		t.Fatalf("reasoning callback: %q", observed.String())
	}
	requests := e.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	seen := false
	for _, m := range requests[1].Messages {
		if strings.Contains(m.Content, reasoning) {
			t.Fatal("reasoning mixed into message content")
		}
		if m.ReasoningContent != "" {
			if m.Role != "assistant" || m.ReasoningContent != reasoning {
				t.Fatalf("wrong reasoning role: %+v", m)
			}
			seen = true
		}
	}
	if !seen {
		t.Fatal("SDK did not retain current-turn assistant reasoning field")
	}
	e.run("next turn")
	requests = e.fake.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	for _, m := range requests[2].Messages {
		if strings.Contains(m.Content, reasoning) || m.ReasoningContent != "" {
			t.Fatal("reasoning leaked into next-turn history")
		}
	}
	for _, event := range e.log() {
		if strings.Contains(event.Content, reasoning) {
			t.Fatal("reasoning persisted in event content")
		}
	}
}
