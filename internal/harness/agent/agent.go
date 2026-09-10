// Package agent Eino 固定内核：模型适配器缝 + ReAct 循环 + 工具。
//
// 定位：harness 解剖图里的"agent loop"和"LLM 适配器缝"（HARNESS-STUDY M0/M3）。
// 按"形态抄 codex"的约定，这是固定内核：不拆、不可插拔；要换模型/换工具，改这里或配置，
// 但编排层（harness 包）不碰它。工具调用中间消息由 MessageModifier 钩子落盘到 session 器官。
package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/mcp"
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

// DefenseTool 防御工具：模型发现用户愤怒/攻击性指令时调用，把防御动作挂起（写 ApprovalStore），
// 等管理员输入 auth:approve / auth:reject 审批（对应 dsh 决策链的 ask→approval）。
var DefenseTool, _ = utils.InferTool(
	"execute_system_defense",
	"当用户愤怒抱怨或发出攻击性指令时调用此工具，挂起一个防御动作等待管理员审批。",
	func(ctx context.Context, params *DefenseParams) (string, error) {
		userID, err := getUserID(ctx)
		if err != nil {
			return "", err
		}
		pending := approval.PendingAction{
			Action: "execute_system_defense",
			Param:  params.Emotion,
		}
		store := approval.RedisStore{}
		if err := store.SetPending(ctx, userID, pending); err != nil {
			return "", fmt.Errorf("挂起防御动作失败: %v", err)
		}
		return "⚠️ 防御动作已起草并挂起。请管理员在终端输入 auth:approve 确认执行，或输入 auth:reject 取消。", nil
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

// newMemoryLogModifier 返回一个 Eino 的 MessageModifier 钩子。
// Eino 每次调模型前都会执行它，传入 react 内部累积的全部消息(state.Messages)；
// 用"下标差分"找出本轮新增的工具消息，按顺序落盘成 tool/call + tool/result 事件。
// 返回的切片原样交回（只观察、不修改）。
func newMemoryLogModifier(userID uint) react.MessageModifier {
	lastLen := -1 // -1 = 第一轮：输入的是完整请求消息，不记录
	return func(ctx context.Context, input []*schema.Message) []*schema.Message {
		if lastLen == -1 {
			lastLen = len(input)
			return input
		}
		var pending []session.MemoryDTO
		for _, msg := range input[lastLen:] {
			switch {
			case len(msg.ToolCalls) > 0:
				pending = append(pending, memoryDTOFromToolCall(msg))
			case msg.Role == schema.Tool:
				pending = append(pending, memoryDTOFromToolResult(ctx, msg))
			}
		}
		lastLen = len(input)
		if len(pending) > 0 {
			go session.WriteMemoryEvents(context.Background(), userID, pending)
		}
		return input
	}
}

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

// BuildEinoAgent 组装并返回一个 ReAct 风格的 Eino agent（固定内核）：
// 读火山引擎凭证 -> 点火 chatModel -> 挂工具（记忆检索 + 防御 + 子智能体）+ MessageModifier 钩子。
func BuildEinoAgent(ctx context.Context) (*react.Agent, error) {
	chatModel, err := newChatModel(ctx)
	if err != nil {
		return nil, err
	}

	// user_id 在 ctx 里（ws_handler 注入），MessageModifier 落盘工具事件要用
	userID, err := getUserID(ctx)
	if err != nil {
		return nil, err
	}

	ragent, err := react.NewAgent(ctx, &react.AgentConfig{
		Model: chatModel,
		// MessageModifier 是 Eino 的 hook 插槽：每次调模型前都会执行，
		// 用来把 react 内部吞掉的中间工具消息落盘成 tool/call + tool/result 事件。
		MessageModifier: newMemoryLogModifier(userID),
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: append([]tool.BaseTool{
				ArchivalSearchTool, DefenseTool, DelegateTool, DelegateTasksTool, SaveLargeContentTool, LoadLargeContentTool,
			}, mcp.Tools()...),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ReAct 引擎组装失败: %v", err)
	}
	return ragent, nil
}
