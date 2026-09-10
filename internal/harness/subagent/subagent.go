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
	"sync"

	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

// maxDepth 递归派发深度上限（防无限递归：子 agent 内部还能再派子 agent）。
const maxDepth = 3

// depthKey 上下文里记录当前递归深度（子 agent 的工具 ctx 会带上它）。
const depthKey = "agent_depth"

// depthFrom 读取当前递归深度（默认 0）。
func depthFrom(ctx context.Context) int {
	if v, ok := ctx.Value(depthKey).(int); ok {
		return v
	}
	return 0
}

// withDepth 返回带上指定深度的 ctx。
func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey, d)
}

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
	// Err 子任务失败原因（并行时单个任务失败不阻塞其他任务）
	Err error
}

// Run 派一个子智能体执行 prompt（干净上下文：只有这一条 prompt，无父历史），
// 等它跑完返回完整回复。递归深度超过 maxDepth 会拒绝（防无限递归）。
func Run(ctx context.Context, prompt string) (*Result, error) {
	if depthFrom(ctx) >= maxDepth {
		return nil, fmt.Errorf("子智能体递归深度超过上限 %d", maxDepth)
	}
	if runner == nil {
		return nil, fmt.Errorf("subagent runner 未设置（main 里调 subagent.SetRunner(agent.BuildEinoAgent)）")
	}
	child, err := runner(ctx)
	if err != nil {
		return nil, fmt.Errorf("子 agent 构造失败: %v", err)
	}
	// 子 agent 内部再派活时深度 +1（它的工具 ctx 会带上这个值）
	stream, err := child.Stream(withDepth(ctx, depthFrom(ctx)+1), []*schema.Message{schema.UserMessage(prompt)})
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

// RunParallel 并行派发多个独立子任务（最多 maxConcurrent 个同时跑），按输入顺序返回结果。
// 每个并行任务同样走 Run 的深度检查；单个任务失败不影响其他任务（Err 字段标记）。
func RunParallel(ctx context.Context, tasks []string, maxConcurrent int) []Result {
	results := make([]Result, len(tasks))
	if len(tasks) == 0 {
		return results
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	// worker 池：固定 maxConcurrent 个 goroutine 从任务 channel 取活
	taskCh := make(chan int)
	var wg sync.WaitGroup
	workers := len(tasks)
	if maxConcurrent < workers {
		workers = maxConcurrent
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range taskCh {
				// 每个任务写不同的 results[idx]，索引不重复，无数据竞争
				if r, err := Run(ctx, tasks[idx]); err != nil {
					results[idx] = Result{Err: err}
				} else {
					results[idx] = *r
				}
			}
		}()
	}
	for i := range tasks {
		taskCh <- i
	}
	close(taskCh)
	wg.Wait()
	return results
}
