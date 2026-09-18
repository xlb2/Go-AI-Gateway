package harness

import (
	"context"
	"fmt"

	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/sandbox"
)

// NewConfigured 统一装配主循环、子循环、摘要、工具与存储。
// 构造后不要替换 Harness 的公开存储字段；需要另一组依赖时重新构造。
func NewConfigured(ctx context.Context, cfg agent.RuntimeConfig, executor sandbox.Executor) (*Harness, error) {
	if executor == nil {
		return nil, fmt.Errorf("harness requires executor")
	}
	runtime, err := agent.NewRuntime(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewWithDependencies(Dependencies{Sessions: cfg.Sessions, Approvals: cfg.Approvals, Executor: executor,
		NewLoop: runtime.NewLoop, Summarize: runtime.Summarize, ToolNames: runtime.ToolNames})
}

// NewFromEnv 是 API 启动入口；应在加载环境、连接基础设施和发现 MCP 工具后调用。
func NewFromEnv(ctx context.Context) (*Harness, error) {
	cfg, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return NewConfigured(ctx, cfg, sandbox.FromEnv())
}
