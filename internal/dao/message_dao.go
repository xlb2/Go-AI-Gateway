package dao

import (
	"fmt"
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

// 战术动作 3：单方删除（原子版）
// 用 MySQL 的 JSON_ARRAY_APPEND 一步完成"追加用户ID到 DeletedBy"，
// 避免"读出→改→写回"三步在并发下互相覆盖丢数据。
// 如果在 JSON 里已存在该用户，JSON_CONTAINS 直接命中返回。
func (dao *MessageDAO) SoftDeleteByUser(msgID, userID uint) error {
	err := dao.Db.Model(&model.Message{}).
		Where("id = ?", msgID).
		Where("NOT JSON_CONTAINS(deleted_by, ?)", fmt.Sprintf(`"%d"`, userID)). // 如果已含该用户号，跳过
		Update("deleted_by", gorm.Expr("JSON_ARRAY_APPEND(deleted_by, '$', ?)", userID)).
		Error
	return err
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

// 战术动作 4：根据两个用户找到私聊会话（不存在则创建）
// 策略：先查"这两个用户各自参与的 single 会话"，对比交集；找不到就新建。
func (dao *MessageDAO) FindOrCreateSingleConversation(userA, userB uint) (uint, error) {
	// 1. 查 userA 参与的所有 single 会话
	var convA []uint
	dao.Db.Model(&model.ConversationMember{}).
		Select("conversation_id").
		Where("user_id = ?", userA).
		Scan(&convA)
	// 2. 在 userA 的会话里，找 userB 也参与的
	for _, convID := range convA {
		var count int64
		dao.Db.Model(&model.ConversationMember{}).
			Where("conversation_id = ? AND user_id = ?", convID, userB).
			Count(&count)
		if count > 0 {
			return convID, nil // 找到了已有会话
		}
	}

	// 3. 不存在：创建会话 + 两个成员
	conv := model.Conversation{Type: "single"}
	if err := dao.Db.Create(&conv).Error; err != nil {
		return 0, err
	}
	members := []model.ConversationMember{
		{ConversationID: conv.ID, UserID: userA, LastReadMsgID: 0},
		{ConversationID: conv.ID, UserID: userB, LastReadMsgID: 0},
	}
	if err := dao.Db.Create(&members).Error; err != nil {
		return 0, err
	}
	return conv.ID, nil
}

// 战术动作 5：更新某成员在某会话的已读游标
func (dao *MessageDAO) UpdateReadCursor(convID, userID, msgID uint) error {
	return dao.Db.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ?", convID, userID).
		Update("last_read_msg_id", msgID).Error
}

// 迁移：把 conversation_id=0 的老消息按 (from,to) 自动补建会话并挂上去
func (dao *MessageDAO) MigrateLegacyMessages() error {
	var legacy []model.Message
	if err := dao.Db.Where("conversation_id = ?", 0).Find(&legacy).Error; err != nil {
		return err
	}
	// 用 map 记录 (min,max) 用户对 → 会话ID，避免重复建会话
	convCache := make(map[string]uint)
	for _, msg := range legacy {
		a, b := msg.FromUserID, msg.ToUserID
		if a > b {
			a, b = b, a // 保证顺序一致，A-B 和 B-A 是同一个会话
		}
		key := fmt.Sprintf("%d-%d", a, b)
		convID, ok := convCache[key]
		if !ok {
			// 复用 FindOrCreate 逻辑
			id, err := dao.FindOrCreateSingleConversation(msg.FromUserID, msg.ToUserID)
			if err != nil {
				return err
			}
			convID = id
			convCache[key] = convID
		}
		// 把消息挂到会话上
		if err := dao.Db.Model(&model.Message{}).Where("id = ?", msg.ID).
			Update("conversation_id", convID).Error; err != nil {
			return err
		}
	}
	if len(legacy) > 0 {
		fmt.Printf("✅ 历史消息迁移完成：%d 条老消息已补挂会话\n", len(legacy))
	}
	return nil
}
