package guard

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// fakeInvokable 一个"能被调用"的假工具，记录被调了几次。
type fakeInvokable struct {
	name  string
	calls int
}

func (t *fakeInvokable) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *fakeInvokable) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	t.calls++
	return "executed", nil
}

// 外部（MCP）工具默认必须人工审批：名字带 `mcp__` 前缀 → ask，而且**绝不许执行**。
func TestExternalToolRequiresApproval(t *testing.T) {
	inner := &fakeInvokable{name: "mcp__myagent__get_weather"}
	p := NewPipeline(func(_ context.Context, _ Call, reason string) (string, error) {
		return "已挂起审批：" + reason, nil
	})

	out, err := p.Wrap(inner).(tool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("ask 应作为正常结果返回（不是 error）: %v", err)
	}
	if inner.calls != 0 {
		t.Fatal("被拦下的外部工具**不许执行** —— 审批之前跑掉就等于没把关")
	}
	if !strings.Contains(out, "审批") {
		t.Fatalf("应返回挂起审批的提示，实际 %q", out)
	}
}

// 自己人（内置工具）照常执行 —— 别把校验做成"一律拒绝"。
func TestInternalToolExecutes(t *testing.T) {
	inner := &fakeInvokable{name: "search_memory_archive"}
	p := NewPipeline(nil)

	out, err := p.Wrap(inner).(tool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if out != "executed" || inner.calls != 1 {
		t.Fatalf("内置工具应被执行一次，实际 out=%q calls=%d", out, inner.calls)
	}
}

// 拿不准又没人能审批时：fail-closed（拒绝），而不是放行。
func TestAskWithoutHandlerFailsClosed(t *testing.T) {
	inner := &fakeInvokable{name: "mcp__x__y"}
	p := NewPipeline(nil) // 没注入 AskHandler

	out, err := p.Wrap(inner).(tool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if inner.calls != 0 {
		t.Fatal("没人能审批时必须拒绝执行")
	}
	if !strings.Contains(out, "拒绝") {
		t.Fatalf("应明确拒绝，实际 %q", out)
	}
}
