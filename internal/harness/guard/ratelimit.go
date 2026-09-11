// Package guard 把关器官：在"模型想干的事"和"真让它干"之间插一层策略判定。
//
// 定位：harness 解剖图里的 M5（工具流水线 + 把关）与 M6 的限流半边。
// 从 dsh 借来的三条纪律：
//
//   - **默认值是按代价选的**：限流这种"人为的、可恢复的"过载 → fail-open（放行，
//     别把正常用户挡在门外）；审批这种"没人介入就不该做"的动作 → fail-closed（拒绝）。
//   - **把关只能收紧**：上层可以叠加更多限制，任何一层都不能把别人收紧的结果放回去。
//   - **把关和执行分开**：判定（allow / deny / ask）不做副作用，真正执行是下一层的事。
package guard

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 限流规则：窗口 10 秒内最多 5 次请求。
const (
	rateWindowMs     int64 = 10000
	rateMaxRequests        = 5
	// rateKeyTTL 物理兜底过期，防止死 Key 堆积（略大于窗口即可）。
	rateKeyTTL = 20 * time.Second
)

// 滑动窗口限流 Lua 脚本：清理 → 清点 → 判定 → 打卡，全程在 Redis 单线程里原子完成，
// 避免"查完再写"之间被并发插入。
const rateLimitScript = `
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
			-- 设置物理兜底过期时间，防止死 Key 堆积
			redis.call('EXPIRE', key, 20)
			return 1 -- 放行！
		end
	`

// AllowRequest 基于 ZSET 滑动窗口判断这次请求放不放行。
//
// fail-open：限流器自己崩了（Redis 抖动）就放行，保证核心业务可用 ——
// 限流拦的是"人为的、短时可恢复的"过载，默认值该选"猜错的代价更小"那一边；
// 换成审批那种"没人介入就不该做"的场景，默认值必须反过来（fail-closed）。
func AllowRequest(ctx context.Context, rdb *redis.Client, userID uint) bool {
	if rdb == nil {
		return true
	}
	now := time.Now().UnixMilli()
	windowStart := now - rateWindowMs
	key := fmt.Sprintf("ratelimit:user:%d", userID)

	result, err := rdb.Eval(ctx, rateLimitScript, []string{key}, windowStart, now, rateMaxRequests).Result()
	if err != nil {
		fmt.Printf(" [把关] 限流器底层执行崩溃（fail-open 放行）：%v\n", err)
		return true
	}
	res, ok := result.(int64)
	if !ok {
		return true
	}
	return res == 1 // 1=放行，0=被拦
}
