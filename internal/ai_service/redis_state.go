package ai_service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var Rdb *redis.Client

func InitRedis() {
	Rdb = redis.NewClient(&redis.Options{Addr: "im_redis:6379"})
}

// 定义挂起状态的结构
type PendingAction struct {
	Action string `json:"action"` // 要执行的动作名称
	Param  string `json:"param"`  // 动作的参数
}

// 写入挂起状态 (TTL 5分钟，超时自动作废)
func SetPendingAction(ctx context.Context, userID uint, action PendingAction) error {
	if Rdb == nil {
		return fmt.Errorf("Redis 客户端未初始化")
	}
	data, _ := json.Marshal(action)
	key := fmt.Sprintf("agent:pending:%d", userID)
	return Rdb.Set(ctx, key, data, 5*time.Minute).Err()
}

// 读取挂起状态
func GetpendingAction(ctx context.Context, userID uint) (*PendingAction, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("Redis 客户端未初始化")
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	data, err := Rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var action PendingAction
	json.Unmarshal(data, &action)
	return &action, nil
}

// 清除挂起状态

func ClearPendingAction(ctx context.Context, userID uint) {
	if Rdb == nil {
		return
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	Rdb.Del(ctx, key)
}
