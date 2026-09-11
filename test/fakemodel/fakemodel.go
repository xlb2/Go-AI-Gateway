// Package fakemodel 假模型：一个 OpenAI 兼容的本地替身，用来把"依赖真模型的端到端测试"
// 变成秒级、确定性的测试（对应 HARNESS-TODO.md 的 P0-1）。
//
// 为什么需要它：真模型 + 真网络 + 真时间的回归一次要 10 分钟，代价是没人敢改这个系统。
// 有了它，压缩、折叠、审批、工具流水线全都能在 1 秒内反复验证。
//
// 起服务：`go run ./cmd/fakemodel`（默认 127.0.0.1:9099），然后把 .env 指过来：
//
//	VOLC_BASE_URL=http://127.0.0.1:9099/api/v3
//
// **业务代码一行不用改。** 测试里则直接用 httptest.NewServer(fakemodel.NewServer(sc).Handler())。
// 场景文件格式见 test/fakemodel/scenarios/default.json。
package fakemodel

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ToolCall 假模型要发起的工具调用。
type ToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Reply 一次回复。
type Reply struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Rule 一条匹配规则：请求里最后一条 user 消息包含 Match 时用这个 Reply。
//
// MaxFires 默认 1 —— 这很重要：模型回了 tool_calls 之后，下一轮请求会带着工具结果再来，
// 如果规则无限触发就会死循环。限制触发次数就自然得到"调一次工具、然后收尾"的确定性行为。
type Rule struct {
	Match    string `json:"match"`
	Reply    Reply  `json:"reply"`
	MaxFires int    `json:"max_fires,omitempty"`
}

// Scenario 场景 = 一组规则 + 兜底回复。
type Scenario struct {
	Default Reply  `json:"default"`
	Rules   []Rule `json:"rules,omitempty"`
}

// RequestInfo 记录一次收到的请求，供测试断言"模型到底收到了什么"。
type RequestInfo struct {
	Seq       int      `json:"seq"`
	Stream    bool     `json:"stream"`
	NumMsgs   int      `json:"num_msgs"`
	HasTools  bool     `json:"has_tools"`
	ToolNames []string `json:"tool_names,omitempty"`
	LastUser  string   `json:"last_user"`
	MatchedBy string   `json:"matched_by"` // 命中了哪条规则（便于排查）
}

// Server 假模型服务器：持有场景、命中计数和请求记录。
type Server struct {
	scenario Scenario

	mu    sync.Mutex
	fires map[int]int // 规则下标 → 已触发次数
	reqs  []RequestInfo
}

// NewServer 构造一个假模型服务器。
func NewServer(sc Scenario) *Server {
	return &Server{scenario: sc, fires: map[int]int{}}
}

// Reset 清空命中计数与请求记录（测试之间隔离用）。
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fires = map[int]int{}
	s.reqs = nil
}

// Requests 返回收到的请求快照。
func (s *Server) Requests() []RequestInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RequestInfo, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// ---- OpenAI 请求/响应的最小子集（只解我们需要的字段）----

type chatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

type chatMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []apiToolCallIn `json:"tool_calls,omitempty"`
}

type apiToolCallIn struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Handler 返回可挂到 httptest.NewServer 或 http.ListenAndServe 的处理器。
// 路径不敏感：任何以 /chat/completions 结尾的路径都接受（不同 SDK 拼 base 的方式不一样）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		s.handleChat(w, r)
	})
	return mux
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}

	// 最后一句话来自谁（规则按它匹配）
	lastUser := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUser = req.Messages[i].Content
			break
		}
	}

	hasTools := len(req.Tools) > 0
	toolNames := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		toolNames = append(toolNames, t.Function.Name)
	}

	reply, matchedBy := s.pick(req, lastUser, hasTools)
	reply = substituteLocator(reply, lastSpillLocator(req))

	s.mu.Lock()
	s.reqs = append(s.reqs, RequestInfo{
		Seq: len(s.reqs) + 1, Stream: req.Stream, NumMsgs: len(req.Messages),
		HasTools: hasTools, ToolNames: toolNames, LastUser: truncate(lastUser, 60),
		MatchedBy: matchedBy,
	})
	seq := len(s.reqs)
	s.mu.Unlock()

	log.Printf("[fakemodel] #%d stream=%v msgs=%d tools=%d 命中=%s 回=%s",
		seq, req.Stream, len(req.Messages), len(req.Tools), matchedBy, describe(reply))

	if req.Stream {
		s.writeStream(w, reply)
		return
	}
	s.writeJSON(w, reply)
}

// pick 选一条规则。
//
// 三条关键规则，缺一条场景就会"看起来在跑、其实全是空转"：
//
//  1. **最后一条消息是 tool 结果时一律回兜底文本** —— 真模型拿到工具结果后是去组织答案，
//     不会立刻再调一次同样的工具。少了这条，规则会无限触发同一个工具调用（死循环）。
//  2. **请求里没有 tools 时一律回兜底文本** —— 压缩器（Summarize）用同一份凭证但没带工具，
//     拿它去匹配"愤怒"规则就会把摘要请求变成一次防御工具调用。这类交叉污染很隐蔽。
//  3. 规则按顺序、首个命中即用。
func (s *Server) pick(req chatRequest, lastUser string, hasTools bool) (Reply, string) {
	if !hasTools {
		return s.scenario.Default, "default(no-tools)"
	}
	if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool" {
		return afterToolResult(req), "default(after-tool-result)"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, rule := range s.scenario.Rules {
		if rule.Match == "" || !strings.Contains(lastUser, rule.Match) {
			continue
		}
		limit := rule.MaxFires
		if limit == 0 {
			limit = 1
		}
		if s.fires[i] >= limit {
			continue // 已经触发过，落到下一条/兜底
		}
		s.fires[i]++
		return rule.Reply, fmt.Sprintf("rule#%d(%s)", i, rule.Match)
	}
	return s.scenario.Default, "default"
}

// afterToolResult 造一个"已经读过工具结果"的回复。
//
// 为什么不能直接回兜底文案：真模型拿到工具结果后，会**把结果组织进答案**。
// 假模型如果永远只回"收到。"，那从外部看到的输出里就完全体现不出工具跑没跑，
// 分不清"工具真的执行了"和"压根没调" —— 这种"看着在跑其实空转"最坑人。
// 这里把最后一条工具结果的内容带出来，让链路是可见的。
func afterToolResult(req chatRequest) Reply {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "tool" {
			return Reply{Text: "（据工具结果）" + truncate(req.Messages[i].Content, 240)}
		}
	}
	return Reply{Text: "收到。"}
}

// spillLocatorRe 抓日志里出现过的溢出定位符（spill:<uid>:<hex>）。
var spillLocatorRe = regexp.MustCompile(`spill:\d+:[0-9a-f]+`)

// lastSpillLocator 从整段对话里找出最近出现过的定位符。
//
// 为什么需要：`load_large_content` 的参数（定位符）是**上一轮工具生成的**，
// 静态场景文件没法预先写死。真模型是"看着上下文把值抄过来"的，
// 假模型要能在这一点上像它，否则"存取往返"这条路永远测不了。
func lastSpillLocator(req chatRequest) string {
	last := ""
	for _, m := range req.Messages {
		if found := spillLocatorRe.FindAllString(m.Content, -1); len(found) > 0 {
			last = found[len(found)-1]
		}
	}
	return last
}

// substituteLocator 把场景里的 {{last_locator}} 占位符换成真实定位符。
func substituteLocator(r Reply, locator string) Reply {
	if locator == "" || len(r.ToolCalls) == 0 {
		return r
	}
	out := r
	out.ToolCalls = make([]ToolCall, len(r.ToolCalls))
	copy(out.ToolCalls, r.ToolCalls)
	for i := range out.ToolCalls {
		out.ToolCalls[i].Arguments = strings.ReplaceAll(out.ToolCalls[i].Arguments, "{{last_locator}}", locator)
	}
	return out
}

func (s *Server) writeJSON(w http.ResponseWriter, reply Reply) {
	msg := chatMessage{Role: "assistant", Content: reply.Text}
	finish := "stop"
	if len(reply.ToolCalls) > 0 {
		finish = "tool_calls"
		msg.ToolCalls = toAPIToolCalls(reply.ToolCalls)
	}
	resp := map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "fakemodel",
		"choices": []any{map[string]any{
			"index": 0, "message": msg, "finish_reason": finish,
		}},
		"usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeStream 按 SSE 吐流：文本切成若干片、工具调用分两次发（先名字后参数），
// 尽量贴近真模型的分片形态 —— 我们的流式处理逻辑才有被真正锻炼到。
func (s *Server) writeStream(w http.ResponseWriter, reply Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	send := func(delta map[string]any, finish any) {
		chunk := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": "fakemodel",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	send(map[string]any{"role": "assistant"}, nil)

	if len(reply.ToolCalls) > 0 {
		for i, tc := range reply.ToolCalls {
			send(map[string]any{"tool_calls": []any{map[string]any{
				"index": i, "id": callID(i), "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": ""},
			}}}, nil)
			send(map[string]any{"tool_calls": []any{map[string]any{
				"index": i,
				"function": map[string]any{"arguments": tc.Arguments},
			}}}, nil)
		}
		send(map[string]any{}, "tool_calls")
	} else {
		runes := []rune(reply.Text)
		const chunkSize = 3
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			send(map[string]any{"content": string(runes[i:end])}, nil)
		}
		send(map[string]any{}, "stop")
	}

	// 用量块（有些 SDK 会等它）
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-fake", "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "fakemodel",
		"choices": []any{},
		"usage":   map[string]int{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	})
	fmt.Fprintf(w, "data: %s\n\n", b)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func toAPIToolCalls(tcs []ToolCall) []apiToolCallIn {
	out := make([]apiToolCallIn, 0, len(tcs))
	for i, tc := range tcs {
		var a apiToolCallIn
		a.ID = callID(i)
		a.Type = "function"
		a.Function.Name = tc.Name
		a.Function.Arguments = tc.Arguments
		out = append(out, a)
	}
	return out
}

func callID(i int) string { return fmt.Sprintf("call_fake_%d", i) }

func describe(r Reply) string {
	if len(r.ToolCalls) > 0 {
		names := make([]string, 0, len(r.ToolCalls))
		for _, tc := range r.ToolCalls {
			names = append(names, tc.Name)
		}
		return "tool_calls[" + strings.Join(names, ",") + "]"
	}
	return fmt.Sprintf("text(%d字)", len([]rune(r.Text)))
}

func truncate(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
