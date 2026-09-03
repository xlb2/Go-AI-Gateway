package ai_service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MaxHistory 是"喂给模型的上限"：GetHistory 每次只取最近 20 条，防止大模型 Token 撑爆。
// 注意：它只在"读"的时候截取；存储层只增不减、永久保存，旧对话仍可被 SearchArchival 查到。
const MaxHistory = 20

// ArchiveDefaultTopK 检索默认返回条数
const ArchiveDefaultTopK = 3

type MemoryDTO struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time"`
}

// SaveMessage 把一条消息追加到唯一的记忆日志（agent:V2:history），只增不减、永久保存。
// 喂给模型时只取最近 MaxHistory 条（见 GetHistory），存储本身不裁剪，所以旧记忆也能被检索到。
func SaveMessage(ctx context.Context, userID uint, msg *schema.Message) error {
	if Rdb == nil {
		fmt.Println(" [记忆中枢] 致命错误：Redis 连接池未挂载")
		return fmt.Errorf("redis client is nil")
	}
	// 1. 降维抽离：把 Eino 的复杂对象，降级为我们自己的纯净 DTO
	dto := MemoryDTO{
		Role:    string(msg.Role),
		Content: msg.Content,
		Time:    time.Now(),
	}
	// 2. 序列化 DTO
	data, err := json.Marshal(dto)
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆序列化崩溃: %v\n", err)
		return err
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)
	pipe := Rdb.Pipeline()
	pipe.RPush(ctx, key, data)
	// 永久保存：不 LTrim、不设 Expire——旧对话要能被 SearchArchival 一直查到。
	// （喂给模型的上限由 GetHistory 取最近 MaxHistory 条控制，与这里存多久无关。）
	_, err = pipe.Exec(ctx)
	if err != nil {
		fmt.Printf(" [记忆中枢] 记忆落盘失败: %v\n", err)
	} else {
		// 监听落地回声
		fmt.Printf(" [记忆中枢] 成功刻录 1 条新记忆! (Role: %s)\n", dto.Role)
	}
	return err
}

// AppendToolEvent 记录一次工具调用（log-only：只记进日志、不喂回模型）。
// 工具函数被真正执行的那一刻，就是天然的记录点：把"哪个工具、什么参数、返回什么"落盘。
// 注意：工具结果想"喂回"模型，必须成对带上它前面的工具调用；现在每轮重建还做不到，所以先只记。
func AppendToolEvent(ctx context.Context, userID uint, toolName, params, result string) error {
	msg := &schema.Message{
		Role:    schema.RoleType("tool"),
		Content: fmt.Sprintf("%s(%s) => %s", toolName, params, result),
	}
	return SaveMessage(ctx, userID, msg)
}

// GetHistory 取出最近 MaxHistory 条消息，喂给模型当上下文。
func GetHistory(ctx context.Context, userID uint) ([]*schema.Message, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)

	// 全量读出来：先筛掉 system，再取最后 MaxHistory 条真消息（顺序不能反：先筛后切）
	dataList, err := Rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	var history []*schema.Message
	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			fmt.Printf(" [记忆中枢] 破译记忆碎片失败: %v\n", err)
			continue
		}
		if dto.Role == "system" || dto.Role == "tool" {
			continue
		}
		msg := &schema.Message{
			Role:    schema.RoleType(dto.Role),
			Content: dto.Content,
		}
		history = append(history, msg)
	}
	// 筛完，再取最后 MaxHistory 条真消息
	if len(history) > MaxHistory {
		history = history[len(history)-MaxHistory:]
	}
	// 战果汇报
	fmt.Printf(" [记忆中枢] 成功为 UserID %d 唤醒了 %d 条前世记忆！\n", userID, len(history))
	return history, nil
}

// tokenize 极简分词：转小写后按非字母数字字符切分，过滤空 token
func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x4e00 && r <= 0x9fff)
	}) {
		if field != "" {
			tokens[field] = true
		}
	}
	return tokens
}

// SearchArchival 在整份记忆日志（就是唯一那份 history 列表）里做词交集检索，取分数最高的 topK 条。
// 思路对应 Phase 14 · 07 课 ArchivalStore.search() 的 Jaccard-like 打分，不引入向量库
func SearchArchival(ctx context.Context, userID uint, query string, topK int) ([]*schema.Message, error) {
	if Rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:V2:history:%d", userID)

	dataList, err := Rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil, nil
	}

	type scoredMsg struct {
		msg   *schema.Message
		score int
	}
	var scored []scoredMsg
	for _, data := range dataList {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(data), &dto); err != nil {
			continue
		}
		if dto.Role == "system" || dto.Role == "tool" {
			continue
		}
		msgTokens := tokenize(dto.Content)
		score := 0
		for t := range queryTokens {
			if msgTokens[t] {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredMsg{
				msg:   &schema.Message{Role: schema.RoleType(dto.Role), Content: dto.Content},
				score: score,
			})
		}
	}

	// 按分数降序排序（简单冒泡即可，历史消息量级不大）
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	if topK <= 0 {
		topK = ArchiveDefaultTopK
	}
	if len(scored) > topK {
		scored = scored[:topK]
	}

	results := make([]*schema.Message, 0, len(scored))
	for _, s := range scored {
		results = append(results, s.msg)
	}
	fmt.Printf(" [归档检索] UserID %d 查询 %q 命中 %d 条\n", userID, query, len(results))
	return results, nil
}
