package e2e

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

func TestContextShadowUsesSuccessfulReadbackAsEvidence(t *testing.T) {
	e := newEnv(t, 9192)
	configureContextShadowHarness(t, e)
	store := session.RedisStore{}
	if err := store.SaveMessage(e.ctx(), e.uid, schema.UserMessage("an earlier question")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReply(e.ctx(), e.uid, "historical evidence FACT-17", false); err != nil {
		t.Fatal(err)
	}
	middleText := "middle unrelated " + strings.Repeat("无关", 16000)
	if err := store.SaveMessage(e.ctx(), e.uid, schema.UserMessage(middleText)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReply(e.ctx(), e.uid, "middle reply", false); err != nil {
		t.Fatal(err)
	}
	sources, err := store.GetHistorySources(e.ctx(), e.uid, 0)
	if err != nil {
		t.Fatal(err)
	}
	var userID, evidenceID, middleID string
	for _, source := range sources {
		if source.Message == nil {
			continue
		}
		switch source.Message.Content {
		case "an earlier question":
			userID = source.ID
		case "historical evidence FACT-17":
			evidenceID = source.ID
		case middleText:
			middleID = source.ID
		}
	}
	if userID == "" || evidenceID == "" || middleID == "" {
		t.Fatalf("source IDs missing: %+v", sources)
	}
	e.fake.SetScenario(fakemodel.Scenario{
		Rules: []fakemodel.Rule{{
			Match:    "current question",
			MaxFires: 1,
			Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{
				Name:      "read_history_source",
				Arguments: `{"source_id":"` + evidenceID + `"}`,
			}}},
		}},
		Default: fakemodel.Reply{Text: "done"},
	})
	var mu sync.Mutex
	var shadows []agent.ContextShadow
	ctx := agent.WithContextShadow(e.ctx(), 20000)
	ctx = agent.WithProgress(ctx, func(p agent.Progress) {
		if p.Shadow != nil {
			mu.Lock()
			shadows = append(shadows, *p.Shadow)
			mu.Unlock()
		}
	})
	if _, err := e.h.RunAgentTurn(ctx, e.uid, "current question", nil); err != nil {
		t.Fatal(err)
	}
	requests := e.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("unexpected model requests: %d", len(requests))
	}
	toolResult := contextShadowToolResult(requests[1])
	if !strings.Contains(toolResult, evidenceID) || !strings.Contains(toolResult, "historical evidence FACT-17") {
		t.Fatalf("history source was not actually read: %q", toolResult)
	}
	actualHasMiddle := false
	for _, message := range requests[1].Messages {
		actualHasMiddle = actualHasMiddle || strings.Contains(message.Content, "middle unrelated")
	}
	if !actualHasMiddle {
		t.Fatal("shadow candidate replaced the actual SDK history")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(shadows) != 2 {
		t.Fatalf("shadow observations=%+v", shadows)
	}
	shadow := shadows[1]
	if shadow.SourceStatus != "matched" || shadow.RetrievedSources != 1 || shadow.UnresolvedRetrievals != 0 || shadow.EvidenceTurns != 1 || shadow.OmittedEvidenceTurns != 0 || shadow.RequiredSources != 0 {
		t.Fatalf("readback not classified as evidence: %+v", shadow)
	}
	byID := map[string]agent.ShadowSource{}
	for _, source := range shadow.Sources {
		byID[source.ID] = source
	}
	if !byID[evidenceID].Retrieved || !byID[evidenceID].Kept || byID[evidenceID].Required || !byID[userID].Kept || byID[middleID].Kept {
		t.Fatalf("unexpected shadow source selection: %+v", shadow.Sources)
	}
}

func TestContextShadowDoesNotPromoteSearchOrFailedReadback(t *testing.T) {
	cases := []struct {
		name       string
		tool       string
		target     string
		wantResult string
		spillOnly  bool
	}{
		{name: "search preview", tool: "search_history_sources", target: "historical evidence FACT-17", wantResult: "FACT-17"},
		{name: "missing source", tool: "read_history_source", target: "historical evidence FACT-17", wantResult: "执行失败"},
		{name: "unexpanded spill pointer", tool: "read_history_source", target: "external result pointer", wantResult: "external result pointer", spillOnly: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := uint(9193 + i)
			e := newEnv(t, owner)
			configureContextShadowHarness(t, e)
			store := session.RedisStore{}
			if err := store.SaveMessage(e.ctx(), owner, schema.UserMessage("an earlier question")); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveReply(e.ctx(), owner, "historical evidence FACT-17", false); err != nil {
				t.Fatal(err)
			}
			if tc.spillOnly {
				locator := fmt.Sprintf("spill:%d:%032x", owner, 1)
				if err := store.AppendEvent(e.ctx(), owner, session.MemoryDTO{Type: session.EventAssistantMessage, Role: string(schema.Assistant), Content: tc.target, SpillLocator: locator}); err != nil {
					t.Fatal(err)
				}
			}
			sources, err := store.GetHistorySources(e.ctx(), owner, 0)
			if err != nil {
				t.Fatal(err)
			}
			var evidenceID string
			for _, source := range sources {
				if source.Message != nil && source.Message.Content == tc.target {
					evidenceID = source.ID
					break
				}
			}
			if evidenceID == "" {
				t.Fatal("evidence source ID missing")
			}
			arguments := `{"query":"FACT-17"}`
			if tc.tool == "read_history_source" {
				readID := evidenceID
				if tc.name == "missing source" {
					lastColon := strings.LastIndex(evidenceID, ":")
					readID = evidenceID[:lastColon+1] + "999999"
				}
				arguments = `{"source_id":"` + readID + `"}`
			}
			e.fake.SetScenario(fakemodel.Scenario{
				Rules: []fakemodel.Rule{{
					Match:    "current question",
					MaxFires: 1,
					Reply:    fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: tc.tool, Arguments: arguments}}},
				}},
				Default: fakemodel.Reply{Text: "done"},
			})
			var mu sync.Mutex
			var shadows []agent.ContextShadow
			ctx := agent.WithProgress(agent.WithContextShadow(e.ctx(), 20000), func(p agent.Progress) {
				if p.Shadow != nil {
					mu.Lock()
					shadows = append(shadows, *p.Shadow)
					mu.Unlock()
				}
			})
			if _, err := e.h.RunAgentTurn(ctx, owner, "current question", nil); err != nil {
				t.Fatal(err)
			}
			requests := e.fake.Requests()
			if len(requests) != 2 {
				t.Fatalf("unexpected model requests: %d", len(requests))
			}
			toolResult := contextShadowToolResult(requests[1])
			if !strings.Contains(toolResult, tc.wantResult) {
				t.Fatalf("tool %s did not produce its expected result %q: %q", tc.tool, tc.wantResult, toolResult)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(shadows) != 2 {
				t.Fatalf("shadow observations=%+v", shadows)
			}
			shadow := shadows[1]
			if shadow.SourceStatus != "matched" || shadow.RetrievedSources != 0 || shadow.UnresolvedRetrievals != 0 || shadow.EvidenceTurns != 0 {
				t.Fatalf("non-read promoted to evidence: %+v", shadow)
			}
			for _, source := range shadow.Sources {
				if source.ID == evidenceID && source.Retrieved {
					t.Fatalf("source %s was marked retrieved: %+v", evidenceID, shadow.Sources)
				}
			}
		})
	}
}

func configureContextShadowHarness(t *testing.T, e *env) {
	t.Helper()
	h, err := harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	h.Exec = &recordingExecutor{}
	e.h = h
}

func contextShadowToolResult(request fakemodel.RequestInfo) string {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "tool" {
			return request.Messages[i].Content
		}
	}
	return ""
}
