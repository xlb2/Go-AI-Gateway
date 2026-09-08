// Package approval 审批器官：人在回路高危动作挂起状态机。
//
// 定位：harness 解剖图里的"审批升级"（HARNESS-STUDY M6）——模型想干高危事时先挂起，
// 等管理员在终端输入 auth:approve / auth:reject 才执行/取消。
// 只有一个接口 Store（当前 Redis 实现 RedisStore）；换存储 = 新写一个满足接口的类型。
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// rdb 本器官的 Redis 客户端（由 Init 注入，main 启动时调用一次）。
var rdb *redis.Client

// Init 注入 Redis 客户端（main 启动时调用）。
func Init(client *redis.Client) {
	rdb = client
}

// PendingAction 挂起的高危动作。
type PendingAction struct {
	// Action 要执行的动作名称（如 execute_system_defense）
	Action string `json:"action"`
	// Param 动作的参数（如触发防御的情绪）
	Param string `json:"param"`
}

// Store 审批器官接口：挂起、读取、清除一个高危动作。
type Store interface {
	SetPending(ctx context.Context, userID uint, action PendingAction) error
	GetPending(ctx context.Context, userID uint) (*PendingAction, error)
	ClearPending(ctx context.Context, userID uint)
}

// RedisStore 是 Store 的 Redis 实现（5 分钟 TTL，超时自动作废）。
type RedisStore struct{}

// SetPending 把高危动作挂起（Redis 5 分钟 TTL，超时自动作废）。
func (RedisStore) SetPending(ctx context.Context, userID uint, action PendingAction) error {
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	data, err := json.Marshal(action)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	return rdb.Set(ctx, key, data, 5*time.Minute).Err()
}

// GetPending 读取当前挂起的高危动作；没有挂起时返回 (nil, nil)，不算错误。
func (RedisStore) GetPending(ctx context.Context, userID uint) (*PendingAction, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	data, err := rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var action PendingAction
	if err := json.Unmarshal(data, &action); err != nil {
		return nil, err
	}
	return &action, nil
}

// ClearPending 清除挂起状态（审批完无论通过还是拒绝都清掉）。
func (RedisStore) ClearPending(ctx context.Context, userID uint) {
	if rdb == nil {
		return
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	rdb.Del(ctx, key)
}
