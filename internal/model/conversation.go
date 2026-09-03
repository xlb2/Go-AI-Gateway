package model

import "gorm.io/gorm"

// Conversation 会话：一组消息的容器，支持单聊/群聊
type Conversation struct {
	gorm.Model
	Type      string `gorm:"size:16;not null;default:'single'"` // single(单聊) / group(群聊)
	MemberIDs []uint `gorm:"-"`                                  // 便于查询时填充，不加真实列
}

// ConversationMember 会话成员 + 已读游标（生产级已读状态）
type ConversationMember struct {
	gorm.Model
	ConversationID uint `gorm:"not null;index"`       // 所属会话
	UserID         uint `gorm:"not null;index"`       // 成员
	LastReadMsgID  uint `gorm:"default:0"`            // 已读游标：我读到了这一条为止
}