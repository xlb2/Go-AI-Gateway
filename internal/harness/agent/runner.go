package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

// Loop 是 agent 循环的缝。
//
// 调用方（harness.RunAgentTurn、subagent 的子 agent 构造）只依赖这个接口，不依赖具体实现。
// 阶段 2.2 起唯一实现是自研循环 ownLoop（loop.go）—— Eino 的 react.Agent 路径已删除。
// 保留这道缝是为了"循环可替换"这件事本身：以后换实现，调用方一行都不用改。
//
// 签名与 subagent.ChildAgent 完全一致（两者都是"不绑死某个循环实现"的同一道缝）。
type Loop interface {
	Stream(ctx context.Context, input []*schema.Message, opts ...einoagent.AgentOption) (*schema.StreamReader[*schema.Message], error)
}

// NewLoop 返回 agent 循环实现。
//
//	未设 / own → 自研循环（唯一实现）
//	react      → 已删除，报错（不静默、不退回）
//	其它        → 报错（fail loud，避免拼错环境变量后"以为切了其实没切"）
func NewLoop(ctx context.Context) (Loop, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_LOOP"))); v {
	case "", "own":
		return NewOwnLoop(ctx)
	case "react":
		return nil, fmt.Errorf("AGENT_LOOP=react 已删除（阶段 2.2 起循环只有自研实现 own）")
	default:
		return nil, fmt.Errorf("未知的 AGENT_LOOP=%q（唯一可选值：own）", v)
	}
}
