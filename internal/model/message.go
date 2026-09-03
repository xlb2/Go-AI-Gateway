package model

import "gorm.io/gorm"

type Message struct {
	gorm.Model        // 自动带上 ID, CreatedAt(发送时间), UpdatedAt, DeletedAt
	ConversationID uint   `gorm:"not null;default:0;index"` // 所属会话（0 表示历史遗留，迁移后补齐）
	FromUserID     uint   `gorm:"not null;index"`           // 发射方坐标（加索引，查询更快）
	ToUserID       uint   `gorm:"not null;index"`           // 靶心坐标
	Content        string `gorm:"type:text;not null"`       // 炮弹内容
	IsRead         bool   `gorm:"default:false"`            // 极其核心的状态位：这条消息到底读没读？
	DeletedBy      string `gorm:"default:'[]'"`             // 单方删除标记：谁删了，用户ID就加进这个JSON数组
}
