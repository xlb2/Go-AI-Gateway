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

// CheckRateLimit 物理执行：基于 ZSET 滑动窗口的单线程原子限流
func CheckRateLimit(ctx context.Context, rdb *redis.Client, userID uint) bool {
	// 限流规则：10秒 (10000毫秒) 内最多 5 次请求
	windowSizeMs := int64(10000)
	maxRequests := 5

	// 1. 提取当前物理时间戳（精确到毫秒）
	now := time.Now().UnixMilli()
	// 2. 划定窗口的起始线
	windowStart := now - windowSizeMs

	// 3. 真正的 ZSET 滑动窗口 Lua 脚本
	script := `
		local key = KEYS[1]
		local window_start = tonumber(ARGV[1])
		local current_time = tonumber(ARGV[2])
		local max_requests = tonumber(ARGV[3])

		-- 动作1：斩断并清理窗口起始线之前的旧记录
		redis.call('ZREMRANGEBYSCORE', key, 0, window_start)

		-- 动作2：清点当前窗口内存活的打卡记录数
		local current_requests = redis.call('ZCARD', key)

		-- 动作3：物理判定
		if current_requests >= max_requests then
			return 0 -- 爆表，拦截！
		else
			-- 没爆表，将当前的毫秒时间戳作为 score 和 member 存入
			redis.call('ZADD', key, current_time, current_time)
			-- 设置物理兜底过期时间，防止死 Key 堆积（稍微大于窗口时间即可）
			redis.call('EXPIRE', key, 20)
			return 1 -- 放行！
		end
	`

	key := fmt.Sprintf("ratelimit:user:%d", userID)

	//  开火！将变量注入 Lua 引擎
	result, err := rdb.Eval(ctx, script, []string{key}, windowStart, now, maxRequests).Result()
	if err != nil {
		fmt.Printf(" Redis 限流器底层执行崩溃：%v\n", err)
		// 工程安全原则：限流器宕机时，默认放行，保障核心业务可用（Fail-open）
		return true
	}

	res, ok := result.(int64)
	if !ok {
		return true
	}

	// 返回 true 表示放行 (1)，false 表示被限流拦截 (0)
	return res == 1
}
