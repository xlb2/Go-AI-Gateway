package e2e

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/tokenmeter"
)

// 摘要追加在日志末尾，但在模型历史里应先于未压缩尾部；旧 checkpoint 不能吞掉尾部。
func TestCompactionProjectionPreservesTailWithLegacyCheckpoint(t *testing.T) {
	dtos := []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: "old question"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "old answer"},
		{Type: session.EventUserMessage, Role: "user", Content: "recent question"},
		{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "recent", Name: "lookup", Arguments: `{}`}}},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "recent", Content: "recent fact"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "recent answer"},
		{Type: session.EventCompactionSummary, Role: "assistant", Content: "old summary", CompactionTo: 1},
		{Type: session.EventCheckpoint, Role: "system", FoldUpto: 5},
	}
	want := append([]*schema.Message{schema.UserMessage("【早期对话摘要】old summary")}, session.ProjectMessagesFrom(dtos[2:6], 2)...)
	for _, base := range []int{0, 2} {
		if got := session.ProjectMessagesFrom(dtos[base:], base); !reflect.DeepEqual(got, want) {
			t.Fatalf("base=%d: projection lost or reordered the recent tail: got=%+v want=%+v", base, got, want)
		}
	}
}

// 直接注入摘要器以检查被摘要的内容，存储、压缩和历史读取都走真实实现。
func TestCompactionPreservesRecentTurnsAndRebuildsFold(t *testing.T) {
	for index, withTools := range []bool{false, true} {
		t.Run(fmt.Sprintf("tools=%t", withTools), func(t *testing.T) {
			e := newEnv(t, uint(9080+index))
			store := session.RedisStore{}
			previousBudget := session.GetBudget()
			// 在校准系数 0.4..2.5 内，旧内容都超触发线，完整近期轮次都能放进窗口。
			session.SetBudget(tokenmeter.Budget{ContextWindow: 4000, ThresholdRatio: 0.8, RetainRatio: 0.08})
			t.Cleanup(func() { session.SetBudget(previousBudget) })
			appendEvents := func(events []session.MemoryDTO) {
				t.Helper()
				for _, event := range events {
					if err := store.AppendEvent(e.ctx(), e.uid, event); err != nil {
						t.Fatal(err)
					}
				}
			}
			old := []session.MemoryDTO{
				{Type: session.EventUserMessage, Role: "user", Content: "old-fact " + strings.Repeat("x", 40000)},
				{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "old", Name: "lookup", Arguments: `{"key":"old-key"}`}}},
				{Type: session.EventToolResult, Role: "tool", ToolCallID: "old", Content: "old-tool-fact"},
				{Type: session.EventAssistantMessage, Role: "assistant", Content: "old answer"},
			}
			recent := []session.MemoryDTO{{Type: session.EventUserMessage, Role: "user", Content: "recent-question"}}
			if withTools {
				recent = append(recent,
					session.MemoryDTO{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "recent", Name: "lookup", Arguments: `{}`}}},
					session.MemoryDTO{Type: session.EventToolResult, Role: "tool", ToolCallID: "recent", Content: "recent-tool-fact " + strings.Repeat("y", 1600)},
				)
			}
			recent = append(recent, session.MemoryDTO{Type: session.EventAssistantMessage, Role: "assistant", Content: "recent-answer " + strings.Repeat("z", 1600)})
			appendEvents(old)
			appendEvents(recent)
			before := e.log()
			calls := 0
			if err := store.Compact(e.ctx(), e.uid, func(_ context.Context, input []string) (string, error) {
				calls++
				text := strings.Join(input, "\n")
				if !strings.Contains(text, "old-fact") || !strings.Contains(text, "old-tool-fact") || !strings.Contains(text, "old-key") {
					t.Fatalf("summary input omitted early evidence: %s", text)
				}
				if strings.Contains(text, "recent-") {
					t.Fatal("recent turn must remain verbatim, not enter the summary")
				}
				return "old summary", nil
			}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("compression must actually run once, got %d", calls)
			}
			want := append([]*schema.Message{schema.UserMessage("【早期对话摘要】old summary")}, session.ProjectMessagesFrom(recent, 0)...)
			assertHistory := func() {
				t.Helper()
				got, err := store.GetHistory(e.ctx(), e.uid)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("history differs from summary + exact recent turn: err=%v got=%+v want=%+v", err, got, want)
				}
			}
			assertHistory()
			after := e.log()
			if len(after) < len(before) || !reflect.DeepEqual(before, after[:len(before)]) {
				t.Fatal("compression changed original log events")
			}
			if got := store.FoldPoint(e.ctx(), e.uid); got != len(old) {
				t.Fatalf("fold point=%d, want first retained event=%d", got, len(old))
			}
			foldKey := fmt.Sprintf("agent:V2:fold:%d", e.uid)
			if err := e.redis.Del(e.ctx(), foldKey).Err(); err != nil {
				t.Fatal(err)
			}
			assertHistory()
			// 模拟旧缓存指向摘要本身；读取必须回退到真正未摘要的尾部。
			if err := e.redis.Set(e.ctx(), foldKey, len(before), 0).Err(); err != nil {
				t.Fatal(err)
			}
			assertHistory()

			appendEvents([]session.MemoryDTO{
				{Type: session.EventUserMessage, Role: "user", Content: "next-old " + strings.Repeat("n", 40000)},
				{Type: session.EventAssistantMessage, Role: "assistant", Content: "next answer"},
				{Type: session.EventUserMessage, Role: "user", Content: "latest question"},
				{Type: session.EventAssistantMessage, Role: "assistant", Content: strings.Repeat("l", 1600)},
			})
			if err := store.Compact(e.ctx(), e.uid, func(_ context.Context, input []string) (string, error) {
				text := strings.Join(input, "\n")
				for _, fact := range []string{"old summary", "recent-question", "recent-answer", "next-old"} {
					if !strings.Contains(text, fact) {
						t.Fatalf("rolling summary omitted %q", fact)
					}
				}
				return "rolling summary", nil
			}); err != nil {
				t.Fatal(err)
			}
			want = []*schema.Message{schema.UserMessage("【早期对话摘要】rolling summary"), schema.UserMessage("latest question"), schema.AssistantMessage(strings.Repeat("l", 1600), nil)}
			assertHistory()
		})
	}
}

// 摘要器失败时不写摘要、不移动水位，也不改变可见历史。
func TestCompactionSummaryFailureLeavesLogUnchanged(t *testing.T) {
	e := newEnv(t, 9082)
	store := session.RedisStore{}
	previousBudget := session.GetBudget()
	session.SetBudget(tokenmeter.Budget{ContextWindow: 4000, ThresholdRatio: 0.8, RetainRatio: 0.08})
	t.Cleanup(func() { session.SetBudget(previousBudget) })
	for _, text := range []string{strings.Repeat("o", 40000), strings.Repeat("r", 1600)} {
		if err := store.SaveMessage(e.ctx(), e.uid, schema.UserMessage(text)); err != nil {
			t.Fatal(err)
		}
	}
	before := e.log()
	boom := errors.New("summary unavailable")
	if err := store.Compact(e.ctx(), e.uid, func(context.Context, []string) (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("want summary error, got %v", err)
	}
	if !reflect.DeepEqual(before, e.log()) || store.FoldPoint(e.ctx(), e.uid) != 0 {
		t.Fatal("failed summary changed the log or fold point")
	}
}
