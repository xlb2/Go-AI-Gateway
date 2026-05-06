package ai_service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

// 极其关键的架构参数：滑动窗口大小。永远只保留最近 20 条对话，绝对防止大模型 Token 撑爆
const MaxHistory = 20
const MemoryTTL = 24 * time.Hour // 记忆存活时间

type MemoryDTO struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func SaveMessage(ctx context.Context, userID uint, msg *schema.Message) error {
	if Rdb == nil {
		fmt.Println("❌ [记忆中枢] 致命错误：Redis 连接池未挂载")
		return fmt.Errorf("redis client is nil")
	}
	// 1. 降维抽离：把 Eino 的复杂对象，降级为我们自己的纯净 DTO
	dto := MemoryDTO{
		Role:    string(msg.Role),
		Content: msg.Content,
	}
	// 2. 序列化 DTO
	data, err := json.Marshal(dto)
	if err != nil {
		fmt.Printf("❌ [记忆中枢] 记忆序列化崩溃: %v\n", err)
		return err
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	// 开启 Redis Pipeline，减少网络 RTT 延迟，这是高并发压测的得分眼
	pipe := Rdb.Pipeline()
	pipe.RPush(ctx, key, data)
	// LTRIM 截断：只保留数组最后 MaxHistory 个元素 (如 -20 到 -1)
	pipe.LTrim(ctx, key, -MaxHistory, -1)
	// 每次有新对话，自动给记忆续命 24 小时
	pipe.Expire(ctx, key, MemoryTTL)
	_, err = pipe.Exec(ctx)
	if err != nil {
		fmt.Printf("❌ [记忆中枢] 记忆落盘失败: %v\n", err)
	} else {
		// 监听落地回声
		fmt.Printf("💾 [记忆中枢] 成功刻录 1 条新记忆! (Role: %s)\n", dto.Role)
	}
	return err
}

// 记忆提取：在每次对话前，将用户的历史记录完整抽出
func GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)

	// 1. 从 Redis 抽出原始数据
	dataList, err := Rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	var history []*schema.Message
	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			fmt.Printf("⚠️ [记忆中枢] 破译记忆碎片失败: %v\n", err)
			continue
		}
		msg := &schema.Message{
			Role:    schema.RoleType(dto.Role),
			Content: dto.Content,
		}
		history = append(history, msg)
	}
	// 战果汇报
	fmt.Printf("🧠 [记忆中枢] 成功为 UserID %d 唤醒了 %d 条前世记忆！\n", userID, len(history))
	return history, nil
}
