package bootstrap

import (
	"go_im_gateway/internal/handler"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// InitGinRouter 组装所有HTTP路由，返回Gin引擎实例
func InitGinRouter(
	userHandler *handler.UserHandler,
	messageService *service.MessageService,
	rdb *redis.Client,
) *gin.Engine {
	r := gin.Default()

	// 启动WebSocket心跳检测
	handler.StartHeartbeatChecker()

	v1 := r.Group("api/v1")
	{
		// 用户模块（公开接口）
		userGroup := v1.Group("/user")
		{
			userGroup.POST("/register", userHandler.Register)
			userGroup.POST("/login", userHandler.Login)
		}

		// 消息模块（需JWT鉴权）
		messageGroup := v1.Group("/message")
		messageGroup.Use(
			handler.JWTAuthMiddleware(),
			handler.RateLimitMiddleware(rdb),
		)
		{
			messageHandler := handler.NewMessageHandler(messageService)
			messageGroup.POST("/send", messageHandler.SendPrivateMessage)
			messageGroup.DELETE("/:id", messageHandler.DeleteMessage)
			messageGroup.POST("/:conversation_id/read", messageHandler.MarkConversationRead)
		}

		// WebSocket 升级端点
		v1.GET("/ws", handler.ConnectWS(messageService, rdb))

		// app-server 协议端点：JSON-RPC v1 双向契约（驱动 agent/审批）
		v1.GET("/rpc", handler.RPCConnect(harness.Default))
	}

	return r
}
