package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness/session"
)

// runValidate 全量体检：把每条历史事件过一遍写入侧的不变量与格式版本。
//
// 为什么需要它：写入侧校验（P1-4）只拦**新写入**的事件 ——
// 在加上校验之前写进去的脏数据还躺在 Redis 里。这条命令回答的是
// "存量数据到底干不干净"，也是以后加新不变量规则时回归存量数据的办法。
//
// 用法：go run ./cmd/probe validate
func runValidate() int {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx := context.Background()

	keys, err := rdb.Keys(ctx, "agent:V2:history:*").Result()
	if err != nil {
		fmt.Println("扫描键失败:", err)
		return 1
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Println("没有任何记忆日志")
		return 0
	}

	total, bad, tooNew := 0, 0, 0
	for _, key := range keys {
		dataList, err := rdb.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			continue
		}
		dtos := make([]session.MemoryDTO, 0, len(dataList))
		for i, data := range dataList {
			total++
			var dto session.MemoryDTO
			if err := json.Unmarshal([]byte(data), &dto); err != nil {
				bad++
				fmt.Printf("  ❌ %s seq=%d 解不出来: %v\n", key, i, err)
				continue
			}
			if dto.V > session.LogFormatVersion {
				tooNew++
				fmt.Printf("  ⚠️  %s seq=%d 版本 v%d 比本程序(v%d)新\n",
					key, i, dto.V, session.LogFormatVersion)
			}
			if err := session.Validate(dto); err != nil {
				bad++
				fmt.Printf("  ❌ %s seq=%d: %v\n", key, i, err)
			}
			dtos = append(dtos, dto)
		}
		// 序列级校验：step 交替、工具配对 —— 单条 Validate 看不到"顺序"，只能整条扫。
		for _, p := range session.ValidateLog(dtos) {
			if strings.HasPrefix(p, "（提示）") {
				fmt.Printf("  ℹ️  %s %s\n", key, p)
				continue
			}
			bad++
			fmt.Printf("  ❌ %s %s\n", key, p)
		}
	}

	fmt.Printf("\n=== 体检结果 ===\n扫描 %d 个会话 / %d 条事件\n", len(keys), total)
	if bad == 0 && tooNew == 0 {
		fmt.Println("✅ 全部通过：没有违反不变量的事件，也没有本版本读不懂的日志")
		return 0
	}
	fmt.Printf("❌ 违反不变量 %d 条；版本比本程序新 %d 条\n", bad, tooNew)
	return 1
}

// 记忆日志探针：扫描 Redis 里所有 agent:V2:history:<uid> 键，
// 统计每个用户的压缩摘要与自动外存事件，用来验证 M7 两项器官真的生效。
func main() {
	// 子命令：probe validate 做一次全量体检（不变量 + 日志格式版本）。
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		os.Exit(runValidate())
	}

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
