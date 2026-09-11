package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness/session"
)

// 记忆日志探针：扫描 Redis 里所有 agent:V2:history:<uid> 键，
// 统计每个用户的压缩摘要与自动外存事件，用来验证 M7 两项器官真的生效。
func main() {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx := context.Background()

	keys, err := rdb.Keys(ctx, "agent:V2:history:*").Result()
	if err != nil {
		fmt.Println("扫描键失败:", err)
		return
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(keys[i], "agent:V2:history:"))
		b, _ := strconv.Atoi(strings.TrimPrefix(keys[j], "agent:V2:history:"))
		return a < b
	})
	if len(keys) == 0 {
		fmt.Println("没有任何记忆日志（agent:V2:history:*）")
		return
	}

	for _, key := range keys {
		dataList, err := rdb.LRange(ctx, key, 0, -1).Result()
		if err != nil || len(dataList) == 0 {
			continue
		}
		fmt.Printf("=== %s: %d 条事件 ===\n", key, len(dataList))

		compaction, spillAuto := 0, 0
		res := map[string]int{} // 事件类型分布
		for _, data := range dataList {
			var dto session.MemoryDTO
			json.Unmarshal([]byte(data), &dto)
			typ := dto.Type
			if typ == "" {
				typ = string(dto.Role)
			}
			res[typ]++

			switch {
			case typ == "compaction/summary":
				compaction++
				fmt.Printf("  [compaction/summary] 遮蔽 seq %d..%d\n    摘要: %.90s\n",
					dto.CompactionFrom, dto.CompactionTo, dto.Content)
			case strings.Contains(dto.Content, "工具结果过大已外存"):
				spillAuto++
				fmt.Printf("  [自动spill] %s(%s): %.90s\n", typ, dto.ToolName, dto.Content)
			case typ == "tool/call" || typ == "tool/result":
				fmt.Printf("  [%s] %s: %.60s\n", typ, dto.ToolName, dto.Content)
			}
		}
		// 事件类型统计，便于看清日志构成
		types := make([]string, 0, len(res))
		for t := range res {
			types = append(types, t)
		}
		sort.Strings(types)
		parts := make([]string, 0, len(types))
		for _, t := range types {
			parts = append(parts, fmt.Sprintf("%s=%d", t, res[t]))
		}
		fmt.Printf("  事件分布: %s\n", strings.Join(parts, " "))
		fmt.Printf("  >> 统计: compaction=%d 自动spill=%d\n\n", compaction, spillAuto)
	}
}
