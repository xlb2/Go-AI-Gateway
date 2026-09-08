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
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/session"
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
				pending = append(pending, memoryDTOFromToolResult(msg))
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

// memoryDTOFromToolResult 把 Eino 的"工具结果消息"降维成 tool/result 事件。
func memoryDTOFromToolResult(msg *schema.Message) session.MemoryDTO {
	return session.MemoryDTO{
		Type:       session.EventToolResult,
		Role:       string(msg.Role),
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
		ToolName:   msg.ToolName,
		Time:       time.Now(),
	}
}

// BuildEinoAgent 组装并返回一个 ReAct 风格的 Eino agent（固定内核）：
// 读火山引擎凭证 -> 点火 chatModel -> 挂工具（记忆检索 + 防御）+ MessageModifier 钩子。
func BuildEinoAgent(ctx context.Context) (*react.Agent, error) {
	apiKey := os.Getenv("VOLC_ACCESS_KEY")
	endpoint := os.Getenv("VOLC_ENDPOINT_ID")
	baseURL := os.Getenv("VOLC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	if apiKey == "" || endpoint == "" {
		return nil, fmt.Errorf("Eino 点火失败：环境变量 VOLC_ACCESS_KEY 或 VOLC_ENDPOINT_ID 未配置")
	}

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		Model:   endpoint,
		BaseURL: baseURL,
	})
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
			Tools: []tool.BaseTool{ArchivalSearchTool, DefenseTool},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ReAct 引擎组装失败: %v", err)
	}
	return ragent, nil
}
