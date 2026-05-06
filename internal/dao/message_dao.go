package dao

import (
	"go_im_gateway/internal/model"

	"gorm.io/gorm"
)

// MessageDAO 是负责所有聊天记录落盘的唯一执行机构
type MessageDAO struct {
	Db *gorm.DB
}

// NewMessageDAO 构造函数：注入 MySQL 引擎
func NewMessageDAO(db *gorm.DB) *MessageDAO {
	return &MessageDAO{Db: db}
}

// 战术动作 1：物理落盘

func (dao *MessageDAO) SaveMessage(msg *model.Message) error {
	// 这里只管存，不管对方在不在，不关心业务
	return dao.Db.Create(msg).Error
}

// 战术动作 2：离线消息吸尘器
func (dao *MessageDAO) GetAndMarkOfflineMessage(userID uint) ([]model.Message, error) {

	var offlineMessage []model.Message

	// 1. 查出发给我的所有未读消息
	err := dao.Db.Where("to_user_id = ? AND is_read = ?", userID, false).
		Order("created_at asc").
		Find(&offlineMessage).Error
	if err != nil || len(offlineMessage) == 0 {
		return offlineMessage, err
	}

	// 2. 极其冷酷的批量翻转状态（避免用 for 循环一条条去更新数据库，那是新手的性能灾难）
	var msgIDs []uint
	for _, msg := range offlineMessage {
		msgIDs = append(msgIDs, msg.ID)
	}
	// GORM 的批量更新语法：一次性把这些 ID 的消息全部标记为已读！

	dao.Db.Model(&model.Message{}).Where("id IN ?", msgIDs).Update("is_read", true)

	return offlineMessage, nil

}
