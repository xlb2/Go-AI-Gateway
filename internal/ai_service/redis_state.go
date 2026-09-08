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

// PendingAction 挂起状态：agent 发起的高危动作先不执行，等管理员在终端输入
// auth:approve / auth:reject 审批。对应 dsh 的"审批升级"（AskForApproval→Decision），
// 见 HARNESS-STUDY.md M6。
type PendingAction struct {
	// Action 要执行的动作名称（如 execute_system_defense）
	Action string `json:"action"`
	// Param 动作的参数（如触发防御的情绪）
	Param string `json:"param"`
}

// SetPendingAction 把一个高危动作挂起（Redis 5 分钟 TTL，超时自动作废）。
func SetPendingAction(ctx context.Context, userID uint, action PendingAction) error {
	if Rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	data, err := json.Marshal(action)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	return Rdb.Set(ctx, key, data, 5*time.Minute).Err()
}

// GetpendingAction 读取当前挂起的高危动作；没有挂起时返回 (nil, nil)，不算错误。
func GetpendingAction(ctx context.Context, userID uint) (*PendingAction, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	data, err := Rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // 没有挂起动作
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

// ClearPendingAction 清除挂起状态（审批完无论通过还是拒绝都清掉）。
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
