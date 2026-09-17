// cmd/mcpcheck 按环境变量连一次 MCP server，打印桥进注册表的工具名 —— 验证 MCP 器官真的通。
//
// 为什么单独一个工具：`mcp` 器官的代码一直在，但 `.env` 里从没配过，**从没真连过**（P3-4）。
// 这个命令只做一件事：连上 → 列工具名（`mcp__<server>__<tool>`），不烧 token、不碰模型。
//
// 用法（仓库根，WSL）：
//
//	MCP_SERVER_COMMAND=python3 \
//	MCP_SERVER_ARGS="/mnt/e/agent study/my-agent/my_agent_server.py" \
//	MCP_SERVER_NAME=myagent \
//	./bin/gwmcp
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go_im_gateway/internal/config"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/mcp"
)

func main() {
	config.LoadConfig() // 从当前目录的 .env 补充配置（这里只连 MCP，不需要模型凭证）

	ctx := context.Background()
	if err := mcp.ConnectFromEnv(ctx); err != nil {
		fmt.Println("MCP 连接失败:", err)
		os.Exit(1)
	}

	names := agent.ToolNames(ctx)
	fmt.Printf("\n注册表里的工具（共 %d 个）：\n", len(names))
	ext := 0
	for _, n := range names {
		if strings.HasPrefix(n, "mcp__") {
			ext++
			fmt.Println("★ " + n + "   ← 外部工具：默认被 guard 拦成 ask（需人工审批）")
			continue
		}
		fmt.Println("  " + n)
	}
	fmt.Printf("\nMCP 工具 %d 个。没配 MCP_SERVER_COMMAND 的话这里会是 0。\n", ext)
}
