package assembly_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
)

func TestProjectionSourceMapping(t *testing.T) {
	events := []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: "old"},
		{Type: session.EventUserMessage, Role: "user", Content: "same"},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "orphan", Content: "drop"},
		{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "b", Name: "read"}, {ID: "a", Name: "read"}}},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "a", Content: "same"},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "b", Content: "same"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "partial", Interrupted: true},
		{Type: session.EventCompactionSummary, Content: "summary", CompactionFrom: 0, CompactionTo: 10},
	}
	got := session.ProjectMessagesWithSources(events, 10)
	want := []int{17, 11, 13, 14, 15, 16}
	if len(got) != len(want) {
		t.Fatalf("projection=%+v", got)
	}
	var messages []*schema.Message
	for i, p := range got {
		if p.Seq != want[i] {
			t.Fatalf("seq[%d]=%d", i, p.Seq)
		}
		messages = append(messages, p.Message)
	}
	if !reflect.DeepEqual(messages, session.ProjectMessagesFrom(events, 10)) {
		t.Fatal("source path diverged from production projection")
	}
	if !got[0].Summary || got[0].CoveredTo != 10 || got[0].EventType != session.EventCompactionSummary {
		t.Fatal("summary attributed as original")
	}
	if !got[5].Interrupted || !strings.Contains(got[5].Message.Content, "被中断") {
		t.Fatal("interrupted source lost")
	}
}

func TestProjectionSourceDropsIncompletePairs(t *testing.T) {
	events := []session.MemoryDTO{
		{Role: "user", Content: "legacy"},
		{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}}},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "a", Content: "not complete"},
		{Type: session.EventUserMessage, Role: "user", Content: "next"},
	}
	got := session.ProjectMessagesWithSources(events, 25)
	if len(got) != 2 || got[0].Seq != 25 || got[1].Seq != 28 || got[0].EventType != session.EventUserMessage {
		t.Fatalf("projection=%+v", got)
	}
}
