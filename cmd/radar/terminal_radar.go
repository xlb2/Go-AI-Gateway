package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

func main() {
	var token string

	flag.StringVar(&token, "token", "", "请输入你的 JWT 护照")
	flag.Parse()

	gatewayURL := "ws://localhost:8080/api/v1/ws?token=" + token

	fmt.Println("⏳ 正在尝试潜入网关基座...")

	conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, nil)

	if err != nil {
		log.Fatal("❌ 物理连接惨遭拒绝！检查网关是否启动，或 Token 是否过期: ", err)
	}
	defer conn.Close()

	fmt.Println("🟢 潜入成功！光缆已物理锁定！")
	fmt.Println("💡 [战术提示] 直接输入你想对网关说的话并按回车。输入 'exit' 退出。")
	fmt.Println("================================================================")

	go func() {

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				fmt.Println("\n\n🔌 [系统] 物理链路已断开。")
				os.Exit(0)
			}
			fmt.Print(string(message))
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if text == "exit" {
			fmt.Println("🛑 撤退指令已下达，物理销毁光缆...")
			break
		}
		payload := map[string]interface{}{
			"type":       "chat",
			"to_user_id": 999,
			"content":    text,
		}
		msgByte, _ := json.Marshal(payload)
		fmt.Print("\n🤖 [云端算力] 正在倾泻火力: \n> ")
		err = conn.WriteMessage(websocket.TextMessage, msgByte)
		if err != nil {
			log.Println("❌ 弹药发射失败: ", err)
			break
		}
	}
}
