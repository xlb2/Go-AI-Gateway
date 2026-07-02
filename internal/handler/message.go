package handler

import (
	"go_im_gateway/internal/service"
	"net/http"

	"github.com/gin-gonic/gin"
)

type MessageHandler struct {
	messageService *service.MessageService
}

func NewMessageHandler(svc *service.MessageService) *MessageHandler {
	return &MessageHandler{messageService: svc}
}

// SendPrivateMessage 发送私聊消息
func (h *MessageHandler) SendPrivateMessage(c *gin.Context) {
	fromUserIDObj, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统异常：获取不到用户信息"})
		return
	}
	fromUserID := fromUserIDObj.(uint)

	var req struct {
		ToUserID uint   `json:"to_user_id"`
		Content  string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "弹药规格不符合 JSON 标准"})
		return
	}

	err := h.messageService.SendPrivateMessage(fromUserID, req.ToUserID, req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "子弹卡壳：" + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "子弹已成功打入 RabbitMQ！MQ万岁！"})
}
