package ai_service

import (
	"context"
	"sync"
)

// PreAgentHook 在 agent 开始处理一条用户消息之前执行。
// 返回 (allow, reply)：allow=false 表示拦截这次 agent 调用，直接把 reply 回给用户。
// 对应 M9 的 waterfall 语义：钩子有"拦/放"的权力（不往下传 = 拦截）。
type PreAgentHook func(ctx context.Context, userID uint, content string) (allow bool, reply string)

// PostAgentHook 在 agent 回复完成后执行，只能观察/记录、不能改流程（对应 M9 的 emit 语义）。
type PostAgentHook func(ctx context.Context, userID uint, reply string)

type preHookEntry struct {
	id   uint64
	hook PreAgentHook
}

type postHookEntry struct {
	id   uint64
	hook PostAgentHook
}

var (
	hookMu       sync.RWMutex
	preHookList  []preHookEntry
	postHookList []postHookEntry
	preNextID    uint64
	postNextID   uint64
)

// RegisterPreAgentHook 注册一个 pre 钩子（按注册顺序执行，先注册的先执行）。
// 返回取消函数（对应 M1 的"可撤销副作用"：卸载时自动移除）。
func RegisterPreAgentHook(h PreAgentHook) func() {
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

// RegisterPostAgentHook 注册一个 post 钩子（按注册顺序执行）。
// 返回取消函数（可撤销副作用）。
func RegisterPostAgentHook(h PostAgentHook) func() {
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

// RunPreAgentHooks 依次执行所有 pre 钩子；第一个返回 allow=false 的钩子拦截本次调用。
// 没有钩子或全部放行时返回 (true, "")。
func RunPreAgentHooks(ctx context.Context, userID uint, content string) (bool, string) {
	hooks := snapshotPreHooks()
	for _, h := range hooks {
		if allow, reply := h(ctx, userID, content); !allow {
			return false, reply
		}
	}
	return true, ""
}

// RunPostAgentHooks 依次执行所有 post 钩子（只观察，不改流程）。
func RunPostAgentHooks(ctx context.Context, userID uint, reply string) {
	hooks := snapshotPostHooks()
	for _, h := range hooks {
		h(ctx, userID, reply)
	}
}

// snapshotPreHooks 拷贝一份当前 pre 钩子列表（读锁下取快照，避免执行时被并发增删改）。
func snapshotPreHooks() []PreAgentHook {
	hookMu.RLock()
	defer hookMu.RUnlock()
	hooks := make([]PreAgentHook, 0, len(preHookList))
	for _, e := range preHookList {
		hooks = append(hooks, e.hook)
	}
	return hooks
}

// snapshotPostHooks 拷贝一份当前 post 钩子列表。
func snapshotPostHooks() []PostAgentHook {
	hookMu.RLock()
	defer hookMu.RUnlock()
	hooks := make([]PostAgentHook, 0, len(postHookList))
	for _, e := range postHookList {
		hooks = append(hooks, e.hook)
	}
	return hooks
}
