package ai_service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MaxHistory 是"喂给模型的上限"：GetHistory 每次只取最近 20 条，防止大模型 Token 撑爆。
// 注意：它只在"读"的时候截取；存储层只增不减、永久保存，旧对话仍可被 SearchArchival 查到。
const MaxHistory = 20

// ArchiveDefaultTopK 检索默认返回条数
const ArchiveDefaultTopK = 3

// 事件类型（对应 dsh SessionEventMap 的最小剪影，见 HARNESS-STUDY.md M4）。
// 进"投影"（GetHistory 喂给模型的历史）的规则：
//   - user/message、assistant/message：直接进；
//   - tool/call + tool/result：成对（靠 callId 配对）才进——模型侧协议要求工具结果必须跟在
//     带 tool_calls 的 assistant 消息后面，悬空的结果/调用都会被丢弃（pair-or-drop）；
//   - system/prompt：log-only，不喂。
const (
	// EventUserMessage 用户说的话（surface，喂模型）
	EventUserMessage = "user/message"
	// EventAssistantMessage 模型组装好的完整回复（surface，喂模型）
	EventAssistantMessage = "assistant/message"
	// EventSystemPrompt 系统提示词（log-only，不喂模型；对应 dsh 的 request/header）
	EventSystemPrompt = "system/prompt"
	// EventToolCall 模型发起的一次工具调用请求（带 callId，配对的另一半是 tool/result）
	EventToolCall = "tool/call"
	// EventToolResult 一次工具执行的结果（带 callId，和 EventToolCall 配对后才进投影）
	EventToolResult = "tool/result"
)

// ToolCallData 一次工具调用的结构化信息（持久化的精简版，对应 dsh 的 tool/call 词汇）。
type ToolCallData struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type MemoryDTO struct {
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"` // tool/result 用它配对 tool/call
	ToolName   string         `json:"tool_name,omitempty"`
	ToolCalls  []ToolCallData `json:"tool_calls,omitempty"` // tool/call 携带的调用清单
	Time       time.Time      `json:"time"`
}

// inferEventType 把 Eino 的消息角色映射成事件类型。
// 老数据没有 type 字段（Type==""），读取时用它按 Role 兜底推断，保证新旧记录兼容。
func inferEventType(role schema.RoleType) string {
	switch string(role) {
	case "user":
		return EventUserMessage
	case "assistant":
		return EventAssistantMessage
	case "system":
		return EventSystemPrompt
	case "tool":
		return EventToolResult
	default:
		return string(role)
	}
}

// SaveMessage 把一条消息追加到唯一的记忆日志（agent:V2:history），只增不减、永久保存。
// 喂给模型时只取最近 MaxHistory 条（见 GetHistory），存储本身不裁剪，所以旧记忆也能被检索到。
func SaveMessage(ctx context.Context, userID uint, msg *schema.Message) error {
	// 1. 降维抽离：把 Eino 的复杂对象，降级为我们自己的纯净 DTO（同时打上事件类型）
	dto := MemoryDTO{
		Type:    inferEventType(msg.Role),
		Role:    string(msg.Role),
		Content: msg.Content,
		Time:    time.Now(),
	}
	return writeMemoryEvent(ctx, userID, dto)
}

// writeMemoryEvent 把一条记忆事件序列化后 RPush 进记忆日志（唯一的写入口）。
// 永久保存：不 LTrim、不设 Expire——旧对话要能被 SearchArchival 一直查到。
// （喂给模型的上限由 GetHistory 取最近 MaxHistory 条控制，与这里存多久无关。）
func writeMemoryEvent(ctx context.Context, userID uint, dto MemoryDTO) error {
	if Rdb == nil {
		fmt.Println(" [记忆中枢] 致命错误：Redis 连接池未挂载")
		return fmt.Errorf("redis client is nil")
	}
	data, err := json.Marshal(dto)
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆序列化崩溃: %v\n", err)
		return err
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	_, err = Rdb.RPush(ctx, key, data).Result()
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆落盘失败: %v\n", err)
	} else {
		// 监听落地回声
		fmt.Printf(" [记忆中枢] 成功刻录 1 条新记忆! (Type: %s, Role: %s)\n", dto.Type, dto.Role)
	}
	return err
}

// writeMemoryEvents 按顺序写入一批记忆事件。
// 专门给工具调用落盘用：tool/call 必须排在它对应的 tool/result 前面，
// 所以由调用方保证 pending 里的顺序，这里用同一条管道一次写完，避免并发 goroutine 打乱顺序。
func writeMemoryEvents(ctx context.Context, userID uint, pending []MemoryDTO) {
	if Rdb == nil || len(pending) == 0 {
		return
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	pipe := Rdb.Pipeline()
	for _, dto := range pending {
		data, err := json.Marshal(dto)
		if err != nil {
			fmt.Printf(" [记忆中枢] 记忆序列化崩溃: %v\n", err)
			continue
		}
		pipe.RPush(ctx, key, data)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		fmt.Printf(" [记忆中枢] 工具调用落盘失败: %v\n", err)
		return
	}
	fmt.Printf(" [记忆中枢] 工具调用已刻录 %d 条 (含 tool/call + tool/result 配对)\n", len(pending))
}

// GetHistory 取出最近 MaxHistory 条消息，喂给模型当上下文。
// 这就是"投影"（对应 dsh 的 deriveMessages）：从事件日志里挑出该给模型看的那部分。
// 投影规则（pair-or-drop）：
//   - user/message、assistant/message：直接投影；
//   - tool/call 必须等它所有 tool/result 到齐，才把 assistant(带tool_calls)+全部 tool(结果)
//     一起喂出（模型侧协议要求 tool 消息必须紧跟在带 tool_calls 的 assistant 消息后面）；
//   - 只出现一半的（有 call 没 result，或悬空的 result）整对丢弃，绝不喂悬空消息。
func GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)

	// 全量读出来：按投影规则生成模型历史，再取最后 MaxHistory 条真消息（顺序不能反：先筛后切）
	dataList, err := Rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	var history []*schema.Message

	// —— 工具配对的投影状态 ——
	var pending *MemoryDTO               // 正在等待配对结果的 tool/call
	open := map[string]bool{}            // 这个 call 里还没等到结果的 callId
	var pendingResults []*schema.Message // 已到齐的工具结果（按到达顺序）
	flushPair := func() {
		if pending == nil || len(open) > 0 {
			return // 没配完就作废，整对丢弃
		}
		// 先喂 assistant(带 tool_calls)，再按到达顺序喂它所有的 tool(结果)
		history = append(history, assistantMsgFromToolCall(pending))
		history = append(history, pendingResults...)
		pending, open, pendingResults = nil, map[string]bool{}, nil
	}

	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			fmt.Printf(" [记忆中枢] 破译记忆碎片失败: %v\n", err)
			continue
		}
		// 老记录没有 type 字段，按 Role 兜底推断，保证兼容
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		switch typ {
		case EventSystemPrompt:
			continue // log-only：只记不喂
		case EventToolCall:
			flushPair() // 上一个 call 还没配完又来新的 → 旧的作废
			pending = &dto
			open = map[string]bool{}
			for _, tc := range dto.ToolCalls {
				open[tc.ID] = true
			}
			pendingResults = nil
		case EventToolResult:
			if pending == nil || !open[dto.ToolCallID] {
				continue // 悬空结果：没有配对的 call，丢弃
			}
			delete(open, dto.ToolCallID)
			pendingResults = append(pendingResults, toolMsgFromToolResult(&dto))
			if len(open) == 0 {
				flushPair() // 这个 call 的所有结果都到齐，整对喂出
			}
		default:
			flushPair() // 正常消息打断了未配完的对 → 作废
			history = append(history, &schema.Message{
				Role:    schema.RoleType(dto.Role),
				Content: dto.Content,
			})
		}
	}
	flushPair() // 日志末尾还悬着的调用也作废

	// 筛完，再取最后 MaxHistory 条真消息
	if len(history) > MaxHistory {
		history = history[len(history)-MaxHistory:]
		// 裁剪可能切在配对中间：开头若是悬空的 tool 消息，丢掉（不能喂 provider）
		for len(history) > 0 && history[0].Role == schema.Tool {
			history = history[1:]
		}
	}
	// 战果汇报
	fmt.Printf(" [记忆中枢] 成功为 UserID %d 唤醒了 %d 条前世记忆！\n", userID, len(history))
	return history, nil
}

// assistantMsgFromToolCall 把一个 tool/call 事件还原成"带 tool_calls 的 assistant 消息"。
// 模型侧协议要求：一次工具调用 = assistant(声明我要调) + tool(结果)，这个还原就是前半段。
func assistantMsgFromToolCall(dto *MemoryDTO) *schema.Message {
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: dto.Content,
	}
	for _, tc := range dto.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
	}
	return msg
}

// toolMsgFromToolResult 把一个 tool/result 事件还原成"工具结果消息"（带 ToolCallID 配对）。
func toolMsgFromToolResult(dto *MemoryDTO) *schema.Message {
	return &schema.Message{
		Role:       schema.Tool,
		Content:    dto.Content,
		ToolCallID: dto.ToolCallID,
	}
}

// tokenize 极简分词：转小写后按非字母数字字符切分，过滤空 token
func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x4e00 && r <= 0x9fff)
	}) {
		if field != "" {
			tokens[field] = true
		}
	}
	return tokens
}

// SearchArchival 在整份记忆日志（就是唯一那份 history 列表）里做词交集检索，取分数最高的 topK 条。
// 思路对应 Phase 14 · 07 课 ArchivalStore.search() 的 Jaccard-like 打分，不引入向量库
func SearchArchival(ctx context.Context, userID uint, query string, topK int) ([]*schema.Message, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)

	dataList, err := Rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil, nil
	}

	type scoredMsg struct {
		msg   *schema.Message
		score int
	}
	var scored []scoredMsg
	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			continue
		}
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		if typ == EventSystemPrompt || typ == EventToolCall || typ == EventToolResult {
			continue // log-only 或工具流水事件：检索的也是"喂过模型的记忆"，不含日志噪声
		}
		msgTokens := tokenize(dto.Content)
		score := 0
		for t := range queryTokens {
			if msgTokens[t] {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredMsg{
				msg:   &schema.Message{Role: schema.RoleType(dto.Role), Content: dto.Content},
				score: score,
			})
		}
	}

	// 按分数降序排序（简单冒泡即可，历史消息量级不大）
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	if topK <= 0 {
		topK = ArchiveDefaultTopK
	}
	if len(scored) > topK {
		scored = scored[:topK]
	}

	results := make([]*schema.Message, 0, len(scored))
	for _, s := range scored {
		results = append(results, s.msg)
	}
	fmt.Printf(" [归档检索] UserID %d 查询 %q 命中 %d 条\n", userID, query, len(results))
	return results, nil
}
