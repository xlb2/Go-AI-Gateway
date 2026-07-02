package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

func JWTAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请求未携带护照 (Authorization为空)"})
			c.Abort()
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if !(len(parts) == 2 && parts[0] == "Bearer") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "护照格式错误，必须是 Bearer Token"})
			c.Abort()
			return
		}
		claims, err := ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			c.Abort()
			return
		}
		c.Set("user_id", claims.UserID)
		c.Next()
	}
}

func ParseToken(tokenString string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("护照已过期或防伪钢印被篡改: %v", err)
	}

	claims, ok := token.Claims.(*CustomClaims)
	if ok && token.Valid {
		return claims, nil
	}
	return nil, fmt.Errorf("护照载荷解析失败")

}

// RateLimitMiddleware 专供 Gin 使用的 HTTP 限流防盗门
func RateLimitMiddleware(rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 获取当前用户身份 (假设 JWT 鉴权后把 user_id 塞进了 context)
		userIDObj, exists := c.Get("user_id")
		if !exists {
			// 如果没拿到 user_id，按 IP 限流（降级兜底防线）
			userIDObj = c.ClientIP()
		}

		// 构建 Redis 的 Key，比如 rate_limit:user:7
		limitKey := fmt.Sprintf("rate_limit:user:%v", userIDObj)

		// 获取当前物理时间（毫秒）
		now := time.Now().UnixNano() / int64(time.Millisecond)
		windowStart := now - 1000 // 设定时间窗口为前 1000 毫秒（1秒）

		// 2. 开启 Redis Pipeline (管道) 保证多条指令的批量执行与极低网络开销
		pipe := rdb.Pipeline()

		// 第一步：斩杀窗口之外的旧请求 (移除 score 小于 windowStart 的元素)
		pipe.ZRemRangeByScore(c, limitKey, "0", fmt.Sprintf("%d", windowStart))

		// 第二步：将本次请求装入 ZSET (Score 和 Member 都存当前时间戳)
		pipe.ZAdd(c, limitKey, redis.Z{Score: float64(now), Member: now})

		// 第三步：统计当前 1 秒窗口内到底有多少次请求
		countCmd := pipe.ZCard(c, limitKey)

		// 第四步：重置 Key 的过期时间，防止死 Key 占用内存泄露
		pipe.Expire(c, limitKey, 2*time.Second)

		// 统一发射 Pipeline 子弹
		_, err := pipe.Exec(c)
		if err != nil {
			// 架构师铁律：如果 Redis 挂了，绝不能阻断主业务！直接放行并记录日志
			// log.Printf("Redis 限流器发生物理熔断: %v", err)
			c.Next()
			return
		}

		// 3. 校验火力阈值 (比如：每秒最多允许 10 次请求)
		limitThreshold := int64(10)
		if countCmd.Val() > limitThreshold {
			// 核心拦截动作：直接打回 429 状态码，终止后续所有 Handler！
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "高频火力警告！您的子弹打得太快了，触发防线熔断！",
			})
			c.Abort()
			return
		}

		// 检查通过，放行进入业务逻辑
		c.Next()
	}
}
