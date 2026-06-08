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

// CheckRateLimit 物理执行：Lua 单线程绝对霸占限流// CheckRateLimit 物理执行：Lua 单线程绝对霸占限流
func CheckRateLimit(ctx context.Context, rdb *redis.Client, userID uint) bool {
	// 逻辑：把用户的访问次数加 1。如果是第一次，设置 10 秒过期。如果次数大于 5，返回 0（拦截），否则返回 1（放行）。
	script := `
			local current  = redis.call('INCR',KEYS[1])
			if tonumber(current) == 1 then 	
					redis.call('EXPIRE',KEYS[1],ARGV[1])
			end
			if tonumber(current) > tonumber(ARGV[2]) then
					return 0
			else 	
					return 1
			end 
	`
	key := fmt.Sprintf("ratelimit:user:%d", userID)

	result, err := rdb.Eval(ctx, script, []string{key}, 10, 5).Result()

	if err != nil {
		fmt.Printf("Redis 执行 Lua 报错：%v\n", err)
		return true
	}
	res, ok := result.(int64)
	if !ok {
		return true
	}
	return res == 1
}
