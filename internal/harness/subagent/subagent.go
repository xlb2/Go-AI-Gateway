// Package subagent 子智能体器官：派一个独立上下文的子 agent 跑一次任务，只把结果带回来。
//
// 定位：harness 解剖图里的"子智能体与隔离"（HARNESS-STUDY M8）。
// 一次性 start、中间上下文不回流：子 agent 只拿到这一条 prompt（无父对话历史），
// 它读的所有东西只留在自己的会话里，父 agent 只能拿到最终结果。
// 为避免包依赖环：本包不 import agent，子 agent 的构造器由 main 注入（SetRunner）。
package subagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

// ChildAgent 子 agent 的最小接口（*react.Agent 满足它）。
// 只声明 Stream，不绑死具体实现。
type ChildAgent interface {
	Stream(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (*schema.StreamReader[*schema.Message], error)
}

// Runner 由外部注入的"造子 agent"函数（指向 agent.BuildEinoAgent）。
type Runner func(ctx context.Context) (ChildAgent, error)

// runner 全局子 agent 构造器（main 启动时注入一次）。
var runner Runner

// SetRunner 注入子 agent 构造器（main 启动时调用：subagent.SetRunner(agent.BuildEinoAgent)）。
func SetRunner(r Runner) {
	runner = r
}

// Result 子任务结果。
type Result struct {
	// Output 子 agent 的完整回复
	Output string
}

// Run 派一个子智能体执行 prompt（干净上下文：只有这一条 prompt，无父历史），
// 等它跑完返回完整回复。ctx 取消会中止等待。
func Run(ctx context.Context, prompt string) (*Result, error) {
	if runner == nil {
		return nil, fmt.Errorf("subagent runner 未设置（main 里调 subagent.SetRunner(agent.BuildEinoAgent)）")
	}
	child, err := runner(ctx)
	if err != nil {
		return nil, fmt.Errorf("子 agent 构造失败: %v", err)
	}
	stream, err := child.Stream(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return nil, fmt.Errorf("子 agent 推流失败: %v", err)
	}
	var out strings.Builder
	for {
		msg, err := stream.Recv()
		if err != nil {
			break // 流结束
		}
		if msg.Content != "" {
			out.WriteString(msg.Content)
		}
	}
	return &Result{Output: out.String()}, nil
}
