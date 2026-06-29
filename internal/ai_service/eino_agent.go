package ai_service

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// 1. 严格定义弹药口径（这里的 jsonschema 标签，Eino 会自动解析并喂给大模型）
type DefenseParams struct {
	Emotion string `json:"emotion" jsonschema:"description=用户当前的情绪状态,required"`
}

var toolCallCounter int32 = 0 // 全局雷达：监控调用频率

// 2. 将你手搓的物理函数，封装成 Eino 标准插件！
var DefenseTool, _ = utils.InferTool(
	"execute_system_defense",
	"当检测到用户极度愤怒或发起攻击指令时，调用此工具起草防御动作。",
	func(ctx context.Context, params *DefenseParams) (string, error) {
		userIDVal := ctx.Value("user_id")
		if userIDVal == nil {
			return "", fmt.Errorf("内部错误：上下文中丢失 user_id")
		}

		var userID uint
		switch v := userIDVal.(type) {
		case uint:
			userID = v
		case int:
			userID = uint(v)
		case float64:
			userID = uint(v)
		default:
			return "", fmt.Errorf("内部错误：user_id 类型不匹配")
		}
		pending := PendingAction{
			Action: "execute_system_defense",
			Param:  params.Emotion,
		}
		SetPendingAction(ctx, userID, pending)
		return "⚠️ 防御动作已起草并挂起。请管理员在终端输入 `auth:approve` 确认执行，或输入 `auth:reject` 取消。", nil
	},
)

func BuildEinoAgent(ctx context.Context) (compose.Runnable[[]*schema.Message, any], error) {

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

	// 2. 将机械臂直接挂载到大模型上！
	// 下面是你原本极其优秀的有向无环图（DAG）流转拓扑，逻辑一行不改，保持原样原速运转！
	toolInfo, err := DefenseTool.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf(" 机械臂图纸提取失败: %v", err)
	}
	err = chatModel.BindTools([]*schema.ToolInfo{toolInfo})
	if err != nil {
		return nil, fmt.Errorf(" 机械臂挂载至大模型失败: %v", err)
	}

	toolsNode, _ := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: []tool.BaseTool{DefenseTool}})

	graph := compose.NewGraph[[]*schema.Message, any]()

	graph.AddChatModelNode("LLM", chatModel)
	graph.AddToolsNode("Tools", toolsNode)
	graph.AddEdge(compose.START, "LLM")

	graph.AddBranch("LLM", compose.NewGraphBranch(func(ctx context.Context, msg *schema.Message) (string, error) {
		if len(msg.ToolCalls) > 0 {
			fmt.Println(" [雷达] 捕捉到工具调用，流量切入 Tools 物理车道！")
			return "Tools", nil
		}
		fmt.Println("[雷达] 纯聊天意图，流量直达终点！")
		return compose.END, nil
	}, map[string]bool{"Tools": true, compose.END: true}))

	graph.AddEdge("Tools", compose.END)

	return graph.Compile(ctx)
}
