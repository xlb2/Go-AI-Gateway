// cmd/resume —— 续跑演示：把一个"被崩溃打断的轮次"接着跑完（P3-1 的落点）。
//
// 用法：
//  1) 起服务（真模型）：scripts/run-api.sh
//  2) 让它跑一轮，中途 kill -9 掉（留下"开放轮次"）
//  3) bin/gwresume.exe <userID>   ← 本工具：检出开放轮次 → Repair → 从投影历史续跑
//
// 关键：它**不新增用户消息** —— 这正是"续跑"与"把话重发一遍"的区别。
// 已完成的工具调用与结果从日志投影出来喂给模型，所以已发生的副作用不会被重放。
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/config"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/subagent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: gwresume <userID>")
		os.Exit(2)
	}
	n, err := strconv.ParseUint(os.Args[1], 10, 64)
	if err != nil {
		fmt.Println("userID 必须是整数:", err)
		os.Exit(2)
	}
	userID := uint(n)

	// 从当前目录的 .env 读模型凭证（和 cmd/api 一样的加载方式）。
	config.LoadConfig()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	session.Init(rdb)
	approval.Init(rdb)
	spill.Init(rdb)
	hooks.RegisterDefaultHooks()
	// 子 agent 构造器（和 cmd/api 一样必须注入）：续跑时模型可能又要派活，
	// 不注入的话 delegate_* 会直接失败（这个坑第一次演示就踩到了）。
	subagent.SetRunner(func(ctx context.Context) (subagent.ChildAgent, error) {
		return agent.NewLoop(ctx)
	})

	h := harness.New()
	ctx := context.WithValue(context.Background(), "user_id", userID)

	closed, open, err := h.Sessions.ResumePoint(ctx, userID)
	if err != nil {
		fmt.Println("ResumePoint 失败:", err)
		os.Exit(1)
	}
	fmt.Printf("续跑点：已闭合 step = %d，末尾开放轮次 = %v\n", closed, open)

	reply, resumed, err := h.ResumeTurn(ctx, userID, func(chunk string) {
		fmt.Print(chunk)
	})
	if err != nil {
		fmt.Println("\nResumeTurn 失败:", err)
		os.Exit(1)
	}
	if !resumed {
		fmt.Println("没有可续跑的开放轮次（无需修复）")
		return
	}
	fmt.Printf("\n--- 续跑完成 ---\n回复: %s\n", reply)
}
