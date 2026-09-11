package fakemodel

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultScenario 内置场景：覆盖测试与 `cmd/verify` 需要的最小行为集合。
//
// 为什么要内置而不是只靠文件：`go run ./cmd/fakemodel` 不传任何参数就能用，
// 降低"想起来去测一下"的门槛 —— 测试基建最大的敌人是麻烦。
//
// 规则按顺序匹配、首个命中即用。两个隐含前提由 Server.pick 保证：
// 最后一条消息是 tool 结果时回兜底（真模型拿到结果后是去组织答案，不会马上再调一次），
// 以及请求不带 tools 时回兜底（压缩器走这里）。
func DefaultScenario() Scenario {
	return Scenario{
		Default: Reply{Text: "收到。"},
		Rules: []Rule{
			// ---- 与 cmd/verify 的六段提示词对齐 ----
			{
				Match:    "delegate_tasks",
				MaxFires: 10, // 第 1 段和第 5 段都含这个词，各触发一次
				Reply: Reply{ToolCalls: []ToolCall{{
					Name:      "delegate_tasks",
					Arguments: `{"tasks":["用一句话介绍你自己。","用一句话描述网关保安的职责。"]}`,
				}}},
			},
			{
				Match:    "store_large_content",
				MaxFires: 10,
				Reply: Reply{ToolCalls: []ToolCall{{
					Name:      "store_large_content",
					Arguments: `{"name":"验证内容","content":"这是一段用于验证溢出存储与大内容取回机制的超长内容。"}`,
				}}},
			},
			{
				Match:    "load_large_content",
				MaxFires: 10,
				// {{last_locator}} 会在运行时被替换成对话里出现过的真实定位符
				Reply: Reply{ToolCalls: []ToolCall{{
					Name:      "load_large_content",
					Arguments: `{"locator":"{{last_locator}}"}`,
				}}},
			},

			// ---- 防御审批（"愤怒"给 e2e 用，"投诉"给 cmd/verify 第 3 段用）----
			{
				Match: "愤怒",
				Reply: Reply{ToolCalls: []ToolCall{{
					Name:      "execute_system_defense",
					Arguments: `{"emotion":"愤怒","threat_level":"high"}`,
				}}},
			},
			{
				Match: "投诉",
				Reply: Reply{ToolCalls: []ToolCall{{
					Name:      "execute_system_defense",
					Arguments: `{"emotion":"愤怒","threat_level":"medium"}`,
				}}},
			},

			// ---- 普通工具调用（验 tool/call 与 tool/result 配对）----
			{
				Match: "查一下历史",
				Reply: Reply{ToolCalls: []ToolCall{{
					Name:      "search_memory_archive",
					Arguments: `{"query":"历史"}`,
				}}},
			},

			// ---- 返回一段超长文本（验工具结果超 spillThreshold 时的自动外存）----
			{
				Match: "写长文",
				Reply: Reply{Text: longText()},
			},
		},
	}
}

// longText 造一段超过 spillThreshold(2000 rune) 的文本，用来逼出自动外存。
func longText() string {
	unit := "这是一段用于验证自动溢出存储的超长内容。"
	out := make([]rune, 0, 2200)
	for len(out) < 2200 {
		out = append(out, []rune(unit)...)
	}
	return string(out)
}

// LoadScenario 从 JSON 文件加载场景。
func LoadScenario(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var sc Scenario
	if err := json.Unmarshal(data, &sc); err != nil {
		return Scenario{}, fmt.Errorf("解析场景 JSON 失败: %w", err)
	}
	if sc.Default.Text == "" && len(sc.Default.ToolCalls) == 0 {
		sc.Default = Reply{Text: "收到。"} // 兜底，免得场景漏配 default 时静默返回空回复
	}
	return sc, nil
}
