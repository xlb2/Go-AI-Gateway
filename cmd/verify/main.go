package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type rpcClient struct {
	conn *websocket.Conn
	id   int64
}

func login() string {
	http.Post("http://localhost:8080/api/v1/user/register", "application/json",
		bytes.NewBufferString(`{"username":"smoketest","password":"123456"}`))
	resp, _ := http.Post("http://localhost:8080/api/v1/user/login", "application/json",
		bytes.NewBufferString(`{"username":"smoketest","password":"123456"}`))
	data, _ := io.ReadAll(resp.Body)
	var l struct {
		Token string `json:"token"`
	}
	json.Unmarshal(data, &l)
	return l.Token
}

func dial() *rpcClient {
	conn, _, err := websocket.DefaultDialer.Dial("ws://localhost:8080/api/v1/rpc?token="+login(), nil)
	if err != nil {
		fmt.Println("dial err:", err)
		os.Exit(1)
	}
	return &rpcClient{conn: conn}
}

// call 发一个请求，读流式 chunk + 最终响应，返回完整回复。
func (c *rpcClient) call(method string, params any) string {
	c.id++
	c.conn.WriteJSON(map[string]any{"version": "v1", "id": c.id, "method": method, "params": params})
	// 单次调用最坏要等模型生成 2×1000 字，180s 不够。
	// 且 gorilla/websocket 一旦读超时，连接即不可再用（之后每次调用都 timeout），
	// 所以这个上限必须给足，否则一次慢调用会把后面所有用例全带崩。
	c.conn.SetReadDeadline(time.Now().Add(600 * time.Second))
	var reply strings.Builder
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			fmt.Println("read err:", err)
			return reply.String()
		}
		var msg map[string]any
		json.Unmarshal(data, &msg)
		if m, _ := msg["method"].(string); m == "agent/chunk" {
			if p, ok := msg["params"].(map[string]any); ok {
				reply.WriteString(fmt.Sprint(p["chunk"]))
			}
			continue
		}
		if _, ok := msg["id"]; ok {
			if e, has := msg["error"]; has {
				return "[ERROR] " + fmt.Sprint(e)
			}
			if r, ok := msg["result"].(map[string]any); ok {
				if s, ok := r["reply"].(string); ok {
					return s
				}
				return fmt.Sprint(r)
			}
			return string(data)
		}
	}
}

// 用法：
//
//	go run ./cmd/verify        跑全部 6 段
//	go run ./cmd/verify pump   只跑第 6 段（灌对话逼压缩），改压缩逻辑后快速回归用，
//	                           省掉前面几段几千字长文本生成的等待
func main() {
	only := ""
	if len(os.Args) > 1 {
		only = os.Args[1]
	}
	pumpOnly := only == "pump"

	c := dial()
	defer c.conn.Close()

	if !pumpOnly {
		fmt.Println("== 1) 子智能体并行 fan-out ==")
		fmt.Println("回复:", c.call("agent/run", map[string]string{"content": `请必须调用 delegate_tasks 工具，把下面两个独立任务并行交给子智能体执行并汇总：
任务1：用一句话介绍你自己。
任务2：用一句话描述"网关保安"的职责。`}))

		fmt.Println("\n== 2) spill 大内容存取 ==")
		longText := strings.Repeat("这是一段用于测试溢出存储的超长内容，用来验证 spill 自动外存与大内容取回机制。", 30)
		fmt.Println("回复:", c.call("agent/run", map[string]string{"content": `请必须调用 store_large_content 工具，把下面这段内容保存起来：
` + longText}))
		fmt.Println("取回:", c.call("agent/run", map[string]string{"content": `请必须调用 load_large_content 工具，用刚才 store_large_content 返回的定位符取回内容，并告诉我它大概多少字。`}))

		fmt.Println("\n== 3) 防御审批流 ==")
		fmt.Println("回复:", c.call("agent/run", map[string]string{"content": "气死我了！我要投诉你们公司！"}))

		fmt.Println("\n== 4) 审批：auth:approve ==")
		fmt.Println("审批:", c.call("approval/command", map[string]string{"text": "auth:approve"}))

		fmt.Println("\n== 5) 逼出自动 spill（delegate_tasks 产出超长结果）==")
		fmt.Println("回复:", c.call("agent/run", map[string]string{"content": `请必须调用 delegate_tasks 工具并行执行两个任务：
任务1：用中文写一篇不少于1000字的自我介绍，详细全面。
任务2：用中文写一篇不少于1000字介绍"网关保安"的职责，详细全面。`}))
	}

	fmt.Println("\n== 6) 灌入多轮对话逼出上下文压缩 ==")
	// 压缩阈值：可投影消息(user/assistant) 超过 MaxHistory=20 才触发。
	// 一轮 agent/run 产生 1 条 user + 1 条 assistant，所以要多灌几轮把窗口顶出去。
	for i := 1; i <= 10; i++ {
		reply := c.call("agent/run", map[string]string{"content": fmt.Sprintf("闲聊一下，这是第%d条短消息，不需要做任何事。", i)})
		fmt.Printf("  第 %d 轮 -> %.40s\n", i, strings.ReplaceAll(reply, "\n", " "))
	}
	fmt.Println("已灌入 10 轮短对话，压缩窗口已被顶出（用 cmd/probe 查 compaction 计数）")
}