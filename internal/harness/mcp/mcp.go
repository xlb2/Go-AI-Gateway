// Package mcp MCP 集成器官：把外部 MCP server 的工具桥进 Eino 统一工具注册表。
//
// 定位：harness 解剖图里的"MCP 集成"（HARNESS-STUDY M10）。
// MCP 集成本质是一个"把外部协议桥进统一工具注册表"的 Consumer——桥完之后，
// 主循环对这套工具"来自 MCP"一无所知，按普通工具对待（走四道关/审批/日志）。
// 命名：工具重命名为 mcp__<serverName>__<rawName>，用 server 名字做命名空间防撞名（M10）。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// protocolVersion 当前 MCP 协议版本。
const protocolVersion = "2025-03-26"

// ConnectFromEnv 按环境变量连接一个 stdio MCP server 并注册进注册表。
// 未配置 MCP_SERVER_COMMAND 时是空操作（MCP 可选）。
// 环境变量：MCP_SERVER_COMMAND(必填) / MCP_SERVER_NAME(默认 ext) / MCP_SERVER_ARGS(逗号分隔)。
func ConnectFromEnv(ctx context.Context) error {
	command := os.Getenv("MCP_SERVER_COMMAND")
	if command == "" {
		return nil
	}
	name := os.Getenv("MCP_SERVER_NAME")
	if name == "" {
		name = "ext"
	}
	var args []string
	if argsStr := os.Getenv("MCP_SERVER_ARGS"); argsStr != "" {
		for _, a := range strings.Split(argsStr, ",") {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, a)
			}
		}
	}
	b, err := Connect(ctx, name, command, args)
	if err != nil {
		return err
	}
	RegisterBridge(b)
	fmt.Printf(" [MCP] 已连接 server=%s 工具数=%d\n", name, len(b.Tools))
	return nil
}

// Bridge 一个已连接的 MCP server + 它桥进来的工具（Eino BaseTool）。
type Bridge struct {
	// ServerName 命名空间前缀（用于 mcp__<serverName>__<tool> 工具名）
	ServerName string
	// Tools 桥进来的 Eino 工具
	Tools []tool.BaseTool

	client *client.Client
}

// Connect 连一个 stdio MCP server，握手、列工具，把它们包成 Eino BaseTool。
// serverName 用作工具名前缀，需唯一。
func Connect(ctx context.Context, serverName, command string, args []string) (*Bridge, error) {
	c, err := client.NewStdioMCPClient(command, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("启动 MCP 子进程失败: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: protocolVersion,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo:      mcp.Implementation{Name: "go-ai-gateway", Version: "0.1.0"},
		},
	}); err != nil {
		c.Close()
		return nil, fmt.Errorf("MCP 握手失败: %v", err)
	}
	list, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("列出 MCP 工具失败: %v", err)
	}

	tools := make([]tool.BaseTool, 0, len(list.Tools))
	for _, t := range list.Tools {
		tools = append(tools, &bridgeTool{
			serverName: serverName,
			name:       t.Name,
			desc:       t.Description,
			input:      t.InputSchema,
			client:     c,
		})
	}
	return &Bridge{ServerName: serverName, Tools: tools, client: c}, nil
}

// Close 断连并清理子进程。
func (b *Bridge) Close() {
	if b.client != nil {
		b.client.Close()
	}
}

// ———— 注册表：agent.BuildEinoAgent 从这里取 MCP 工具 ————

var (
	mu      sync.RWMutex
	bridges []*Bridge
)

// RegisterBridge 注册一个已连接的桥（agent 构建工具列表时用）。
func RegisterBridge(b *Bridge) {
	mu.Lock()
	defer mu.Unlock()
	bridges = append(bridges, b)
}

// Tools 返回所有桥接进来的 Eino 工具（供 agent.BuildEinoAgent 挂进工具列表）。
func Tools() []tool.BaseTool {
	mu.RLock()
	defer mu.RUnlock()
	var out []tool.BaseTool
	for _, b := range bridges {
		out = append(out, b.Tools...)
	}
	return out
}

// ———— 单个 MCP 工具 → Eino InvokableTool ————

// bridgeTool 把单个 MCP 工具包成 Eino 工具。
type bridgeTool struct {
	serverName string
	name       string
	desc       string
	input      mcp.ToolInputSchema
	client     *client.Client
}

// publicName 对外工具名：mcp__<serverName>__<rawName>（命名空间防撞名）。
func (t *bridgeTool) publicName() string {
	return fmt.Sprintf("mcp__%s__%s", t.serverName, t.name)
}

// Info 返回 Eino 工具描述（把 MCP 的 inputSchema 转成 Eino 的 jsonschema）。
func (t *bridgeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	js, err := toEinoSchema(t.input)
	if err != nil {
		return nil, fmt.Errorf("转换工具 %s 的 schema 失败: %v", t.name, err)
	}
	return &schema.ToolInfo{
		Name:        t.publicName(),
		Desc:        t.desc,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(js),
	}, nil
}

// InvokableRun 收到模型发的参数（JSON 字符串），转发给 MCP server 执行，把文本结果取回。
func (t *bridgeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args map[string]any
	if argumentsInJSON != "" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", fmt.Errorf("解析参数失败: %v", err)
		}
	}
	res, err := t.client.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: t.name, Arguments: args},
	})
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), nil
}

// toEinoSchema 把 MCP 的 ToolInputSchema（JSON Schema 形状）转成 Eino 用的 *jsonschema.Schema。
func toEinoSchema(in mcp.ToolInputSchema) (*jsonschema.Schema, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var js *jsonschema.Schema
	if err := json.Unmarshal(data, &js); err != nil {
		return nil, err
	}
	return js, nil
}
