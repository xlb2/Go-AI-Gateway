package ai_service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 1. 定义极其严谨的弹药结构体（必须和大模型的官方 API 绝对对齐！）
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Stream         bool            `json:"stream"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

// StreamChat 暴露给外部网关的重机枪！
// 入参：护照(apiKey)、门牌号(endpoint)、历史记忆(history)、退弹管(tokenChan)
func StreamChat(ctx context.Context, apiKey, endpoint string, history []Message, tokenChan chan<- string) {
	// 如果不写这行，外面的网关就会一辈子死等这个管子，直接协程死锁爆内存！
	defer close(tokenChan)

	// 2. 组装请求弹药
	reqBody := ChatRequest{
		Model:          endpoint,
		Messages:       history,
		Stream:         true,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
	payloadByte, _ := json.Marshal(reqBody)

	// 3. 瞄准发射
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://ark.cn-beijing.volces.com/api/v3/chat/completions", bytes.NewBuffer(payloadByte))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("❌ AI 请求惨遭物理击落！Error: %v\n", err)
		if resp != nil {
			fmt.Printf("❌ 云端拒收状态码: %d\n", resp.StatusCode)

			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Printf("【云端死亡回执】: %s\n", string(bodyBytes))
		}

		tokenChan <- "【系统异常】AI 基地失联！"
		return
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	for {
		// 一滴一滴地抽水，遇到换行符 '\n' 算一滴
		line, err := reader.ReadString('\n')
		if err != nil {
			break // 水管彻底干了，或者通信结束
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "") {
			continue // 读到了空气，跳过
		}

		// 物理真相：SSE 协议标准规定，流式数据的每一行，前缀必须是 "data: "
		if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimPrefix(line, "data: ")
			// 当大模型对你喊出 "[DONE]"，说明这一句话说完了，子弹打光了！
			if dataStr == "[DONE]" {
				break
			}
			// 动用 JSON 破译机，把这滴水里的那 1 个“字”挖出来
			var chunk map[string]interface{}
			json.Unmarshal([]byte(dataStr), &chunk)
			// 极其凶残的点号透视法：顺着 JSON 的层级，把大模型吐出的那个词挖出来
			choices := chunk["choices"].([]interface{})
			if len(choices) > 0 {
				delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
				if content, ok := delta["content"].(string); ok {
					tokenChan <- content
				}
			}
		}
	}
}
