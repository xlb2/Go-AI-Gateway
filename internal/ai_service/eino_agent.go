package ai_service

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
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

// 当前上下文（滑动窗口）里找不到答案时，模型可以主动调用它去查更早的历史记录
var ArchivalSearchTool, _ = utils.InferTool(
	"search_memory_archive",
	"当用户提到较早之前说过的话、当前对话上下文里找不到时，调用此工具检索完整历史记录。",
	func(ctx context.Context, params *ArchivalSearchParams) (string, error) {
		userID, err := getUserID(ctx)
		if err != nil {
			return "", err
		}

		results, err := SearchArchival(ctx, userID, params.Query, ArchiveDefaultTopK)
		if err != nil {
			return "", fmt.Errorf("归档检索失败: %v", err)
		}
		if len(results) == 0 {
			return "未在历史记录中找到相关内容。", nil
		}

		var sb strings.Builder
		sb.WriteString("检索到以下历史记录：\n")
		for i, msg := range results {
			sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, msg.Role, msg.Content))
		}
		go AppendToolEvent(context.Background(), userID, "search_memory_archive", params.Query, sb.String())
		return sb.String(), nil
	},
)

// DefenseTool 防御工具：模型发现用户愤怒/攻击性指令时调用，把防御动作挂起（写 PendingAction），
// 等管理员在终端输入 auth:approve / auth:reject 审批（对应 dsh 决策链的 ask→approval，见 HARNESS-STUDY M5/M6）。
var DefenseTool, _ = utils.InferTool(
	"execute_system_defense",
	"当用户愤怒抱怨或发出攻击性指令时调用此工具，挂起一个防御动作等待管理员审批。",
	func(ctx context.Context, params *DefenseParams) (string, error) {
		userID, err := getUserID(ctx)
		if err != nil {
			return "", err
		}
		pending := PendingAction{
			Action: "execute_system_defense",
			Param:  params.Emotion,
		}
		if err := SetPendingAction(ctx, userID, pending); err != nil {
			return "", fmt.Errorf("挂起防御动作失败: %v", err)
		}
		return " 防御动作已起草并挂起。请管理员在终端输入 auth:approve 确认执行，或输入 auth:reject 取消。", nil
	},
)

// BuildEinoAgent 组装并返回一个 ReAct 风格的 Eino agent：
// 读取火山引擎的模型凭证 -> 点火 chatModel -> 挂载工具（记忆检索 search_memory_archive + 防御 execute_system_defense）-> 编译成可调用的 Agent。
// 调用方（ws_handler.go）拿到这个 Agent 后，每次用户发消息就调它的 .Stream() 方法跑一轮对话。
func BuildEinoAgent(ctx context.Context) (*react.Agent, error) {

	//  [熔断器] 直接从操作系统的进程环境变量中提取私密配置
	apiKey := os.Getenv("VOLC_ACCESS_KEY")
	endpoint := os.Getenv("VOLC_ENDPOINT_ID")
	baseURL := os.Getenv("VOLC_BASE_URL")

	// 容错兜底：如果本地没有配 BaseURL，自动使用你原本的北京节点默认值
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}

	//  [硬核风控] 生产环境安全断路：一旦发现核心凭证为空，绝不发起网络调用，直接就地熔断
	if apiKey == "" || endpoint == "" {
		return nil, fmt.Errorf(" 极其致命：Eino 点火失败，环境变量 VOLC_ACCESS_KEY 或 VOLC_ENDPOINT_ID 未正确挂载")
	}

	// 1. 点火火山引擎 (Eino 复用了 openai 的标准 API 格式)
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,   // 安全注入
		Model:   endpoint, // 安全注入
		BaseURL: baseURL,
	})
	if err != nil {
		return nil, err
	}

	// 2. 用 Eino 官方的 react.NewAgent 搭 ReAct 循环：
	// 内部会自动做 Tools -> ChatModel 的回环，工具结果能被模型再读一遍组织成自然语言。
	ragent, err := react.NewAgent(ctx, &react.AgentConfig{
		Model: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{ArchivalSearchTool, DefenseTool},
		},
	})
	if err != nil {
		return nil, fmt.Errorf(" ReAct 引擎组装失败: %v", err)
	}

	return ragent, nil
}
