package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"go_im_gateway/internal/harness/appserver"
)

// RPCConnect app-server 协议接入点：/api/v1/rpc
// 流程：JWT 鉴权 → 升级 WebSocket → 交给 appserver.ServeWS 跑 JSON-RPC 循环。
func RPCConnect(runner appserver.AgentRunner) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString := c.Query("token")
		if tokenString == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请求未携带护照"})
			return
		}
		claims, err := ParseToken(tokenString)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的护照"})
			return
		}
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 和 /ws 保持一致：把 user_id 放进 ctx，agent 的 getUserID 才能读到
		ctx := context.WithValue(context.Background(), "user_id", claims.UserID)
		appserver.ServeWS(ctx, conn, runner, claims.UserID)
	}
}
