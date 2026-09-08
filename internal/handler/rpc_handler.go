package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

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
		token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})
		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的护照"})
			return
		}
		claims, ok := token.Claims.(*CustomClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "护照载荷损坏"})
			return
		}
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		appserver.ServeWS(context.Background(), conn, runner, claims.UserID)
	}
}
