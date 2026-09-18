package e2e

import (
	"testing"

	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

func TestRuntimeDefaultAssembly(t *testing.T) {
	e := newEnv(t, 9041)
	t.Setenv("AGENT_LOOP", "own")
	t.Setenv("RETRY_BASE_DELAY_MS", "1")
	t.Setenv("RETRY_MAX_DELAY_MS", "1")
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "runtime done"}, Rules: []fakemodel.Rule{
		{Match: "parent", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: "delegate_task", Arguments: `{"task":"child"}`}}}},
	}})
	var err error
	e.h, err = harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	// Startup snapshots configuration; subsequent environment changes do not redirect this instance.
	t.Setenv("VOLC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("AGENT_LOOP", "invalid")
	// 假模型会将工具结果组织进最终回答，不能期待原始兜底文本。
	const want = "（据工具结果）runtime done"
	if out := e.run("parent"); out != want {
		t.Fatalf("final reply=%q, want %q", out, want)
	}
	counts := e.countTypes()
	if counts[session.EventStepStart] != 2 || counts[session.EventToolResult] != 1 {
		t.Fatalf("unexpected parent events: %v", counts)
	}
	requests := e.fake.Requests()
	if len(requests) != 3 {
		t.Fatalf("expected parent/child/parent requests, got %d", len(requests))
	}
	for _, req := range requests {
		if len(req.ToolNames) != 6 {
			t.Fatalf("wrong tools: %v", req.ToolNames)
		}
	}
}
