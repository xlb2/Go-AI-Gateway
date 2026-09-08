// Package hooks 钩子器官：横切需求的插槽（pre 拦截 / post 观察）。
//
// 定位：harness 解剖图里的"钩子/事件/扩展点"（HARNESS-STUDY M9）。
// 加横切需求（敏感词、审计、埋点、风控）= 往这里挂一个函数，业务代码一行不动。
// 语义：PreHook 有"拦/放"的权力（waterfall）；PostHook 只能观察（emit）。
package hooks

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// PreHook 在 agent 开始处理一条用户消息之前执行。
// 返回 (allow, reply)：allow=false 表示拦截这次 agent 调用，直接把 reply 回给用户。
type PreHook func(ctx context.Context, userID uint, content string) (allow bool, reply string)

// PostHook 在 agent 回复完成后执行，只能观察/记录、不能改流程。
type PostHook func(ctx context.Context, userID uint, reply string)

type preHookEntry struct {
	id   uint64
	hook PreHook
}

type postHookEntry struct {
	id   uint64
	hook PostHook
}

var (
	hookMu       sync.RWMutex
	preHookList  []preHookEntry
	postHookList []postHookEntry
	preNextID    uint64
	postNextID   uint64
)

// RegisterPre 注册一个 pre 钩子（按注册顺序执行），返回取消函数（可撤销副作用）。
func RegisterPre(h PreHook) func() {
	hookMu.Lock()
	defer hookMu.Unlock()
	preNextID++
	id := preNextID
	preHookList = append(preHookList, preHookEntry{id: id, hook: h})
	return func() {
		hookMu.Lock()
		defer hookMu.Unlock()
		for i, e := range preHookList {
			if e.id == id {
				preHookList = append(preHookList[:i], preHookList[i+1:]...)
				return
			}
		}
	}
}

// RegisterPost 注册一个 post 钩子，返回取消函数。
func RegisterPost(h PostHook) func() {
	hookMu.Lock()
	defer hookMu.Unlock()
	postNextID++
	id := postNextID
	postHookList = append(postHookList, postHookEntry{id: id, hook: h})
	return func() {
		hookMu.Lock()
		defer hookMu.Unlock()
		for i, e := range postHookList {
			if e.id == id {
				postHookList = append(postHookList[:i], postHookList[i+1:]...)
				return
			}
		}
	}
}

// RunPre 依次执行所有 pre 钩子；第一个返回 allow=false 的钩子拦截本次调用。
func RunPre(ctx context.Context, userID uint, content string) (bool, string) {
	hookMu.RLock()
	hooks := make([]PreHook, 0, len(preHookList))
	for _, e := range preHookList {
		hooks = append(hooks, e.hook)
	}
	hookMu.RUnlock()
	for _, h := range hooks {
		if allow, reply := h(ctx, userID, content); !allow {
			return false, reply
		}
	}
	return true, ""
}

// RunPost 依次执行所有 post 钩子（只观察，不改流程）。
func RunPost(ctx context.Context, userID uint, reply string) {
	hookMu.RLock()
	hooks := make([]PostHook, 0, len(postHookList))
	for _, e := range postHookList {
		hooks = append(hooks, e.hook)
	}
	hookMu.RUnlock()
	for _, h := range hooks {
		h(ctx, userID, reply)
	}
}

// ———— 内置钩子（第一个消费者：敏感词/超长拦截 + 回复统计）————

// sensitiveWords 内置钩子的敏感词表（示例，可按需扩展或改成配置/外部词典）。
var sensitiveWords = []string{"诈骗", "赌博"}

// maxAgentInputLen 单条消息最多允许的字数，超出直接拦截（防 token 黑洞）。
const maxAgentInputLen = 2000

// RegisterDefaultHooks 注册项目内置横切钩子（main 启动时调用一次）。
func RegisterDefaultHooks() {
	RegisterPre(checkSensitiveWords)
	RegisterPre(checkTooLongInput)
	RegisterPost(logReplyStats)
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
