package ai_service

import (
	"context"
	"fmt"
	"strings"
)

// sensitiveWords 内置钩子的敏感词表（示例，可按需扩展或改成配置/外部词典）。
var sensitiveWords = []string{"诈骗", "赌博"}

// maxAgentInputLen 单条消息最多允许的字数，超出直接拦截（防 token 黑洞）。
const maxAgentInputLen = 2000

// RegisterDefaultHooks 注册项目内置横切钩子（main.go 启动时调用一次）。
// 第一个消费者：敏感词/超长输入拦截（pre）+ 回复统计日志（post）。
// 之后加横切需求（审计、埋点、风控）就往这里挂，不动业务代码。
func RegisterDefaultHooks() {
	RegisterPreAgentHook(checkSensitiveWords)
	RegisterPreAgentHook(checkTooLongInput)
	RegisterPostAgentHook(logReplyStats)
}

// checkSensitiveWords 敏感词检查钩子：命中就拦截这次 agent 调用。
func checkSensitiveWords(ctx context.Context, userID uint, content string) (bool, string) {
	for _, w := range sensitiveWords {
		if strings.Contains(content, w) {
			fmt.Printf(" [横切钩子] UserID %d 消息命中敏感词 %q，已拦截\n", userID, w)
			return false, "检测到敏感内容，本次消息已被拦截。"
		}
	}
	return true, ""
}

// checkTooLongInput 超长输入拦截钩子：单条超过上限直接拒收。
func checkTooLongInput(ctx context.Context, userID uint, content string) (bool, string) {
	if n := len([]rune(content)); n > maxAgentInputLen {
		fmt.Printf(" [横切钩子] UserID %d 消息过长(%d 字)，已拦截\n", userID, n)
		return false, "消息过长，请精简后重试。"
	}
	return true, ""
}

// logReplyStats 回复统计钩子（post）：记录每次 agent 回复的字数，供观察。
func logReplyStats(ctx context.Context, userID uint, reply string) {
	fmt.Printf(" [横切钩子] UserID %d agent 回复 %d 字\n", userID, len([]rune(reply)))
}
