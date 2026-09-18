package guard

import (
	"context"
	"fmt"
	"time"
)

// Config 是单个流水线的启动配置。构造时复制各表，不读取全局注册表。
// Timeout 为零使用默认值；工具超时覆盖必须为正。空信任表意味着 MCP 需审批。
type Config struct {
	Ask                    AskHandler
	Timeout                time.Duration
	ToolTimeouts           map[string]time.Duration
	RequiredApprovals      map[string]string
	TrustedExternalServers map[string]bool
}

// NewConfiguredPipeline 保留默认入参限制、脱敏和单调守卫。
// Pre/Guard/Post/SetTimeout 仅用于启动装配，运行期间不得并发修改流水线。
func NewConfiguredPipeline(cfg Config) (*Pipeline, error) {
	if cfg.Timeout < 0 {
		return nil, fmt.Errorf("guard timeout must not be negative")
	}
	timeouts := make(map[string]time.Duration, len(cfg.ToolTimeouts))
	for name, duration := range cfg.ToolTimeouts {
		if name == "" || duration <= 0 {
			return nil, fmt.Errorf("guard tool timeout requires a name and positive duration")
		}
		timeouts[name] = duration
	}
	approvals := make(map[string]string, len(cfg.RequiredApprovals))
	for name, reason := range cfg.RequiredApprovals {
		if name == "" {
			return nil, fmt.Errorf("guard approval requires a tool name")
		}
		approvals[name] = reason
	}
	trusted := make(map[string]bool, len(cfg.TrustedExternalServers))
	for server, enabled := range cfg.TrustedExternalServers {
		if server == "" {
			return nil, fmt.Errorf("guard trust requires a server name")
		}
		trusted[server] = enabled
	}
	p := &Pipeline{ask: cfg.Ask, timeout: cfg.Timeout, toolTimeout: func(name string) time.Duration { return timeouts[name] }}
	p.Guard(func(_ context.Context, call Call) Verdict {
		if reason, ok := approvals[call.Tool]; ok {
			return ask(reason)
		}
		return allow()
	})
	p.Guard(func(_ context.Context, call Call) Verdict {
		return checkExternalTool(call, func(server string) bool { return trusted[server] })
	})
	p.Pre(limitArgsSize)
	p.Post(redactSecrets)
	return p, nil
}
