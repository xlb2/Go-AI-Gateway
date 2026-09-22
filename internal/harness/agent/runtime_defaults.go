package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/mcp"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
)

// RuntimeConfigFromEnv 在基础设施/MCP 初始化后读取一次启动配置。
// Redis 适配器目前仍使用包级客户端；显式 RuntimeConfig 可注入独立 Store。
func RuntimeConfigFromEnv() (RuntimeConfig, error) {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_LOOP"))); v != "" && v != "own" {
		return RuntimeConfig{}, fmt.Errorf("未知的 AGENT_LOOP=%q（唯一可选值：own）", v)
	}
	factory, err := modelFactoryFromEnv()
	if err != nil {
		return RuntimeConfig{}, err
	}
	var extra []tool.InvokableTool
	for _, t := range mcp.Tools() {
		invokable, ok := t.(tool.InvokableTool)
		if !ok {
			return RuntimeConfig{}, fmt.Errorf("MCP tool is not invokable")
		}
		extra = append(extra, invokable)
	}
	return RuntimeConfig{Sessions: session.RedisStore{}, Approvals: approval.RedisStore{}, Spill: spill.RedisStore{},
		NewModel: factory, ExtraTools: extra, MaxSteps: maxStepsFromEnv(), ChildConcurrency: 4,
		Retry: retryPolicyFromEnv(), Guard: guard.Config{ToolTimeouts: map[string]time.Duration{
			"delegate_task": 10 * time.Minute, "delegate_tasks": 10 * time.Minute,
		}},
	}, nil
}

func modelFactoryFromEnv() (ModelFactory, error) {
	cfg := openai.ChatModelConfig{APIKey: os.Getenv("VOLC_ACCESS_KEY"), Model: os.Getenv("VOLC_ENDPOINT_ID"), BaseURL: os.Getenv("VOLC_BASE_URL")}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	if cfg.APIKey == "" || cfg.Model == "" {
		return nil, fmt.Errorf("模型凭证未配置：VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID")
	}
	return func(ctx context.Context) (model.ChatModel, error) {
		copy := cfg
		if isOpenCodeEndpoint(copy.BaseURL) {
			id, err := openCodeSession(ctx)
			if err != nil {
				return nil, err
			}
			copy.HTTPClient = &http.Client{Timeout: copy.Timeout, Transport: openCodeTransport{base: http.DefaultTransport, session: id}}
		}
		return openai.NewChatModel(ctx, &copy)
	}, nil
}
