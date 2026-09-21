package knowledge

import (
	"errors"
	"github.com/gin-gonic/gin"
	"strconv"
)

func chatError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	status, message := 500, "会话服务暂时不可用"
	switch {
	case errors.Is(err, ErrNotFound):
		status, message = 404, "会话或资料不存在"
	case errors.Is(err, ErrConflict):
		status, message = 409, ErrConflict.Error()
	case errors.Is(err, ErrInvalid):
		status, message = 400, err.Error()
	}
	c.JSON(status, gin.H{"error": message})
	return true
}
func (chat *Chat) registerConversations(group *gin.RouterGroup) {
	group.GET("/libraries/:library_id/conversations/:conversation_id/turns/:turn_id/evidence/:event_id", func(c *gin.Context) {
		ids := make([]uint64, 4)
		for i, name := range []string{"library_id", "conversation_id", "turn_id", "event_id"} {
			id, ok := resourceID(c, name)
			if !ok {
				return
			}
			ids[i] = id
		}
		reading, err := chat.store.Evidence(c.Request.Context(), c.GetUint("user_id"), ids[0], ids[1], ids[2], ids[3])
		if !chatError(c, err) {
			c.Header("Cache-Control", "no-store")
			c.JSON(200, reading)
		}
	})
	group.POST("/libraries/:library_id/conversations", func(c *gin.Context) {
		library, ok := resourceID(c, "library_id")
		if !ok {
			return
		}
		conversation, err := chat.store.CreateConversation(c.Request.Context(), c.GetUint("user_id"), library)
		if !chatError(c, err) {
			c.JSON(201, conversation)
		}
	})
	group.GET("/libraries/:library_id/conversations", func(c *gin.Context) {
		library, ok := resourceID(c, "library_id")
		if !ok {
			return
		}
		before, _ := strconv.ParseUint(c.Query("before"), 10, 64)
		items, err := chat.store.Conversations(c.Request.Context(), c.GetUint("user_id"), library, before)
		if !chatError(c, err) {
			c.JSON(200, gin.H{"items": items})
		}
	})
	group.GET("/libraries/:library_id/conversations/:conversation_id", func(c *gin.Context) {
		library, ok := resourceID(c, "library_id")
		if !ok {
			return
		}
		id, ok := resourceID(c, "conversation_id")
		if !ok {
			return
		}
		detail, err := chat.store.Conversation(c.Request.Context(), c.GetUint("user_id"), library, id)
		if !chatError(c, err) {
			c.JSON(200, detail)
		}
	})
}
