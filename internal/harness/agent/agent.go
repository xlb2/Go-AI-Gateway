// Package agent agent 循环 + 模型适配器缝 + 工具。
//
// 定位：harness 解剖图里的"agent loop"和"LLM 适配器缝"（HARNESS-STUDY M0/M3）。
// 自研循环（loop.go，阶段 2.2 起为唯一实现）：Eino 只提供零件（模型缝 + 工具接口 + 流式消息拼接），
// 循环本身是我们自己的 —— step 是一等公民，为 P3-1 的 step 级 checkpoint 铺路。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/mcp"
	"go_im_gateway/internal/harness/metrics"
	"go_im_gateway/internal/harness/retry"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/subagent"
)

// ArchivalSearchParams 归档记忆检索工具的入参
type ArchivalSearchParams struct {
	Query string `json:"query" jsonschema:"description=要在历史对话中检索的关键词或问题,required"`
}

// getUserID 从 context 里取出 user_id（agent 工具的公共取数函数）。
func getUserID(ctx context.Context) (uint, error) {
	userIDVal := ctx.Value("user_id")
	if userIDVal == nil {
		return 0, fmt.Errorf("内部错误：上下文中丢失 user_id")
	}
	switch v := userIDVal.(type) {
	case uint:
		return v, nil
	case int:
		return uint(v), nil
	case float64:
		return uint(v), nil
	default:
		return 0, fmt.Errorf("内部错误：user_id 类型不匹配")
	}
}

// DefenseParams execute_system_defense 工具的入参
type DefenseParams struct {
	Emotion     string `json:"emotion" jsonschema:"description=检测到的用户情绪,required"`
	ThreatLevel string `json:"threat_level" jsonschema:"description=威胁等级 low/medium/high,required"`
}

// ArchivalSearchTool 记忆检索工具：滑动窗口里找不到答案时，模型可以主动调用它查更早的历史。
var ArchivalSearchTool, _ = utils.InferTool(
	"search_memory_archive",
	"当用户提到较早之前说过的话、当前对话上下文里找不到时，调用此工具检索完整历史记录。",
	func(ctx context.Context, params *ArchivalSearchParams) (string, error) {
		userID, err := getUserID(ctx)
		if err != nil {
			return "", err
		}
		var store session.RedisStore
		results, err := store.SearchArchival(ctx, userID, params.Query, session.ArchiveDefaultTopK)
		if err != nil {
			return "", fmt.Errorf("归档检索失败: %v", err)
		}
		if len(results) == 0 {
			return "未在历史记录中找到相关内容。", nil
		}
		var sb string
		for i, msg := range results {
			sb += fmt.Sprintf("%d. [%s] %s\n", i+1, msg.Role, msg.Content)
		}
		return "检索到以下历史记录：\n" + sb, nil
	},
)

// sanitizeThreatLevel 把模型给的威胁等级收敛到白名单。
// 为什么必须做：这个值会被拼进 shell 命令（防御动作的审计落盘），
// 模型输出不可信，直接拼就是命令注入（`high; rm -rf /`）。白名单是唯一正确做法。
func sanitizeThreatLevel(level string) string {
	switch lv := strings.ToLower(strings.TrimSpace(level)); lv {
	case "low", "medium", "high":
		return lv
	default:
		return "unknown"
	}
}

// defenseAuditTarget 防御动作的审计落盘目标（可用 DEFENSE_AUDIT_FILE 覆盖），返回绝对路径。
func defenseAuditTarget() string {
	p := os.Getenv("DEFENSE_AUDIT_FILE")
	if p == "" {
		p = filepath.Join("audit", "defense.log")
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// defenseExecProposal 把一次防御动作翻译成一份**结构化执行计划**（HARNESS-TODO 的 P2-1）。
//
// 这是"审批不是假的"的关键：挂起时就把批准后要执行的东西一起定下来，
// 人只决定批不批，批完直接交给沙箱器官执行，不再经过模型
// （避免审批通道变成参数注入的入口）。
//
// 演示用的真实副作用 = 往审计文件追加一条封禁记录（可观察、不破坏性）。
//
// 注意这里**不拼任何 shell 命令**：动作本质是"写文件"，就用沙箱的内置文件动作
// （纯 Go 写），而不是 `sh -c 'echo x >> y'`。这一步把两个坑一起根治了：
//  1. 项目目录叫 "agent study"（带空格），绝对路径拼进命令串后会被 Go 的参数转义
//     和 cmd.exe 的引号规则来回撕（实测报"文件名、目录名或卷标语法不正确"）——
//     现在路径只作为结构体字段传递，根本不进命令串。
//  2. 中文经 cmd/sh 重定向容易被终端代码页转成乱码 —— 现在字节直接写文件，与代码页无关。
func defenseExecProposal(level, emotion string) sandbox.Request {
	line := fmt.Sprintf("[%s] BLOCK-DECISION level=%s emotion=%s\n",
		time.Now().Format(time.RFC3339), level, emotion)
	return sandbox.Request{
		Kind:     sandbox.ActionAppendFile,
		FilePath: defenseAuditTarget(),
		FileText: line,
		Timeout:  10 * time.Second,
	}
}

// DefenseTool 防御工具：模型发现用户愤怒/攻击性指令时调用，起草一个**带具体可执行提案**的
// 高危动作并挂起（写 ApprovalStore），等管理员输入 auth:approve / auth:reject 审批。
// 注意提案里带上了批准后要跑的命令——批准之后就交给沙箱器官真实执行，
// 不再是"打印一行日志假装执行"（对应 dsh 决策链的 ask→approval→execute）。
var DefenseTool, _ = utils.InferTool(
	"execute_system_defense",
	"当用户愤怒抱怨或发出攻击性指令时调用此工具，起草一个防御动作并挂起，等管理员审批后才真正执行。",
	func(ctx context.Context, params *DefenseParams) (string, error) {
		userID, err := getUserID(ctx)
		if err != nil {
			return "", err
		}
		level := sanitizeThreatLevel(params.ThreatLevel)
		pending := approval.PendingAction{
			Action: "execute_system_defense",
			Param:  params.Emotion,
			Reason: fmt.Sprintf("检测到情绪=%s、威胁等级=%s，需封禁并留痕", params.Emotion, level),
			Plan:   defenseExecProposal(level, params.Emotion),
			// 可选项由**提案方**决定（P2-2）：现在是"批准 / 拒绝"两个，
			// 将来加"仅本次允许 / 永久封禁"就往这个数组里加，交互协议不用动。
			AvailableDecisions: []string{"approve", "reject"},
			RequestedAt:        time.Now(),
		}
		store := approval.RedisStore{}
		if err := store.SetPending(ctx, userID, pending); err != nil {
			return "", fmt.Errorf("挂起防御动作失败: %v", err)
		}
		return fmt.Sprintf("⚠️ 防御动作已起草并挂起，等待管理员审批。\n提案：向审计文件追加一条封禁记录（level=%s）。\n请管理员输入 auth:approve 执行，或 auth:reject 取消。", level), nil
	},
)

// DelegateParams delegate_task 工具的入参
type DelegateParams struct {
	Task string `json:"task" jsonschema:"description=要交给子智能体完成的独立任务,required"`
}

// DelegateTool 子智能体工具：主 agent 把可拆分的独立子任务派给子智能体执行，
// 子任务上下文与主对话隔离，只把结果带回来（对应 HARNESS-STUDY M8）。
var DelegateTool, _ = utils.InferTool(
	"delegate_task",
	"当任务可以拆成独立的子任务、且不需要主对话上下文时调用，派一个子智能体去执行并返回结果。",
	func(ctx context.Context, params *DelegateParams) (string, error) {
		res, err := subagent.Run(ctx, params.Task)
		if err != nil {
			return "", fmt.Errorf("子智能体执行失败: %v", err)
		}
		return res.Output, nil
	},
)

// SpillSaveParams store_large_content 工具的入参
type SpillSaveParams struct {
	Name    string `json:"name" jsonschema:"description=给这段内容起的名字(便于辨认),required"`
	Content string `json:"content" jsonschema:"description=要保存的大段内容,required"`
}

// SaveLargeContentTool 溢出存储工具：把大段内容存起来，只把定位符 + 取回指引给模型（M7 spill）。
var SaveLargeContentTool, _ = utils.InferTool(
	"store_large_content",
	"当需要保存一段很长的内容(如大段文本/长文章)、不想每次都占用对话上下文时调用，返回一个定位符。",
	func(ctx context.Context, params *SpillSaveParams) (string, error) {
		userID, err := getUserID(ctx)
		if err != nil {
			return "", err
		}
		store := spill.RedisStore{}
		ref, err := store.SaveText(ctx, userID, params.Name, params.Content)
		if err != nil {
			return "", fmt.Errorf("溢出存储失败: %v", err)
		}
		return fmt.Sprintf("已保存(%d 字节)。%s", ref.Bytes, ref.RetrievalHint), nil
	},
)

// SpillLoadParams load_large_content 工具的入参
type SpillLoadParams struct {
	Locator string `json:"locator" jsonschema:"description=store_large_content 返回的定位符,required"`
}

// LoadLargeContentTool 溢出取回工具：按定位符取回之前保存的大段内容。
var LoadLargeContentTool, _ = utils.InferTool(
	"load_large_content",
	"用定位符取回之前保存的大段内容。",
	func(ctx context.Context, params *SpillLoadParams) (string, error) {
		store := spill.RedisStore{}
		content, err := store.LoadText(ctx, params.Locator)
		if err != nil {
			return "", err
		}
		return content, nil
	},
)

// DelegateTasksParams delegate_tasks 工具的入参
type DelegateTasksParams struct {
	Tasks []string `json:"tasks" jsonschema:"description=要并行派发的多个独立任务,required"`
}

// DelegateTasksTool 并行子智能体工具：把多个互相独立的子任务并行派发，
// 按输入顺序返回每个任务的结果（对应 M8 的"并行拆任务"）。
var DelegateTasksTool, _ = utils.InferTool(
	"delegate_tasks",
	"当有多个互相独立的子任务可以同时做时调用，并行派发给多个子智能体，返回按顺序排列的每个任务结果。",
	func(ctx context.Context, params *DelegateTasksParams) (string, error) {
		results := subagent.RunParallel(ctx, params.Tasks, 4)
		var sb strings.Builder
		for i, r := range results {
			sb.WriteString(fmt.Sprintf("任务%d: ", i+1))
			if r.Err != nil {
				sb.WriteString("失败(" + r.Err.Error() + ")\n")
			} else {
				sb.WriteString(r.Output + "\n")
			}
		}
		return sb.String(), nil
	},
)

// 说明：tool/call + tool/result 的落盘以前靠 Eino 的 MessageModifier 钩子（下标差分），
// 阶段 2.2 换成自研循环后，改由 loop.go 的 executeTools 直接落盘 —— 钩子已删除。

// memoryDTOFromToolCall 把 Eino 的"带工具调用的 assistant 消息"降维成 tool/call 事件。
func memoryDTOFromToolCall(msg *schema.Message) session.MemoryDTO {
	dto := session.MemoryDTO{
		Type:    session.EventToolCall,
		Role:    string(msg.Role),
		Content: msg.Content,
		Time:    time.Now(),
	}
	for _, tc := range msg.ToolCalls {
		dto.ToolCalls = append(dto.ToolCalls, session.ToolCallData{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return dto
}

// spillThreshold 工具结果超过该长度就自动外存（spill），事件里只留定位符+取回指引。
const spillThreshold = 2000

// memoryDTOFromToolResult 把 Eino 的"工具结果消息"降维成 tool/result 事件。
// 超大结果自动 spill（M7 自动策略）：内容外存，事件只留"定位符+取回指引"，
// 避免大内容把记忆日志和模型上下文撑爆；模型需要时可调 load_large_content 取回。
// spill 失败则回退存完整内容（best-effort）。
func memoryDTOFromToolResult(ctx context.Context, msg *schema.Message) session.MemoryDTO {
	content := msg.Content
	if len([]rune(content)) > spillThreshold {
		if userID, err := getUserID(ctx); err == nil {
			store := spill.RedisStore{}
			if ref, err := store.SaveText(ctx, userID, "tool_result", content); err == nil {
				content = fmt.Sprintf("（工具结果过大已外存）定位符: %s；需要时用 load_large_content 工具取回", ref.Locator)
			}
		}
	}
	return session.MemoryDTO{
		Type:       session.EventToolResult,
		Role:       string(msg.Role),
		Content:    content,
		ToolCallID: msg.ToolCallID,
		ToolName:   msg.ToolName,
		Time:       time.Now(),
	}
}

// newChatModel 用环境变量配置火山引擎模型（openai 兼容协议）。
func newChatModel(ctx context.Context) (model.ChatModel, error) {
	apiKey := os.Getenv("VOLC_ACCESS_KEY")
	endpoint := os.Getenv("VOLC_ENDPOINT_ID")
	baseURL := os.Getenv("VOLC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	if apiKey == "" || endpoint == "" {
		return nil, fmt.Errorf("模型凭证未配置：VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID")
	}
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		Model:   endpoint,
		BaseURL: baseURL,
	})
}

// Summarize 把一段消息列表压成一段中文摘要（供 session 压缩用，纯模型调用、无工具）。
func Summarize(ctx context.Context, messages []string) (string, error) {
	chatModel, err := newChatModel(ctx)
	if err != nil {
		return "", err
	}
	joined := strings.Join(messages, "\n")
	out, err := chatModel.Generate(ctx, []*schema.Message{
		schema.SystemMessage("你是一个对话压缩器。把下面的历史对话压成一段 100 字以内的中文摘要，保留关键事实。"),
		schema.UserMessage(joined),
	})
	if err != nil {
		return "", err
	}
	return out.Content, nil
}

// builtinTools 固定内核的原始工具清单。
// 加了工具只改这一处——系统提示里的"工具指引"是从注册表实时取的，会自动跟上。
func builtinTools() []tool.BaseTool {
	return append([]tool.BaseTool{
		ArchivalSearchTool, DefenseTool, DelegateTool, DelegateTasksTool, SaveLargeContentTool, LoadLargeContentTool,
	}, mcp.Tools()...)
}

var (
	pipelineOnce sync.Once
	pipeline     *guard.Pipeline
)

// toolPipeline 全局唯一的工具流水线（M5）。
func toolPipeline() *guard.Pipeline {
	pipelineOnce.Do(func() {
		// 内部要再跑一轮模型的工具，耗时和其它工具差一个数量级，单独放宽超时
		guard.SetToolTimeout("delegate_task", 10*time.Minute)
		guard.SetToolTimeout("delegate_tasks", 10*time.Minute)
		pipeline = guard.NewPipeline(guardAskHandler)
	})
	return pipeline
}

// AllTools 暴露给模型的完整工具清单：全部套上 M5 工具流水线
// （pre 策略 → guard 单调守卫 → execute 超时/指标 → post 结果改写）。
func AllTools() []tool.BaseTool {
	p := toolPipeline()
	raw := builtinTools()
	out := make([]tool.BaseTool, 0, len(raw))
	for _, t := range raw {
		out = append(out, p.Wrap(t))
	}
	return out
}

// guardAskHandler 流水线判定为 ask 时，把这次工具调用挂起等人工审批。
//
// 这里**不**附带执行计划：被拦下的是"任意一次工具调用"，它还没有被翻译成
// 具体的 argv 或内置动作。于是批准后编排层会**如实拒绝执行**（执行计划非法），
// 而不是假装执行过 —— 这是刻意保留的诚实缺口。
// 把这条链补全（批准后真的把那次工具调用跑起来）属于 HARNESS-TODO 的 P3-2
// （审批升级链），现在先明确地不做，而不是糊过去。
func guardAskHandler(ctx context.Context, call guard.Call, reason string) (string, error) {
	userID, err := getUserID(ctx)
	if err != nil {
		return "", err
	}
	param := call.Args
	if r := []rune(param); len(r) > 200 {
		param = string(r[:200]) + "…"
	}
	if err := (approval.RedisStore{}).SetPending(ctx, userID, approval.PendingAction{
		Action:      call.Tool,
		Param:       param,
		Reason:      reason,
		RequestedAt: time.Now(),
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("⚠️ 工具 %s 被工具流水线拦下并要求人工审批（原因：%s），已挂起。请管理员输入 auth:approve 执行 / auth:reject 取消。", call.Tool, reason), nil
}

// ToolNames 返回当前真实注册的工具名，供 prompt 器官生成"工具指引"片段。
func ToolNames(ctx context.Context) []string {
	tools := AllTools()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name == "" {
			continue
		}
		names = append(names, info.Name)
	}
	return names
}

// 说明：react 版组装函数 BuildEinoAgent 已在阶段 2.2 删除 ——
// 循环的唯一实现是 NewOwnLoop（loop.go），重试/工具装配逻辑也搬到了那里。
// 下面几个 helper（retryPolicyFromEnv / envInt / retryAttemptsSoFar / retryLogger）
// 仍被 NewOwnLoop 复用，所以留在这里。

// retryPolicyFromEnv 允许用环境变量调重试策略 —— 线上发现"重试太凶"（烧钱）
// 或"退避太久"（用户等得着急）时改配置重启即可，不用改代码重编译。
// 全部有默认值，一个都不配也能跑。
func retryPolicyFromEnv() retry.Policy {
	p := retry.DefaultPolicy()
	if n, ok := envInt("RETRY_MAX_ATTEMPTS"); ok && n >= 0 {
		p.MaxAttempts = n
	}
	if n, ok := envInt("RETRY_BASE_DELAY_MS"); ok && n >= 0 {
		p.BaseDelay = time.Duration(n) * time.Millisecond
	}
	if n, ok := envInt("RETRY_MAX_DELAY_MS"); ok && n >= 0 {
		p.MaxDelay = time.Duration(n) * time.Millisecond
	}
	return p
}

// envInt 读一个整型环境变量；没配或不是整数就返回 ok=false（调用方用默认值）。
func envInt(key string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// retryAttemptsSoFar 本轮已经用掉的重试次数（读日志，best-effort）。
// 读不到就当 0 —— 重试预算是保护措施，不该因为它自己失败而把对话打挂。
func retryAttemptsSoFar(ctx context.Context, userID uint) int {
	n, err := session.RedisStore{}.RetryAttemptsInTurn(ctx, userID)
	if err != nil {
		return 0
	}
	return n
}

// retryLogger 把每次重试落成 llm/retry 事件 + 指标。
//
// 为什么非记不可：没有它，用户只会看到"这一轮特别慢"，
// 而"慢是因为上游限流、重试了 3 次"这个事实没有任何痕迹。
// 记账失败也只打日志 —— 绝不能因为"记不下来"就不让对话继续。
func retryLogger(ctx context.Context, userID uint) retry.Observer {
	return func(attempt int, reason string, backoff time.Duration) {
		metrics.Default.Inc("llm_retries_total")
		fmt.Printf(" [重试] 第 %d 次（原因 %s），退避 %s\n", attempt, reason, backoff)
		payload, _ := json.Marshal(map[string]any{
			"attempt":    attempt,
			"reason":     reason,
			"backoff_ms": backoff.Milliseconds(),
		})
		// 注意：这里必须先把 store 取出来再调用 —— 写成
		// `if err := session.RedisStore{}.AppendEvent(...); err != nil` 过不了编译，
		// 因为 if 的控制子句里复合字面量后面的 `{` 会被当成语句块的开始。
		store := session.RedisStore{}
		if err := store.AppendEvent(ctx, userID, session.MemoryDTO{
			Type:    session.EventLLMRetry,
			Role:    "system",
			Content: string(payload),
		}); err != nil {
			fmt.Printf(" [重试] 落盘 llm/retry 事件失败: %v\n", err)
		}
	}
}
