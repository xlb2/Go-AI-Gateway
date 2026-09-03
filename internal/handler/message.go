package handler

import (
	"go_im_gateway/internal/service"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type MessageHandler struct {
	messageService *service.MessageService
}

func NewMessageHandler(svc *service.MessageService) *MessageHandler {
	return &MessageHandler{messageService: svc}
}

// DeleteMessage 删除消息（单方软删除）
func (h *MessageHandler) DeleteMessage(c *gin.Context) {
	userIDObj, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统异常：获取不到用户信息"})
		return
	}
	userID := userIDObj.(uint)

	// 从路径参数解析消息ID：/api/v1/message/123
	msgID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息ID格式错误"})
		return
	}

	err = h.messageService.SoftDeleteMessage(uint(msgID), userID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// MarkConversationRead 打开会话 → 标记已读（私聊）
func (h *MessageHandler) MarkConversationRead(c *gin.Context) {
	userIDObj, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统异常：获取不到用户信息"})
		return
	}
	userID := userIDObj.(uint)

	convID, err := strconv.ParseUint(c.Param("conversation_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话ID格式错误"})
		return
	}

	err = h.messageService.MarkConversationRead(uint(convID), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已标记已读"})
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
