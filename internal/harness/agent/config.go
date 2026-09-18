package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
)

// LoopConfig 是单次循环的已装配依赖。模型应为本轮独立实例；
// 工具必须已按调用方策略包装（如 guard.Pipeline），这里不读取全局注册表。
type LoopConfig struct {
	Model       model.ChatModel
	Tools       []tool.InvokableTool
	MaxSteps    int
	WriteEvents func(context.Context, uint, []session.MemoryDTO) error
	ToolResult  func(context.Context, *schema.Message) session.MemoryDTO
}

// NewConfiguredLoop 不读取环境或创建存储；所有运行依赖均须显式提供。
func NewConfiguredLoop(ctx context.Context, cfg LoopConfig) (Loop, error) {
	if cfg.Model == nil || cfg.WriteEvents == nil || cfg.ToolResult == nil || cfg.MaxSteps <= 0 {
		return nil, fmt.Errorf("loop requires model, event writer, result mapper and positive max steps")
	}
	tools := append([]tool.InvokableTool(nil), cfg.Tools...)
	byName := make(map[string]tool.InvokableTool, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("nil tool")
		}
		info, err := t.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("tool schema: %w", err)
		}
		if info == nil || info.Name == "" {
			return nil, fmt.Errorf("tool has no name")
		}
		if _, exists := byName[info.Name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", info.Name)
		}
		byName[info.Name] = t
	}
	return &ownLoop{model: cfg.Model, tools: tools, byName: byName, maxSteps: cfg.MaxSteps, writeEvents: cfg.WriteEvents, toolResult: cfg.ToolResult}, nil
}
