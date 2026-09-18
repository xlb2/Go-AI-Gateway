package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
)

// StorageTools 将显式工具、自动外存和审批挂起绑定到同一组存储。
// 存储实现由调用方管理生命周期，并须支持其预期的并发访问。
type StorageTools struct {
	tools     []tool.InvokableTool
	approvals approval.Store
	spill     spill.Store
}

func NewStorageTools(sessions session.Store, approvals approval.Store, content spill.Store) (*StorageTools, error) {
	if sessions == nil || approvals == nil || content == nil {
		return nil, fmt.Errorf("storage tools require session, approval and spill stores")
	}
	set := &StorageTools{approvals: approvals, spill: content}
	for _, build := range []func() (tool.InvokableTool, error){
		func() (tool.InvokableTool, error) { return newArchivalSearchTool(sessions) },
		func() (tool.InvokableTool, error) { return newDefenseTool(approvals) },
		func() (tool.InvokableTool, error) { return newSaveLargeContentTool(content) },
		func() (tool.InvokableTool, error) { return newLoadLargeContentTool(content) },
	} {
		t, err := build()
		if err != nil {
			return nil, err
		}
		set.tools = append(set.tools, t)
	}
	return set, nil
}

// Tools 返回清单副本；调用方仍须使用 guard 包装工具。
func (s *StorageTools) Tools() []tool.InvokableTool {
	return append([]tool.InvokableTool(nil), s.tools...)
}

// ToolResult 可直接注入 LoopConfig；外存失败沿用完整内容回退。
func (s *StorageTools) ToolResult(ctx context.Context, msg *schema.Message) session.MemoryDTO {
	return toolResultWithStore(ctx, msg, s.spill)
}

// Ask 可直接注入 guard.Config。这里只负责挂起，通用批准执行仍由 F-2 补齐。
func (s *StorageTools) Ask(ctx context.Context, call guard.Call, reason string) (string, error) {
	return askWithStore(ctx, call, reason, s.approvals)
}
