// Package spill 溢出存储器官：把超大内容存起来，只给模型一个不透明定位符 + 取回指引。
//
// 定位：harness 解剖图里"压缩与溢出"的 spill 半边（HARNESS-STUDY M7）——
// 大内容外存、按需取回，不占模型上下文；compaction 压缩的是"旧对话"，spill 处理的是"大块内容"。
// 关键约束：Locator 对模型是不透明字符串，消费者只能照 RetrievalHint 用，禁止解析/拼接。
package spill

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// rdb 本器官的 Redis 客户端（由 Init 注入，main 启动时调用一次）。
var rdb *redis.Client

// Init 注入 Redis 客户端（main 启动时调用）。
func Init(client *redis.Client) {
	rdb = client
}

// spillPrefix 定位符前缀：LoadText 用它校验，防止消费者伪造 locator 读任意 key。
const spillPrefix = "spill:"

// Ref 一次溢出存储的结果：不透明定位符 + 字节数 + 取回指引。
type Ref struct {
	// Locator 不透明定位符（对模型是字符串，禁止解析）
	Locator string
	// Bytes 写入的精确字节数
	Bytes int
	// RetrievalHint 给模型看的取回指引
	RetrievalHint string
}

// Store 溢出存储器官接口。
type Store interface {
	SaveText(ctx context.Context, owner uint, suggestedName, content string) (Ref, error)
	LoadText(ctx context.Context, locator string) (string, error)
}

// RedisStore 是 Store 的 Redis 实现（定位符 = 一个随机的 Redis key）。
type RedisStore struct{}

// SaveText 把完整内容存起来，返回不透明定位符 + 字节数 + 取回指引。
func (RedisStore) SaveText(ctx context.Context, owner uint, suggestedName, content string) (Ref, error) {
	if rdb == nil {
		return Ref{}, fmt.Errorf("redis client is nil")
	}
	hexID := make([]byte, 16)
	if _, err := rand.Read(hexID); err != nil {
		return Ref{}, err
	}
	locator := fmt.Sprintf("%s%d:%s", spillPrefix, owner, hex.EncodeToString(hexID))
	if err := rdb.Set(ctx, locator, content, 0).Err(); err != nil {
		return Ref{}, err
	}
	return Ref{
		Locator:       locator,
		Bytes:         len(content),
		RetrievalHint: fmt.Sprintf("用 load_large_content 工具、传定位符 %s 取回内容", locator),
	}, nil
}

// LoadText 按定位符取回内容（校验前缀，拒绝解析任意 key）。
func (RedisStore) LoadText(ctx context.Context, locator string) (string, error) {
	if rdb == nil {
		return "", fmt.Errorf("redis client is nil")
	}
	if !strings.HasPrefix(locator, spillPrefix) {
		return "", fmt.Errorf("非法定位符（必须以 %s 开头）", spillPrefix)
	}
	content, err := rdb.Get(ctx, locator).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("定位符 %s 不存在", locator)
	}
	if err != nil {
		return "", err
	}
	return content, nil
}
