// Package session 记忆器官：事件溯源式会话日志（只追加）+ 模型历史投影。
//
// 定位：harness 解剖图里的"会话日志 = 事件溯源"（HARNESS-STUDY M4）。
// 只有一个接口 Store（当前 Redis 实现 RedisStore）；换存储（如 MySQL）= 新写一个满足接口的类型，
// 上层 Harness 不用改。
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

// rdb 本器官的 Redis 客户端（由 Init 注入，main 启动时调用一次）。
var rdb *redis.Client

// Init 注入 Redis 客户端（main 启动时调用）。
func Init(client *redis.Client) {
	rdb = client
}

// MaxHistory 是"喂给模型的上限"：GetHistory 每次只取最近 20 条，防止大模型 Token 撑爆。
// 注意：它只在"读"的时候截取；存储层只增不减、永久保存，旧对话仍可被 SearchArchival 查到。
const MaxHistory = 20

// ArchiveDefaultTopK 检索默认返回条数
const ArchiveDefaultTopK = 3

// 事件类型（对应 dsh SessionEventMap 的最小剪影）。
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
	// EventCompactionSummary 压缩产生的摘要事件（log-only：记录被遮蔽的旧消息 seq 区间 + 摘要文本）
	EventCompactionSummary = "compaction/summary"
	// EventAudit 审批与真实执行的审计记录（log-only，不喂模型）。
	// 为什么要进日志：审批是"人介入"的动作，必须留下可追溯的一条——
	// 谁批的、批了什么、真实执行的结果如何。不给模型看，但必须可审计。
	EventAudit = "audit/action"
	// EventCheckpoint 折叠快照（log-only）：标记某条水位之前的事件已被压成摘要，
	// 读取时可以直接从标记处开始 LRange，不必每轮把整条日志拉回内存（对应事件溯源的 snapshot）。
	EventCheckpoint = "session/checkpoint"
	// EventCorrupt 读取侧标记：这条日志解不出来（保留占位以保证下标=seq 不错位）。
	// 只在内存里用，不会写进日志。
	EventCorrupt = "corrupt/event"
)

// ToolCallData 一次工具调用的结构化信息（持久化的精简版）。
type ToolCallData struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// MemoryDTO 持久化在日志里的一条事件（自研纯净 DTO，避开第三方结构体反序列化陷阱）。
type MemoryDTO struct {
	Type           string         `json:"type"`
	Role           string         `json:"role"`
	Content        string         `json:"content"`
	ToolCallID     string         `json:"tool_call_id,omitempty"` // tool/result 用它配对 tool/call
	ToolName       string         `json:"tool_name,omitempty"`
	ToolCalls      []ToolCallData `json:"tool_calls,omitempty"`      // tool/call 携带的调用清单
	CompactionFrom int            `json:"compaction_from,omitempty"` // compaction/summary 遮蔽的起始 seq
	CompactionTo   int            `json:"compaction_to,omitempty"`   // compaction/summary 遮蔽的结束 seq
	FoldUpto       int            `json:"fold_upto,omitempty"`       // session/checkpoint 的折叠水位：此下标（含）之前的事件已被摘要覆盖
	Time           time.Time      `json:"time"`
}

// Store 记忆器官接口：只追加日志 + 投影模型历史 + 归档检索 + 上下文压缩。
type Store interface {
	SaveMessage(ctx context.Context, userID uint, msg *schema.Message) error
	GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error)
	SearchArchival(ctx context.Context, userID uint, query string, topK int) ([]*schema.Message, error)
	Compact(ctx context.Context, userID uint, summarize CompactSummarizer) error
	// AppendEvent 追加一条自定义事件（审计/检查点这类 log-only 事件）。
	AppendEvent(ctx context.Context, userID uint, dto MemoryDTO) error
	// HasEvent 该类型的事件是否已写过（用于"系统提示只写一次"这类去重）。
	HasEvent(ctx context.Context, userID uint, eventType string) (bool, error)
}

// RedisStore 是 Store 的 Redis 实现。
type RedisStore struct{}

// SaveMessage 把一条消息追加到唯一的记忆日志，只增不减、永久保存。
func (RedisStore) SaveMessage(ctx context.Context, userID uint, msg *schema.Message) error {
	dto := MemoryDTO{
		Type:    inferEventType(msg.Role),
		Role:    string(msg.Role),
		Content: msg.Content,
		Time:    time.Now(),
	}
	return writeMemoryEvent(ctx, userID, dto)
}

// GetHistory 从日志投影出喂给模型的最近历史（pair-or-drop 规则）。
func (RedisStore) GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error) {
	dtos, base, err := RedisStore{}.loadActiveLog(ctx, userID)
	if err != nil {
		return nil, err
	}
	history := ProjectMessagesFrom(dtos, base)
	fmt.Printf(" [记忆中枢] 成功为 UserID %d 唤醒了 %d 条前世记忆！(读 %d 条事件, 自 seq %d)\n",
		userID, len(history), len(dtos), base)
	return history, nil
}

// foldKey 折叠水位指针的 key（派生数据，不是日志本体）。
func foldKey(userID uint) string { return fmt.Sprintf("agent:V2:fold:%d", userID) }

// FoldPoint 日志的折叠水位：此下标之前的事件已被摘要整体覆盖，不必再读。
//
// 为什么要它：压缩语义修对之后，"读日志"仍然是每轮全量 LRange，对话越长每轮越慢（O(总长)）。
// 事件溯源的标准解法是 snapshot —— 把已经摘要过的前缀折叠掉，读取只从最新快照开始。
// 这里的最新快照就是最新那条 compaction/summary（滚动摘要自己代表了它之前的全部历史），
// 所以折叠水位 = 最新摘要的下标 = 最后一条 checkpoint 事件的 FoldUpto + 1。
//
// 快速路径读指针键（O(1)）；指针丢了就从日志里的 checkpoint 事件重建一次（O(n)，只发生一次），
// 重建不出来就返回 0 —— 等于全量读，语义不变、只是慢。
func (RedisStore) FoldPoint(ctx context.Context, userID uint) int {
	if rdb == nil {
		return 0
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	total, err := rdb.LLen(ctx, key).Result()
	if err != nil || total == 0 {
		return 0
	}
	if base, err := rdb.Get(ctx, foldKey(userID)).Int(); err == nil && base > 0 && int64(base) < total {
		return base
	}
	// 慢路径：从日志尾部往前找最后一条 checkpoint
	dataList, err := rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return 0
	}
	for i := len(dataList) - 1; i >= 0; i-- {
		var dto MemoryDTO
		if json.Unmarshal([]byte(dataList[i]), &dto) != nil {
			continue
		}
		if dto.Type == EventCheckpoint {
			base := dto.FoldUpto + 1
			setFoldPoint(ctx, userID, base)
			return base
		}
	}
	return 0
}

// setFoldPoint 写折叠水位（派生数据，写失败不影响正确性，下次压缩会再写一遍）。
func setFoldPoint(ctx context.Context, userID uint, idx int) {
	if rdb == nil || idx <= 0 {
		return
	}
	rdb.Set(ctx, foldKey(userID), idx, 0)
}

// loadActiveLog 读出日志的"活跃尾部"：从折叠水位开始的事件 + 它们的真实起始下标。
//
// 起始下标必须一起带出去：日志下标就是 seq，投影/压缩的遮蔽区间全靠它对齐，
// 只读尾部却不带偏移，所有区间都会错位（模型会看到本该被摘要顶掉的旧消息）。
func (RedisStore) loadActiveLog(ctx context.Context, userID uint) ([]MemoryDTO, int, error) {
	if rdb == nil {
		return nil, 0, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	base := RedisStore{}.FoldPoint(ctx, userID)
	dataList, err := rdb.LRange(ctx, key, int64(base), -1).Result()
	if err != nil {
		return nil, 0, err
	}
	dtos := make([]MemoryDTO, 0, len(dataList))
	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			fmt.Printf(" [记忆中枢] 破译记忆碎片失败: %v\n", err)
			// 坏数据也要占一个位置：下标就是 seq，跳过会让后面所有事件错位。
			dto = MemoryDTO{Type: EventCorrupt}
		}
		dtos = append(dtos, dto)
	}
	return dtos, base, nil
}

// SearchArchival 在完整日志里做词交集检索，取分数最高的 topK 条（滑动窗口外的旧记忆）。
func (RedisStore) SearchArchival(ctx context.Context, userID uint, query string, topK int) ([]*schema.Message, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	dataList, err := rdb.LRange(ctx, key, 0, -1).Result()
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

// inferEventType 把 Eino 的消息角色映射成事件类型（老数据没 type 字段时按 Role 兜底推断）。
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

// AppendEvent 追加一条自定义事件（审计/检查点这类 log-only 事件）。
// 走的是和别的写入完全相同的唯一写入口，保证"日志只追加"这条不变量不被绕开。
func (RedisStore) AppendEvent(ctx context.Context, userID uint, dto MemoryDTO) error {
	if dto.Time.IsZero() {
		dto.Time = time.Now()
	}
	return writeMemoryEvent(ctx, userID, dto)
}

// HasEvent 判断该类型的事件是否已经在日志里出现过。
// 用途：系统提示这类每轮都一样、又只是 log-only 的事件，只写一次就够了。
func (RedisStore) HasEvent(ctx context.Context, userID uint, eventType string) (bool, error) {
	if rdb == nil {
		return false, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	dataList, err := rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return false, err
	}
	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			continue
		}
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		if typ == eventType {
			return true, nil
		}
	}
	return false, nil
}

// writeMemoryEvent 把一条记忆事件序列化后 RPush 进记忆日志（唯一的写入口）。
func writeMemoryEvent(ctx context.Context, userID uint, dto MemoryDTO) error {
	if rdb == nil {
		fmt.Println(" [记忆中枢] 致命错误：Redis 连接池未挂载")
		return fmt.Errorf("redis client is nil")
	}
	data, err := json.Marshal(dto)
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆序列化崩溃: %v\n", err)
		return err
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	_, err = rdb.RPush(ctx, key, data).Result()
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆落盘失败: %v\n", err)
	} else {
		fmt.Printf(" [记忆中枢] 成功刻录 1 条新记忆! (Type: %s, Role: %s)\n", dto.Type, dto.Role)
	}
	return err
}

// WriteMemoryEvents 按顺序写入一批记忆事件（供 agent 的 MessageModifier 钩子落盘工具调用用）。
// 保证顺序：tool/call 必须排在它对应的 tool/result 前面。
func WriteMemoryEvents(ctx context.Context, userID uint, pending []MemoryDTO) {
	if rdb == nil || len(pending) == 0 {
		return
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	pipe := rdb.Pipeline()
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

// CompactSummarizer 把一段早期消息压成摘要（由编排层注入，通常用模型实现）。
type CompactSummarizer func(ctx context.Context, messages []string) (string, error)

// Compact 上下文压缩（对应 HARNESS-STUDY M7）：把 MaxHistory 窗口之外的早期对话压成一条摘要，
// 写一条 log-only 的 compaction/summary 事件；投影时被遮蔽的旧事件不再喂给模型。
// 没有可压缩的早期消息时是空操作（不调 summarize）。
//
// 两条不变量（缺一就会退化成"每轮从头重摘一遍"）：
//  1. 单调：只压"上一条摘要 CompactionTo 之后"的新消息（high-water mark），已摘要过的绝不再摘。
//  2. 单一滚动摘要：新摘要 = 旧摘要 + 新增溢出消息，重写成一条总摘要，
//     且 CompactionFrom 恒为 0 —— 旧摘要自身也是一个日志下标，会落进 [0,to] 被遮蔽，
//     于是投影里永远只剩一条摘要，不会出现 N 条互相重叠的摘要同时喂给模型。
//
// 注意：压缩是 best-effort——摘要失败就跳过本次，不影响正常对话。
func (RedisStore) Compact(ctx context.Context, userID uint, summarize CompactSummarizer) error {
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	// 只读"活跃尾部"（折叠水位之后）——折叠就是事件溯源的 snapshot：
	// 最新摘要自己代表了它之前的一切，之前的事件不必再拉回内存。
	dtos, base, err := RedisStore{}.loadActiveLog(ctx, userID)
	if err != nil {
		return err
	}

	// 第一遍：先找出"已经压到哪"和"当前摘要是什么"。
	// 必须独立成一遍 —— 摘要事件排在它遮蔽的消息之后（日志只追加），
	// 边扫边收的话，扫到早期消息时还没看见摘要，high-water mark 就是空的。
	lastTo := -1      // 已经压缩到哪个 seq（high-water mark）
	prevSummary := "" // 当前生效的摘要正文，用来续写而不是重头再来
	for _, dto := range dtos {
		if dto.Type != EventCompactionSummary {
			continue
		}
		if dto.CompactionTo > lastTo {
			lastTo = dto.CompactionTo
		}
		prevSummary = dto.Content // 按顺序扫，最后一条就是最新的
	}

	// 第二遍：只收 high-water mark 之后的消息（seq = 折叠水位 + 数组下标）
	type msg struct {
		idx     int
		content string
	}
	var msgs []msg
	for i, dto := range dtos {
		seq := base + i
		if seq <= lastTo {
			continue // 已被现有摘要覆盖，跳过（否则每轮都会把早期对话重摘一遍）
		}
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		switch typ {
		case EventUserMessage, EventAssistantMessage:
			msgs = append(msgs, msg{seq, dto.Content})
		}
	}
	// 生效的摘要恒为 1 条（新摘要取代旧摘要），所以窗口只给它留一个名额。
	// 注意这里不能用"摘要条数"去减：日志里的历史摘要是只追加的、不会被删，
	// 拿它当分母会让窗口越缩越小，最后变成每轮都压缩。
	keepWindow := MaxHistory - 1
	if len(msgs) <= keepWindow {
		return nil // 没超窗，不用压缩
	}
	toCompress := msgs[:len(msgs)-keepWindow]
	// 摘要输入 = 旧摘要（如果有）+ 本轮新增的溢出消息，保证新摘要覆盖全部早期对话
	contents := make([]string, 0, len(toCompress)+1)
	if prevSummary != "" {
		contents = append(contents, "【已有摘要，请在此基础上续写，不要丢失已有信息】\n"+prevSummary)
	}
	for _, m := range toCompress {
		contents = append(contents, m.content)
	}
	summary, err := summarize(ctx, contents)
	if err != nil {
		return fmt.Errorf("压缩摘要失败: %v", err)
	}
	dto := MemoryDTO{
		Type:    EventCompactionSummary,
		Role:    "assistant",
		Content: summary,
		// from 恒为 0：这条摘要代表"到目前为止的全部早期对话"，
		// 旧摘要的下标也在这段区间里，会被投影自动遮蔽。
		CompactionFrom: 0,
		CompactionTo:   toCompress[len(toCompress)-1].idx,
		Time:           time.Now(),
	}
	if err := writeMemoryEvent(ctx, userID, dto); err != nil {
		return err
	}

	// 折叠：把读取水位推到刚写的这条摘要上，并落一条 checkpoint 事件把它记在日志里
	// （日志仍然只追加、一条不删；"折叠到哪"本身也可审计、可在指针丢失时重建）。
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	if n, err := rdb.LLen(ctx, key).Result(); err == nil && n > 0 {
		summarySeq := int(n - 1) // 刚写的摘要的 seq
		setFoldPoint(ctx, userID, summarySeq)
		if err := writeMemoryEvent(ctx, userID, MemoryDTO{
			Type: EventCheckpoint,
			Role: "system",
			// FoldUpto 指向摘要的"前一个"，这样投影预扫描不会把摘要自己也遮蔽掉
			Content:  fmt.Sprintf("折叠水位推进到 seq %d：此前事件由 seq %d 的摘要代表", summarySeq, summarySeq),
			FoldUpto: summarySeq - 1,
			Time:     time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ProjectMessages 把一组记忆事件投影成喂给模型的 []*schema.Message（纯函数，不依赖 Redis）。
// 投影规则（pair-or-drop）：见本文件顶部事件类型注释。
// 最后只保留最近 MaxHistory 条真消息；裁剪切在配对中间时，丢弃开头的悬空 tool 消息。
func ProjectMessages(dtos []MemoryDTO) []*schema.Message {
	return ProjectMessagesFrom(dtos, 0)
}

// ProjectMessagesFrom 同上，但显式给出 dtos[0] 在日志里的真实 seq。
//
// 折叠（snapshot）之后只读日志尾部，数组下标就不再等于 seq 了 ——
// 而遮蔽区间（CompactionFrom/To、FoldUpto）记的都是真实 seq，
// 不把 baseSeq 加回来的话所有区间都会错位。
func ProjectMessagesFrom(dtos []MemoryDTO, baseSeq int) []*schema.Message {
	// 预扫描压缩摘要：
	//  1. 被遮蔽的旧消息下标直接跳过（压缩后日志只追加、不删旧事件）
	//  2. 摘要之间是"后者取代前者"的滚动语义：只有覆盖范围最大的那条进投影。
	//     不能靠下标遮蔽来做到这点 —— 旧摘要自己排在它遮蔽的消息之后，
	//     新摘要的 [0,to] 根本盖不到它的下标，于是 N 条内容重叠的摘要会一起
	//     喂给模型，压缩反而把上下文喂胖了。
	shadowed := map[int]bool{}
	activeSummary := -1
	for i := range dtos {
		if dtos[i].Type == EventCheckpoint {
			// 折叠水位：这个下标之前的事件已经被摘要覆盖了，跳过即可（日志本身不动）
			for j := 0; j <= dtos[i].FoldUpto; j++ {
				shadowed[j] = true
			}
			continue
		}
		if dtos[i].Type != EventCompactionSummary {
			continue
		}
		for j := dtos[i].CompactionFrom; j <= dtos[i].CompactionTo; j++ {
			shadowed[j] = true
		}
		if activeSummary == -1 || dtos[i].CompactionTo >= dtos[activeSummary].CompactionTo {
			activeSummary = i // 注意：这里是数组下标，不是 seq
		}
	}

	history := make([]*schema.Message, 0, len(dtos))

	var pending *MemoryDTO
	open := map[string]bool{}
	var pendingResults []*schema.Message
	flushPair := func() {
		if pending == nil || len(open) > 0 {
			return
		}
		history = append(history, assistantMsgFromToolCall(pending))
		history = append(history, pendingResults...)
		pending, open, pendingResults = nil, map[string]bool{}, nil
	}

	for i := range dtos {
		if shadowed[baseSeq+i] {
			continue // 被压缩/折叠遮蔽的旧事件，不喂模型
		}
		dto := dtos[i]
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		switch typ {
		case EventCompactionSummary:
			if i != activeSummary {
				continue // 被更新的摘要取代了，不进投影
			}
			flushPair()
			// 摘要以"用户消息"的形式进投影（模型能看到早期对话的梗概）
			history = append(history, &schema.Message{
				Role:    schema.User,
				Content: "【早期对话摘要】" + dto.Content,
			})
		case EventSystemPrompt, EventAudit, EventCheckpoint:
			continue // log-only：只存档/审计用，永远不喂模型
		case EventToolCall:
			flushPair()
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
				flushPair()
			}
		default:
			flushPair()
			history = append(history, &schema.Message{
				Role:    schema.RoleType(dto.Role),
				Content: dto.Content,
			})
		}
	}
	flushPair()

	if len(history) > MaxHistory {
		history = history[len(history)-MaxHistory:]
		for len(history) > 0 && history[0].Role == schema.Tool {
			history = history[1:]
		}
	}
	return history
}

// assistantMsgFromToolCall 把一个 tool/call 事件还原成"带 tool_calls 的 assistant 消息"。
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

// tokenize 极简分词：转小写后按非字母数字字符切分，过滤空 token。
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
