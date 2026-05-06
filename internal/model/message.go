package model

import "gorm.io/gorm"

type Message struct {
	gorm.Model        // 自动带上 ID, CreatedAt(发送时间), UpdatedAt, DeletedAt
	FromUserID uint   `gorm:"not null;index"`     // 发射方坐标（加索引，查询更快）
	ToUserID   uint   `gorm:"not null;index"`     // 靶心坐标
	Content    string `gorm:"type:text;not null"` // 炮弹内容
	IsRead     bool   `gorm:"default:false"`      // 极其核心的状态位：这条消息到底读没读？
}
