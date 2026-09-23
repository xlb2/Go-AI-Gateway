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
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness/runstate"
	"go_im_gateway/internal/harness/tokenmeter"
)

// rdb 本器官的 Redis 客户端（由 Init 注入，main 启动时调用一次）。
var rdb *redis.Client

// Init 注入 Redis 客户端（main 启动时调用）。
func Init(client *redis.Client) {
	rdb = client
}

// budget 上下文预算：什么时候该压缩、压缩后保留多长。
//
// 以前这里是 `const MaxHistory = 20` —— 按**消息条数**判窗口。那是错的：
// 20 条"收到"和 20 条千字长文差两个数量级，条数一样但在上下文里差 100 倍。
// 现在一律按 token 算（tokenmeter），条数不再参与判断。
var budget = tokenmeter.DefaultBudget()

// SetBudget 注入上下文预算（main 启动时按环境变量调一次）。
func SetBudget(b tokenmeter.Budget) {
	budget = b.Sanitized()
}

// GetBudget 读当前预算（日志/排查用）。
func GetBudget() tokenmeter.Budget { return budget }

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
	// EventUsage 模型返回的真实 token 用量（log-only，不喂模型）。
	// 为什么要进日志：它是"预算判断"的唯一真实依据（估算只是估算），
	// 落盘之后可以回溯每次调用的实际成本、也可以据此核对估算系数是否漂了。
	EventUsage = "usage/report"
	// EventCheckpoint 折叠快照（log-only）：标记某条水位之前的事件已被压成摘要，
	// 读取时可以直接从标记处开始 LRange，不必每轮把整条日志拉回内存（对应事件溯源的 snapshot）。
	EventCheckpoint = "session/checkpoint"
	// EventTurnEnd 一轮对话的收尾标记（log-only，不喂模型）。
	// 正常收尾由编排层写入；崩溃/中断留下的"开放轮次"由 Repair 补一条合成收尾。
	// 为什么必须有它：没有它的话，"用户说了话但没有回复"这一格
	// 分不清是"模型没回"还是"进程死在半路"—— 这两件事的处置完全不同。
	EventTurnEnd = "turn/end"
	// EventStepStart / EventStepEnd 一个 step 的开始与结束（log-only，不喂模型）。
	// 为什么要它们：step 是计量 / 中断 / 断点续跑的最小单位（见 lessons/01-agent-loop.md）。
	// 落成日志之后，"跑到第几步、最后一步为什么结束"才是**可重建**的 —— 这是 P3-1 step 级 checkpoint 的地基。
	EventStepStart = "step/start"
	EventStepEnd   = "step/end"
	// EventLLMRetry 模型调用重试（log-only，对应 HARNESS-TODO 的 P1-3）。
	// 为什么必须进日志：重试是"这一轮为什么变慢"的唯一解释；
	// 而且本轮的重试预算从日志恢复（RetryAttemptsInTurn），
	// 进程在轮内重启也不会把预算凭空重置 —— 对齐 dsh 那句"重试状态幂等于日志"。
	EventLLMRetry = "llm/retry"
	// EventCorrupt 读取侧标记：这条日志解不出来（保留占位以保证下标=seq 不错位）。
	// 只在内存里用，不会写进日志。
	EventCorrupt = "corrupt/event"
)

// LogFormatVersion 当前写出的日志格式版本（对应 HARNESS-TODO 的 P1-5）。
//
// 为什么需要版本号：MemoryDTO 的结构会变（FoldUpto / Interrupted / V 都是后加的），
// 而 json.Unmarshal 对"多出来的字段"和"缺失的字段"**一律不报错** ——
// 旧日志读进新结构，新字段静默变成零值，表现是"旧数据莫名失效"却没有任何信号。
//
// 规则（对齐 dsh 的持久化：宁可拒绝，不要猜）：
//   - 读到**比本版本更新**的日志 → 明确报错拒绝加载，而不是拿零值继续跑；
//   - 读到比本版本旧的 → 照常读（0 = 加版本号之前的老日志，按 v1 对待）。
//
// 老数据绝不能因为"没有版本号"就被判定为非法 —— 那等于一次升级废掉用户全部历史。
const LogFormatVersion = 1

// Validate 校验一条事件在**结构上**是否合法（写入前调用，对应 P1-4）。
//
// 为什么要在写入侧校验：pair-or-drop 是投影时发现悬空就丢掉 —— 那是事后兜底，
// 脏数据**已经写进日志了**，靠投影默默丢弃来掩盖，问题永远查不出来
// （你会看到"工具结果凭空消失"，但日志里明明有）。
// 这里只放"一定是 bug"的结构性规则，不掺业务判断，免得把正常写法误判成违规。
func Validate(dto MemoryDTO) error {
	switch dto.Type {
	case EventToolResult:
		// 没有 ToolCallID 的 tool/result 配对不上任何 tool/call，投影时只能被丢弃 ——
		// 而模型会以为这个工具压根没返回。
		if strings.TrimSpace(dto.ToolCallID) == "" {
			return fmt.Errorf("不变量违反：tool/result 必须带 ToolCallID（否则配对不上 tool/call，投影时会被静默丢弃）")
		}
	case EventToolCall:
		// 空调用的 tool/call 会让模型看到一条"要求调用但没说要调什么"的消息。
		if len(dto.ToolCalls) == 0 {
			return fmt.Errorf("不变量违反：tool/call 必须携带至少一条调用清单")
		}
	case EventCompactionSummary:
		// 区间反了的摘要会让遮蔽逻辑把"负数范围"也标成已覆盖，历史被无声吞掉。
		if dto.CompactionTo < dto.CompactionFrom {
			return fmt.Errorf("不变量违反：compaction/summary 的 CompactionTo(%d) 不能小于 CompactionFrom(%d)",
				dto.CompactionTo, dto.CompactionFrom)
		}
	case EventStepEnd:
		// 没有原因的 step/end 等于什么都没说 —— 断点续跑时无法判断"为什么停"。
		if strings.TrimSpace(dto.Content) == "" {
			return fmt.Errorf("不变量违反：step/end 必须带结构化的结束原因（completed/max-tokens/aborted/error/…）")
		}
	}
	return nil
}

// ValidateLog 全量**序列**校验（单条 Validate 只看一条，看不到"顺序"，所以需要它）。
//
// 为什么必须有：`step/start` 与 `step/end` 必须交替、`tool/result` 必须配到 `tool/call` ——
// 这些只有把整条日志按序扫一遍才看得出来。写入侧只能拦"单条结构非法"，
// 而"写入丢失 / 顺序错乱"这类问题只能在体检时抓（真实数据里已经出现过 step/start≠step/end）。
//
// 末尾悬空（未闭合的 step / tool-call）是**崩溃的正常现象**，Repair 会收尾 —— 只作"提示"返回，
// 前缀是 `（提示）`，调用方据此把它和真正的错位区分开。
func ValidateLog(dtos []MemoryDTO) []string {
	var problems []string
	stepOpen := false
	openStepSeq := -1
	openCalls := map[string]int{} // callId -> tool/call 的 seq

	for i, dto := range dtos {
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		switch typ {
		case EventStepStart:
			if stepOpen {
				problems = append(problems, fmt.Sprintf(
					"seq=%d: step/start 重复 —— 上一个 step（起始 seq=%d）还没闭合", i, openStepSeq))
			}
			stepOpen = true
			openStepSeq = i
		case EventStepEnd:
			if !stepOpen {
				problems = append(problems, fmt.Sprintf(
					"seq=%d: step/end 找不到配对的 step/start（写入丢失或顺序被破坏）", i))
			}
			stepOpen = false
		case EventToolCall:
			for _, tc := range dto.ToolCalls {
				openCalls[tc.ID] = i
			}
		case EventToolResult:
			if _, ok := openCalls[dto.ToolCallID]; !ok {
				problems = append(problems, fmt.Sprintf(
					"seq=%d: tool/result(%s) 找不到配对的 tool/call", i, dto.ToolCallID))
			} else {
				delete(openCalls, dto.ToolCallID)
			}
		}
	}

	if stepOpen {
		problems = append(problems, fmt.Sprintf("（提示）末尾有一个未闭合的 step（起始 seq=%d）—— 正常，Repair 会收尾", openStepSeq))
	}
	for id, seq := range openCalls {
		problems = append(problems, fmt.Sprintf("（提示）tool/call(%s)（seq=%d）未落结果 —— 正常，Repair 会补", id, seq))
	}
	return problems
}

// prepareForWrite 写入前的统一把关：盖版本号 + 走不变量校验。
//
// 所有写入路径（单条 / 批量）都要过这一关，这样"日志里不会有结构性非法事件"
// 就是**写入侧保证**的，而不是读取侧事后擦屁股。
//
// 严格度由环境变量 INVARIANTS 控制：
//   - 默认（strict）：校验不过就**拒绝写入**并返回错误。理由同 fail-closed ——
//     写进去也是垃圾（投影时会丢），还不如当场炸出来，让 bug 在源头可见。
//   - warn：只打日志、仍然写入（排查线上问题时用，别长期开）。
//   - off：跳过校验。
func prepareForWrite(dto *MemoryDTO) error {
	if dto.V == 0 {
		dto.V = LogFormatVersion
	}
	err := Validate(*dto)
	if err == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("INVARIANTS"))) {
	case "off":
		return nil
	case "warn":
		fmt.Printf(" [不变量] %v（INVARIANTS=warn，仍然写入）\n", err)
		return nil
	default:
		fmt.Printf(" [不变量] 拒绝写入：%v\n", err)
		return err
	}
}

// ToolCallData 一次工具调用的结构化信息（持久化的精简版）。
type ToolCallData struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// MemoryDTO 持久化在日志里的一条事件（自研纯净 DTO，避开第三方结构体反序列化陷阱）。
type MemoryDTO struct {
	Run            *runstate.Ref  `json:"run,omitempty"`
	Type           string         `json:"type"`
	Role           string         `json:"role"`
	Content        string         `json:"content"`
	ToolCallID     string         `json:"tool_call_id,omitempty"` // tool/result 用它配对 tool/call
	ToolName       string         `json:"tool_name,omitempty"`
	ToolCalls      []ToolCallData `json:"tool_calls,omitempty"`      // tool/call 携带的调用清单
	CompactionFrom int            `json:"compaction_from,omitempty"` // compaction/summary 遮蔽的起始 seq
	CompactionTo   int            `json:"compaction_to,omitempty"`   // compaction/summary 遮蔽的结束 seq
	FoldUpto       int            `json:"fold_upto,omitempty"`       // session/checkpoint 的折叠水位：此下标（含）之前的事件已被摘要覆盖
	Interrupted    bool           `json:"interrupted,omitempty"`     // assistant/message：这次流是被切断的，不是正常结束
	// V 日志格式版本（见 LogFormatVersion）。
	// 为什么要有：结构一变（FoldUpto / Interrupted 都是后加的），旧日志读进新结构时
	// json.Unmarshal **不会报错**，新字段静默变成零值 —— 表现是"旧数据莫名失效"却没有任何信号。
	// 0 = 加版本号之前的日志，按 v1 对待（兼容，绝不能因为老数据没版本号就废掉它）。
	V    int       `json:"v,omitempty"`
	Time time.Time `json:"time"`
}

// Store 记忆器官接口：只追加日志 + 投影模型历史 + 归档检索 + 上下文压缩。
type Store interface {
	SaveMessage(ctx context.Context, userID uint, msg *schema.Message) error
	// SaveReply 落盘一条助手回复；interrupted=true 表示这次流是被切断的（不是正常结束）。
	// 为什么要区分：半截回复被当成"模型的完整回答"永久写进历史，会污染后续所有轮次 ——
	// 模型会以为自己说过那些话。已投递的文本是真实发生过的，照存，但必须打上标记。
	SaveReply(ctx context.Context, userID uint, content string, interrupted bool) error
	GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error)
	SearchArchival(ctx context.Context, userID uint, query string, topK int) ([]*schema.Message, error)
	Compact(ctx context.Context, userID uint, summarize CompactSummarizer) error
	// Repair 检出"未闭合的轮次"并补一条合成收尾；返回修了几轮（0 = 无需修复）。
	Repair(ctx context.Context, userID uint) (int, error)
	// AppendEvent 追加一条自定义事件（审计/检查点这类 log-only 事件）。
	AppendEvent(ctx context.Context, userID uint, dto MemoryDTO) error

	ResumePoint(ctx context.Context, userID uint) (closedSteps int, openTurn bool, err error)
	// HasEvent 该类型的事件是否已写过（用于"系统提示只写一次"这类去重）。
	HasEvent(ctx context.Context, userID uint, eventType string) (bool, error)
	// RetryAttemptsInTurn 本轮（最近一条用户消息之后）已经用掉的重试次数。
	RetryAttemptsInTurn(ctx context.Context, userID uint) (int, error)
}

// RedisStore 是 Store 的 Redis 实现。
type RedisStore struct{}

// EventStore 为运行时提供同源的会话与批量事件存储。
// AppendEvents 须先验证整个批次，再整体追加；错误不保证远端未接受写入。
type EventStore interface {
	Store
	AppendEvents(context.Context, uint, []MemoryDTO) error
}

func (RedisStore) AppendEvents(ctx context.Context, userID uint, events []MemoryDTO) error {
	return WriteMemoryEvents(ctx, userID, events)
}

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
//
// 投影之后还有一道**硬保护**：按 token 裁到窗口以内。
// 为什么需要它：压缩是 best-effort（摘要失败就跳过本次），
// 万一压缩连续失败，不能把整个超长上下文直接塞给模型 —— 那是会直接报错的。
func (RedisStore) GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error) {
	dtos, base, err := RedisStore{}.loadActiveLog(ctx, userID)
	if err != nil {
		return nil, err
	}
	history := ProjectMessagesFrom(dtos, base)
	history = tokenmeter.TrimToBudget(history, budget.ContextWindow)
	return history, nil
}

// foldKey 折叠水位指针的 key（派生数据，不是日志本体）。
func foldKey(userID uint) string { return fmt.Sprintf("agent:V2:fold:%d", userID) }

// FoldPoint 日志的折叠水位：此下标之前的事件已被摘要整体覆盖，不必再读。
//
// 为什么要它：压缩语义修对之后，"读日志"仍然是每轮全量 LRange，对话越长每轮越慢（O(总长)）。
// 事件溯源的标准解法是 snapshot —— 把已经摘要过的前缀折叠掉，读取只从最新快照开始。
// 摘要只代表 CompactionTo 之前的内容，读取起点必须是 CompactionTo + 1，
// 不能跳到摘要自己的下标，否则尚未摘要的近期尾部也会被跳过。
//
// 快速路径读指针键（O(1)）；指针丢了就从日志里的摘要覆盖范围重建一次（O(n)），
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
		// 旧版缓存指向摘要本身；只修派生指针，不改原始日志。
		raw, err := rdb.LIndex(ctx, key, int64(base)).Result()
		if err != nil {
			return 0
		}
		var dto MemoryDTO
		if json.Unmarshal([]byte(raw), &dto) != nil {
			return 0
		}
		if dto.Type == EventCompactionSummary && dto.CompactionFrom == 0 && dto.CompactionTo >= 0 && dto.CompactionTo < base {
			base = dto.CompactionTo + 1
			setFoldPoint(ctx, userID, base)
		}
		return base
	}
	// checkpoint 是派生记录，旧版可能夸大覆盖范围；以摘要本身为准。
	dataList, err := rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return 0
	}
	for i := len(dataList) - 1; i >= 0; i-- {
		var dto MemoryDTO
		if json.Unmarshal([]byte(dataList[i]), &dto) != nil {
			continue
		}
		if dto.Type == EventCompactionSummary && dto.CompactionFrom == 0 && dto.CompactionTo >= 0 && dto.CompactionTo < i {
			base := dto.CompactionTo + 1
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
	for i, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			fmt.Printf(" [记忆中枢] 破译记忆碎片失败: %v\n", err)
			// 坏数据也要占一个位置：下标就是 seq，跳过会让后面所有事件错位。
			dto = MemoryDTO{Type: EventCorrupt}
		}
		// 版本比本程序新 → 这些日志是更高版本写出来的，里面有本版本不认识的字段。
		// 宁可明确拒绝，也不要用零值继续跑（那会把"读不懂"伪装成"读到空"）。
		if dto.V > LogFormatVersion {
			return nil, base, fmt.Errorf(
				"日志格式 v%d 本版本(v%d)不认识，拒绝加载（userID=%d, seq=%d）：宁可不读，也不用零值猜",
				dto.V, LogFormatVersion, userID, base+i)
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
		if typ == EventSystemPrompt || typ == EventToolCall || typ == EventToolResult ||
			typ == EventStepStart || typ == EventStepEnd {
			continue // log-only 或工具/step 事件：检索的也是"喂过模型的记忆"，不含日志噪声
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

// SaveReply 落盘一条助手回复。
//
// interrupted=true 表示这次流是被切断的（网络断、服务重启、用户取消），不是正常结束。
// 为什么必须区分：半截回复被当成"模型的完整回答"永久写进历史，会污染后续所有轮次 ——
// 模型之后会以为自己说过那些话。已投递的文本确实发生过，所以照存，但必须打标记。
func (RedisStore) SaveReply(ctx context.Context, userID uint, content string, interrupted bool) error {
	return writeMemoryEvent(ctx, userID, MemoryDTO{
		Type:        EventAssistantMessage,
		Role:        "assistant",
		Content:     content,
		Interrupted: interrupted,
		Time:        time.Now(),
	})
}

// TurnEndReason 一轮对话的收尾原因（结构化，写进 turn/end 事件的 content）。
//
// 为什么结构化：以前收尾原因是个裸字符串（"completed"），机读只能靠 strings.Contains，
// 和 step/end 是一个毛病 —— 结构化之后"这一轮为什么结束"能被可靠解析，也能往里加字段。
type TurnEndReason string

const (
	TurnCompleted   TurnEndReason = "completed"   // 模型正常说完
	TurnInterrupted TurnEndReason = "interrupted" // 崩溃/被切断，由 Repair 合成收尾
)

// TurnEndContent 生成 turn/end 事件的 content（结构化 JSON）。
func TurnEndContent(reason TurnEndReason, detail string) string {
	b, _ := json.Marshal(struct {
		Reason TurnEndReason `json:"reason"`
		Detail string        `json:"detail,omitempty"`
	}{Reason: reason, Detail: detail})
	return string(b)
}

// Repair 检出"未闭合的轮次 / step / 工具调用"并补**合成**的收尾事件，返回修了几轮（0 = 无需修复）。
//
// 为什么需要：RunAgentTurn 跑到一半进程被杀/崩溃时，日志里 user/message 后面什么都没有。
// 下一轮投影时它只是被当成"模型没回"，**没人分得清那轮是断了还是真没回** ——
// 这两件事的处置完全不同（一个该重试，一个该换问法）。
//
// 对齐 dsh（session-persistence/coordinator.ts）：遇到没有 turn/end 的开放轮次，
// 合成一条 turn/end{interrupted}。**只追加、绝不截断或改写历史** ——
// 这也是整个记忆器官从头到尾守的那条纪律。
//
// P3-1 扩展（3.2）—— 除了轮次，还补两类"崩溃留下的悬空"：
//   - **开放 step**：`step/start` 之后没有 `step/end` → 补一条 step/end{interrupted}，
//     否则"跑到第几步"无法重建，断点续跑无从谈起。
//   - **悬空 tool/call**：派了工具（`tool/call`）却没落结果 → 补一条"结果未知"的 `tool/result`，
//     否则投影（pair-or-drop）会看到悬空的工具调用。**关键：合成结果而不是重放工具** ——
//     工具可能已经产生了副作用，重放会让副作用做两遍（详见 loop.go 的幂等前提）。
func (RedisStore) Repair(ctx context.Context, userID uint) (int, error) {
	dtos, _, err := RedisStore{}.loadActiveLog(ctx, userID)
	if err != nil {
		return 0, err
	}

	openTurn := false
	unclosedTurns := 0
	stepOpen := false
	unclosedSteps := 0
	openCalls := map[string]string{} // callId -> 工具名（合成结果时带上）

	for _, dto := range dtos {
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		switch typ {
		case EventUserMessage:
			if openTurn {
				unclosedTurns++ // 上一条用户消息开的轮次，一直没闭合
			}
			openTurn = true
		case EventAssistantMessage:
			// 被中断的回复**不算闭合** —— 那一轮没走完，用户的问题没被答完。
			// 这条判断是必须的：被中断的轮次同样会留下一条 assistant/message（半截的），
			// 如果一律当成闭合，就恰好漏掉了最该被认出来的那种情况。
			if dto.Interrupted {
				continue
			}
			openTurn = false
		case EventTurnEnd:
			openTurn = false
		case EventStepStart:
			// 只记"当前有没有开着的 step"，**不**把"连续两个 start"当成两个未闭合：
			// 历史数据里父/子 agent 的 step 交错会造成这种形状，按个数补会越补越多（真实踩过）。
			stepOpen = true
		case EventStepEnd:
			stepOpen = false
		case EventToolCall:
			for _, tc := range dto.ToolCalls {
				openCalls[tc.ID] = tc.Name
			}
		case EventToolResult:
			delete(openCalls, dto.ToolCallID)
		}
	}
	if stepOpen {
		unclosedSteps = 1
	}
	if openTurn {
		unclosedTurns++
	}

	if unclosedSteps == 0 && len(openCalls) == 0 && unclosedTurns == 0 {
		return 0, nil
	}

	// 只追加合成事件，绝不改写历史。顺序与正常写入一致：step/end → tool/result → turn/end。
	if unclosedSteps > 0 {
		if err := writeMemoryEvent(ctx, userID, MemoryDTO{
			Type: EventStepEnd, Role: "system",
			Content: fmt.Sprintf("interrupted：检测到 %d 个未闭合的 step（进程中断/崩溃），已合成收尾。", unclosedSteps),
			Time:    time.Now(),
		}); err != nil {
			return 0, err
		}
	}
	ids := make([]string, 0, len(openCalls))
	for id := range openCalls {
		ids = append(ids, id)
	}
	sort.Strings(ids) // 顺序确定，便于测试与排查
	for _, id := range ids {
		if err := writeMemoryEvent(ctx, userID, MemoryDTO{
			Type: EventToolResult, Role: "tool", ToolCallID: id, ToolName: openCalls[id],
			Content: "（进程中断：该工具调用已派发但结果未知；未重放，以免副作用做两遍）",
			Time:    time.Now(),
		}); err != nil {
			return 0, err
		}
	}
	if unclosedTurns > 0 {
		if err := writeMemoryEvent(ctx, userID, MemoryDTO{
			Type: EventTurnEnd,
			Role: "system",
			Content: TurnEndContent(TurnInterrupted, fmt.Sprintf(
				"检测到 %d 个未闭合的轮次（进程中断/崩溃/回复被切断），已合成收尾；原始事件一条未改。", unclosedTurns)),
			Time: time.Now(),
		}); err != nil {
			return 0, err
		}
	}
	return unclosedTurns, nil
}

// ResumePoint 报告"续跑到哪"：已闭合的 step 数 + 末尾是否有未闭合轮次（只读，供断点续跑判断）。
// 判据与 Repair 一致：被中断的 assistant 回复**不算**闭合。
func (RedisStore) ResumePoint(ctx context.Context, userID uint) (int, bool, error) {
	dtos, _, err := RedisStore{}.loadActiveLog(ctx, userID)
	if err != nil {
		return 0, false, err
	}
	closedSteps := 0
	openTurn := false
	for _, dto := range dtos {
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		switch typ {
		case EventStepEnd:
			closedSteps++
		case EventUserMessage:
			openTurn = true
		case EventTurnEnd:
			openTurn = false
		case EventAssistantMessage:
			if dto.Interrupted {
				continue
			}
			openTurn = false
		}
	}
	return closedSteps, openTurn, nil
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

// RetryAttemptsInTurn 数出"本轮已经重试过几次"（对应 P1-3 第 4 点）。
//
// 为什么预算按"轮"而不是按"次调用"：一轮里 agent 会调很多次模型
// （每次工具调用之后都要再调一次），如果每次调用都各自从 0 开始算 5 次预算，
// 一个坏上游能让这一轮实际重试几十次。
//
// 为什么数日志而不是数内存：日志是唯一真相。以后做 P3-1（step 级 checkpoint）、
// 进程能在轮内续跑时，计数必须来自日志，否则重启一次预算就被重置 ——
// 这正是 dsh 那句"重试状态幂等于日志"的意思。
func (RedisStore) RetryAttemptsInTurn(ctx context.Context, userID uint) (int, error) {
	dtos, _, err := RedisStore{}.loadActiveLog(ctx, userID)
	if err != nil {
		return 0, err
	}
	n := 0
	// 从尾部往前数，遇到本轮的用户消息就停 —— 再往前就是上一轮了。
	for i := len(dtos) - 1; i >= 0; i-- {
		typ := dtos[i].Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dtos[i].Role))
		}
		if typ == EventUserMessage {
			break
		}
		if typ == EventLLMRetry {
			n++
		}
	}
	return n, nil
}

// writeMemoryEvent 把一条记忆事件序列化后 RPush 进记忆日志（唯一的写入口）。
func writeMemoryEvent(ctx context.Context, userID uint, dto MemoryDTO) error {
	ref, err := runstate.Bind(ctx, userID, dto.Run)
	if err != nil {
		return err
	}
	dto.Run = ref
	if rdb == nil {
		fmt.Println(" [记忆中枢] 致命错误：Redis 连接池未挂载")
		return fmt.Errorf("redis client is nil")
	}
	if err := prepareForWrite(&dto); err != nil {
		return err
	}
	data, err := json.Marshal(dto)
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆序列化崩溃: %v\n", err)
		return err
	}
	err = appendSourceEvents(ctx, userID, data)
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆落盘失败: %v\n", err)
	}
	return err
}

// WriteMemoryEvents 按顺序写入一批记忆事件（供 agent 的 MessageModifier 钩子落盘工具调用用）。
// 保证顺序：tool/call 必须排在它对应的 tool/result 前面。
func WriteMemoryEvents(ctx context.Context, userID uint, pending []MemoryDTO) error {
	if len(pending) == 0 {
		return nil
	}
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	values := make([]any, 0, len(pending))
	for _, dto := range pending {
		ref, err := runstate.Bind(ctx, userID, dto.Run)
		if err != nil {
			return err
		}
		dto.Run = ref
		// 批量路径不能成为"绕过不变量"的后门：和单条写入走同一道把关。
		// 全部校验完再用一个 RPUSH 写入，避免非法事件导致部分批次被接受。
		if err := prepareForWrite(&dto); err != nil {
			return fmt.Errorf("校验记忆事件失败: %w", err)
		}
		data, err := json.Marshal(dto)
		if err != nil {
			return fmt.Errorf("序列化记忆事件失败: %w", err)
		}
		values = append(values, data)
	}
	if err := appendSourceEvents(ctx, userID, values...); err != nil {
		return fmt.Errorf("写入记忆事件失败: %w", err)
	}
	return nil
}

// CompactSummarizer 把一段早期消息压成摘要（由编排层注入，通常用模型实现）。
type CompactSummarizer func(ctx context.Context, messages []string) (string, error)

// Compact 上下文压缩（对应 HARNESS-STUDY M7）：把窗口之外的早期对话压成一条摘要，
// 写一条 log-only 的 compaction/summary 事件；投影时被遮蔽的旧事件不再喂给模型。
// 没有可压缩的早期消息时是空操作（不调 summarize）。
//
// 三条不变量（缺一条都会退化）：
//  1. **按 token 判断，不按条数**：用量超过 `窗口 × thresholdRatio` 才压，
//     压完保留尾部约 `窗口 × retainRatio`。条数和上下文成本差两个数量级，不能拿它当尺子。
//  2. **单调**：只压"上一条摘要 CompactionTo 之后"的新消息（high-water mark），已摘要过的绝不再摘。
//  3. **单一滚动摘要**：新摘要 = 旧摘要 + 新增溢出消息，重写成一条总摘要，
//     且 CompactionFrom 恒为 0 —— 旧摘要自身也是一个日志下标，会落进 [0,to] 被遮蔽，
//     于是投影里永远只剩一条摘要，不会出现 N 条互相重叠的摘要同时喂给模型。
//
// 注意：压缩是 best-effort——摘要失败就跳过本次，不影响正常对话；
// 真正的硬保护在 GetHistory 里（按窗口 token 硬裁）。
func (RedisStore) Compact(ctx context.Context, userID uint, summarize CompactSummarizer) error {
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	// 只读"活跃尾部"（折叠水位之后）——折叠就是事件溯源的 snapshot：
	// 从未摘要尾部开始读，末尾还包含用于重建早期内容的摘要。
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
		case EventToolCall, EventToolResult:
			// 工具事实也会被摘要遮蔽，必须连同调用参数交给摘要器。
			data, err := json.Marshal(dto)
			if err != nil {
				return fmt.Errorf("序列化摘要工具事件失败: %w", err)
			}
			msgs = append(msgs, msg{seq, string(data)})
		}
	}
	// ---- 判断该不该压：按 token，不按条数 ----
	// 用"投影后的完整消息"来算 —— 那正是模型真正会看到的东西（含配对的工具结果）。
	projected := ProjectMessagesFrom(dtos, base)
	used := tokenmeter.Used(projected)
	if used <= budget.TriggerTokens() {
		return nil // 还没用到触发线，不压
	}

	// ---- 决定压到哪：从日志尾部往前累加 token，直到落进 retain 预算 ----
	// 这里走**日志事件**（含 tool/result）而不是只看 user/assistant：
	// 工具结果往往才是上下文里最大的一块，漏掉它会导致"以为留了尾巴、其实早超了"。
	retain := budget.RetainTokens()
	acc := 0
	keepSeq := base
	for i := len(dtos) - 1; i >= 0; i-- {
		seq := base + i
		if seq <= lastTo {
			break // 只管 high-water mark 之后的部分
		}
		typ := dtos[i].Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dtos[i].Role))
		}
		switch typ {
		case EventUserMessage, EventAssistantMessage, EventToolResult:
			acc += tokenmeter.Estimate(dtos[i].Content) + 4
		default:
			continue
		}
		keepSeq = seq
		if acc >= retain {
			break
		}
	}

	// 退回这轮的用户消息，保留完整轮次，避免切开工具调用/结果或开放 step。
	// 找不到轮次起点时保守地不压，交给预算硬裁兜底。
	for keepSeq > base {
		dto := dtos[keepSeq-base]
		if dto.Type == EventUserMessage || (dto.Type == "" && dto.Role == "user") {
			break
		}
		keepSeq--
	}
	// 保留区之前的可见内容全部交给摘要器。
	var toCompress []msg
	for _, m := range msgs {
		if m.idx < keepSeq {
			toCompress = append(toCompress, m)
		}
	}
	if len(toCompress) == 0 {
		// 极端情况：一两条消息就把窗口撑爆（比如单条就是巨型文本）。
		// 这时候没有"早期对话"可压 —— 交给 GetHistory 的硬裁兜底，不要硬造一条空摘要。
		return nil
	}
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
		return fmt.Errorf("压缩摘要失败: %w", err)
	}
	dto := MemoryDTO{
		Type:    EventCompactionSummary,
		Role:    "assistant",
		Content: summary,
		// from 恒为 0：这条摘要代表"到目前为止的全部早期对话"，
		// 旧摘要的下标也在这段区间里，会被投影自动遮蔽。
		CompactionFrom: 0,
		CompactionTo:   keepSeq - 1,
		Time:           time.Now(),
	}
	if err := writeMemoryEvent(ctx, userID, dto); err != nil {
		return err
	}

	// 起点留在未摘要尾部；摘要写入成功后，缓存丢失也能从覆盖范围重建。
	if err := writeMemoryEvent(ctx, userID, MemoryDTO{
		Type:     EventCheckpoint,
		Role:     "system",
		Content:  fmt.Sprintf("折叠水位推进到 seq %d：此前事件由滚动摘要代表", keepSeq),
		FoldUpto: dto.CompactionTo,
		Time:     time.Now(),
	}); err != nil {
		return err
	}
	setFoldPoint(ctx, userID, keepSeq)
	return nil
}

// ProjectMessagesFrom 把一组记忆事件投影成喂给模型的 []*schema.Message（纯函数，不依赖 Redis）。
//
// 投影规则（pair-or-drop）：见本文件顶部事件类型注释。
// dtos[0] 在日志里的真实 seq 由 baseSeq 给出 —— 摘要遮蔽区间（CompactionFrom/To）
// 记的都是真实 seq，折叠之后只读尾部就必须把偏移加回来，否则所有区间静默错位。
//
// **这里不做任何长度/条数裁剪**：投影只负责"把日志翻译成消息"，
// 窗口大小是预算的事（见 Compact 与 GetHistory 的 TrimToBudget）。
// 两者混在一起正是原来"按条数截断"的病根。
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
		// checkpoint 只记派生读取位置，不拥有遮蔽内容的权力。
		if dtos[i].Type != EventCompactionSummary {
			continue
		}
		for j := max(baseSeq, dtos[i].CompactionFrom); j <= min(baseSeq+len(dtos)-1, dtos[i].CompactionTo); j++ {
			shadowed[j] = true
		}
		if activeSummary == -1 || dtos[i].CompactionTo >= dtos[activeSummary].CompactionTo {
			activeSummary = i // 注意：这里是数组下标，不是 seq
		}
	}

	history := make([]*schema.Message, 0, len(dtos))
	if activeSummary >= 0 {
		// 写入顺序是尾部在前、摘要在后；模型顺序必须是摘要在前、尾部在后。
		history = append(history, schema.UserMessage("【早期对话摘要】"+dtos[activeSummary].Content))
	}

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
			continue // 唯一生效摘要已放到历史开头
		case EventSystemPrompt, EventAudit, EventCheckpoint, EventUsage, EventTurnEnd, EventStepStart, EventStepEnd, EventCorrupt:
			continue // log-only：只存档/审计/计量/收尾/step 边界用，永远不喂模型
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
			content := dto.Content
			// 被中断的半截回复要标出来：不标的话模型会以为自己当时把话说完了，
			// 于是接着半截话继续答，或者引用一段它从没真正表达完整的意思。
			if typ == EventAssistantMessage && dto.Interrupted {
				content = "【上一轮回复被中断，以下是已生成的部分】" + content
			}
			history = append(history, &schema.Message{
				Role:    schema.RoleType(dto.Role),
				Content: content,
			})
		}
	}
	flushPair()

	// 这里刻意不做条数截断：窗口由 token 预算决定（GetHistory 的 TrimToBudget 兜底）。
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
