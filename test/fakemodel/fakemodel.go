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
	Reasoning string     `json:"reasoning,omitempty"`
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// AbortAfter > 0：先正常发这么多片内容，然后**把流弄坏**，模拟真实世界的
	// "用户断网 / 上游断连 / 服务重启"。这是测"流中断语义"的唯一办法 ——
	// 假模型不配合，那条路径永远测不到。
	AbortAfter int `json:"abort_after,omitempty"`
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

// Scenario 场景 = 一组规则 + 兜底回复（+ 可选的上游故障脚本）。
type Scenario struct {
	Default Reply  `json:"default"`
	Rules   []Rule `json:"rules,omitempty"`
	// Faults 上游故障脚本：按顺序在前几次请求上抛出错误，用来测重试路径。
	// 为什么需要：重试只在真出错时才走 —— 假模型不会出错，等于这条路径永远测不到。
	Faults []Fault `json:"faults,omitempty"`
}

// Fault 一次"上游故障"：返回指定的 HTTP 状态码，而不是正常的模型回复。
//
// 注意它模拟的是 SDK 眼里的"请求失败"（非 2xx），SDK 会在错误文本里带上
// "status code: 429" 这样的信息 —— internal/harness/retry 正是靠解析它判断
// 该不该重试，所以这条路是**端到端**的（不是绕过 SDK 直接喂一个 error 值）。
type Fault struct {
	Status  int    `json:"status"`            // HTTP 状态码，如 429 / 503
	Message string `json:"message,omitempty"` // 错误信息
	Times   int    `json:"times,omitempty"`   // 连续失败几次（默认 1）
}

// RequestInfo 记录一次收到的请求，供测试断言"模型到底收到了什么"。
type RequestInfo struct {
	Messages  []MessageSnapshot `json:"-"` // Test-only evidence; never exposed by the request summary endpoint.
	Seq       int               `json:"seq"`
	Stream    bool              `json:"stream"`
	NumMsgs   int               `json:"num_msgs"`
	HasTools  bool              `json:"has_tools"`
	ToolNames []string          `json:"tool_names,omitempty"`
	LastUser  string            `json:"last_user"`
	MatchedBy string            `json:"matched_by"` // 命中了哪条规则（便于排查）
}

// Server 假模型服务器：持有场景、命中计数和请求记录。
type Server struct {
	scenario Scenario

	mu    sync.Mutex
	fires map[int]int // 规则下标 → 已触发次数
	reqs  []RequestInfo
	// faultQueue 尚未抛出的故障（构造时把 Faults 按 Times 展开成队列）。
	faultQueue []Fault
}

// NewServer 构造一个假模型服务器。
func NewServer(sc Scenario) *Server {
	return &Server{scenario: sc, fires: map[int]int{}, faultQueue: expandFaults(sc.Faults)}
}

// expandFaults 把 [{429,times:2}] 展开成 [429,429]，按顺序逐个消费。
func expandFaults(faults []Fault) []Fault {
	out := make([]Fault, 0, len(faults))
	for _, f := range faults {
		times := f.Times
		if times <= 0 {
			times = 1
		}
		for i := 0; i < times; i++ {
			out = append(out, f)
		}
	}
	return out
}

// Reset 清空命中计数、请求记录与故障队列（测试之间隔离用）。
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fires = map[int]int{}
	s.reqs = nil
	s.faultQueue = expandFaults(s.scenario.Faults)
}

// takeFault 取出下一个待抛出的故障；没有则返回 ok=false。
func (s *Server) takeFault() (Fault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.faultQueue) == 0 {
		return Fault{}, false
	}
	f := s.faultQueue[0]
	s.faultQueue = s.faultQueue[1:]
	return f, true
}

// SetScenario 运行期替换场景（测试用）。连命中计数、请求记录与故障队列一起重置，
// 免得上一个场景的计数串到下一个。
func (s *Server) SetScenario(sc Scenario) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scenario = sc
	s.fires = map[int]int{}
	s.reqs = nil
	s.faultQueue = expandFaults(sc.Faults)
}

// Requests 返回收到的请求快照。
func (s *Server) Requests() []RequestInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RequestInfo, len(s.reqs))
	copy(out, s.reqs)
	for i := range out {
		out[i].Messages = append([]MessageSnapshot(nil), out[i].Messages...)
	}
	return out
}

// ---- OpenAI 请求/响应的最小子集（只解我们需要的字段）----

// chatMessageIn 请求里的一条消息（只解我们需要的字段）。
type MessageSnapshot struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatMessageIn = MessageSnapshot

type chatRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages []chatMessageIn `json:"messages"`
	Tools    []struct {
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

	promptTokens := estimateTokens(req.Messages)

	// 故障优先于正常回复：否则"前 N 次失败"的脚本会被正常回复吃掉，
	// 重试那条路永远走不到。
	if f, ok := s.takeFault(); ok {
		s.mu.Lock()
		s.reqs = append(s.reqs, RequestInfo{
			Seq: len(s.reqs) + 1, Stream: req.Stream, NumMsgs: len(req.Messages),
			HasTools: hasTools, ToolNames: toolNames, LastUser: truncate(lastUser, 60),
			MatchedBy: fmt.Sprintf("fault(HTTP %d)", f.Status), Messages: append([]MessageSnapshot(nil), req.Messages...),
		})
		seq := len(s.reqs)
		s.mu.Unlock()
		log.Printf("[fakemodel] #%d 注入上游故障: HTTP %d", seq, f.Status)
		s.writeFault(w, f)
		return
	}

	reply, matchedBy := s.pick(req, lastUser, hasTools)
	reply = substituteLocator(reply, lastSpillLocator(req))

	s.mu.Lock()
	s.reqs = append(s.reqs, RequestInfo{
		Seq: len(s.reqs) + 1, Stream: req.Stream, NumMsgs: len(req.Messages),
		HasTools: hasTools, ToolNames: toolNames, LastUser: truncate(lastUser, 60),
		MatchedBy: matchedBy, Messages: append([]MessageSnapshot(nil), req.Messages...),
	})
	seq := len(s.reqs)
	s.mu.Unlock()

	log.Printf("[fakemodel] #%d stream=%v msgs=%d tools=%d 命中=%s 回=%s",
		seq, req.Stream, len(req.Messages), len(req.Tools), matchedBy, describe(reply))

	if req.Stream {
		s.writeStream(w, reply, promptTokens)
		return
	}
	s.writeJSON(w, reply, promptTokens)
}

// estimateTokens 假模型自报的 prompt 用量。
//
// 为什么必须**自己算**而不是写死一个常数：业务侧会拿真实 usage 校准它的估算系数，
// 如果这里回一个假的固定值（比如 11），校准系数会被一路压到下限，
// 于是"上下文预算"永远不触发压缩 —— 测试反而把被测系统带沟里。
//
// 又为什么故意用一个**独立且粗糙**的算法（纯按字符数折算、约 0.6 token/字符），
// 而不是调用被测代码的 tokenmeter：那样等于"自己校准自己"，
// 偏差会被完美掩盖，校准环节就永远测不出问题。真实 tokenizer 和估算器本来就有偏差，
// 这里保留偏差才是诚实的测试替身。
func estimateTokens(msgs []chatMessageIn) int {
	runes := 0
	for _, m := range msgs {
		runes += len([]rune(m.Content))
	}
	if runes == 0 {
		return 1
	}
	return runes*3/5 + len(msgs)*3 // 约 0.6 token/字符 + 每条 3 token 结构开销
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

// writeFault 返回一个非 2xx 响应，模拟上游故障（429 限流 / 503 不可用等）。
//
// 错误体照 OpenAI 的形状写：SDK 会解析它，并在自己的错误文本里带上
// "status code: N" —— retry 包就靠这个判断该不该重试。
func (s *Server) writeFault(w http.ResponseWriter, f Fault) {
	status := f.Status
	if status == 0 {
		status = 500
	}
	msg := f.Message
	if msg == "" {
		switch status {
		case 429:
			msg = "rate limit exceeded, please retry later"
		case 503:
			msg = "service unavailable"
		default:
			msg = "upstream error"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "fake_upstream_error",
			"code":    status,
		},
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, reply Reply, promptTokens int) {
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
		"usage": usageOf(promptTokens, completionTokens(reply)),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// usageOf 组装 usage 块（prompt 用量由请求内容推算，不是写死的常数）。
func usageOf(promptTokens, completionTokens int) map[string]int {
	return map[string]int{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
}

// completionTokens 回复本身占的 token（同样按字符数粗算，和 prompt 口径一致）。
func completionTokens(reply Reply) int {
	n := len([]rune(reply.Text))
	for _, tc := range reply.ToolCalls {
		n += len([]rune(tc.Name)) + len([]rune(tc.Arguments))
	}
	if n == 0 {
		return 1
	}
	return n*3/5 + 1
}

// writeStream 按 SSE 吐流：文本切成若干片、工具调用分两次发（先名字后参数），
// 尽量贴近真模型的分片形态 —— 我们的流式处理逻辑才有被真正锻炼到。
func (s *Server) writeStream(w http.ResponseWriter, reply Reply, promptTokens int) {
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
	for _, r := range reply.Reasoning {
		send(map[string]any{"reasoning_content": string(r)}, nil)
	}

	if len(reply.ToolCalls) > 0 {
		for i, tc := range reply.ToolCalls {
			send(map[string]any{"tool_calls": []any{map[string]any{
				"index": i, "id": callID(i), "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": ""},
			}}}, nil)
			send(map[string]any{"tool_calls": []any{map[string]any{
				"index":    i,
				"function": map[string]any{"arguments": tc.Arguments},
			}}}, nil)
		}
		send(map[string]any{}, "tool_calls")
	} else {
		runes := []rune(reply.Text)
		const chunkSize = 3
		sent := 0
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			send(map[string]any{"content": string(runes[i:end])}, nil)
			sent++
			if reply.AbortAfter > 0 && sent >= reply.AbortAfter {
				abortStream(w, flusher)
				return
			}
		}
		send(map[string]any{}, "stop")
	}

	// 用量块（有些 SDK 会等它）
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-fake", "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "fakemodel",
		"choices": []any{},
		"usage":   usageOf(promptTokens, completionTokens(reply)),
	})
	fmt.Fprintf(w, "data: %s\n\n", b)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// abortStream 把流"弄坏"，让客户端拿到一个非 EOF 的错误。
//
// 为什么用"畸形数据帧"而不是直接关连接：直接关的话，SDK 有可能把
// "body 到此为止"当成本轮**正常结束**（io.EOF），那样就测不出中断路径了 ——
// 测试替身在这里必须给出**确定性**的信号，而不是一个看运气的结果。
func abortStream(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprint(w, "data: {\"broken\":\n\n") // 半截 JSON：解码必然失败
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
