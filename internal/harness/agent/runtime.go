package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/retry"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/subagent"
)

// ModelFactory 每次返回独立模型；主轮次、子任务和摘要不能共享可变的绑定工具状态。
type ModelFactory func(context.Context) (model.ChatModel, error)

// RuntimeConfig 是主/子循环共享的装配配置，不包含某轮的可变状态。
// ExtraTools 必须未包装 guard，且其 Info 在生命周期内稳定；工具须支持并发调用。
// SummaryModel 为 nil 时复用 NewModel 工厂。Retry.MaxAttempts 为零禁用重试。
// Guard.Ask 必须为空，由 Runtime 绑定到 Approvals。
type RuntimeConfig struct {
	Sessions         session.EventStore
	Approvals        approval.Store
	Spill            spill.Store
	NewModel         ModelFactory
	SummaryModel     ModelFactory
	ExtraTools       []tool.InvokableTool
	Guard            guard.Config
	Retry            retry.Policy
	MaxSteps         int
	ChildConcurrency int
}

// Runtime 的工具、策略和存储在构造后固定，每次 NewLoop 创建新的模型和预算。
type Runtime struct {
	cfg     RuntimeConfig
	storage *StorageTools
	tools   []tool.InvokableTool
	names   []string
}

func NewRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	if cfg.NewModel == nil || cfg.MaxSteps <= 0 || cfg.ChildConcurrency <= 0 {
		return nil, fmt.Errorf("runtime requires model factory and positive step/concurrency limits")
	}
	if cfg.Retry.MaxAttempts < 0 || cfg.Retry.BaseDelay < 0 || cfg.Retry.MaxDelay < 0 || cfg.Retry.Jitter < 0 || cfg.Retry.Jitter > 1 {
		return nil, fmt.Errorf("invalid runtime retry policy")
	}
	if cfg.Guard.Ask != nil {
		return nil, fmt.Errorf("runtime owns guard Ask; configure Approvals instead")
	}
	storage, err := NewStorageTools(cfg.Sessions, cfg.Approvals, cfg.Spill)
	if err != nil {
		return nil, err
	}
	if cfg.SummaryModel == nil {
		cfg.SummaryModel = cfg.NewModel
	}
	r := &Runtime{cfg: cfg, storage: storage}
	children, err := NewDelegationTools(func(ctx context.Context) (subagent.ChildAgent, error) {
		return r.NewLoop(ctx)
	}, cfg.ChildConcurrency)
	if err != nil {
		return nil, err
	}
	policy := cfg.Guard
	policy.Ask = storage.Ask
	pipeline, err := guard.NewConfiguredPipeline(policy)
	if err != nil {
		return nil, err
	}
	raw := append(storage.Tools(), children...)
	raw = append(raw, cfg.ExtraTools...)
	seen := make(map[string]bool, len(raw))
	for _, t := range raw {
		if t == nil {
			return nil, fmt.Errorf("nil runtime tool")
		}
		info, err := t.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("runtime tool schema: %w", err)
		}
		if info == nil || info.Name == "" {
			return nil, fmt.Errorf("runtime tool has no name")
		}
		if seen[info.Name] {
			return nil, fmt.Errorf("duplicate runtime tool %q", info.Name)
		}
		seen[info.Name] = true
		r.names = append(r.names, info.Name)
		r.tools = append(r.tools, pipeline.Wrap(t).(tool.InvokableTool))
	}
	// Only the assembled tool set is retained, not the caller's mutable collections.
	r.cfg.ExtraTools = nil
	r.cfg.Guard = guard.Config{}
	return r, nil
}

func (r *Runtime) NewLoop(ctx context.Context) (Loop, error) {
	m, err := r.cfg.NewModel(ctx)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("model factory returned nil")
	}
	userID, err := getUserID(ctx)
	if err != nil {
		return nil, err
	}
	if subagent.IsNested(ctx) {
		userID = 0
	}
	if r.cfg.Retry.MaxAttempts > 0 {
		m = retry.Wrap(m, retry.Config{Policy: r.cfg.Retry,
			InitialAttempt: retryAttemptsWithStore(ctx, userID, r.cfg.Sessions),
			Observer:       retryLoggerWithStore(ctx, userID, r.cfg.Sessions),
		})
	}
	return NewConfiguredLoop(ctx, LoopConfig{Model: m, Tools: r.tools, MaxSteps: r.cfg.MaxSteps,
		WriteEvents: r.cfg.Sessions.AppendEvents, ToolResult: r.storage.ToolResult})
}

func (r *Runtime) ToolNames(context.Context) []string { return append([]string(nil), r.names...) }

// Summarize 使用无工具的新模型，沿用既有摘要提示，不占用主循环重试预算。
func (r *Runtime) Summarize(ctx context.Context, messages []string) (string, error) {
	m, err := r.cfg.SummaryModel(ctx)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", fmt.Errorf("summary model factory returned nil")
	}
	return summarizeWithModel(ctx, m, messages)
}

func summarizeWithModel(ctx context.Context, m model.ChatModel, messages []string) (string, error) {
	out, err := m.Generate(ctx, []*schema.Message{
		schema.SystemMessage("你是一个对话压缩器。把下面的历史对话压成一段 100 字以内的中文摘要，保留关键事实。"),
		schema.UserMessage(strings.Join(messages, "\n")),
	})
	if err != nil {
		return "", err
	}
	if out == nil {
		return "", fmt.Errorf("summary model returned nil message")
	}
	return out.Content, nil
}
