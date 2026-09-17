package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"go_im_gateway/internal/harness/session"
)

// 独立账号保留验收证据，避免旧会话或预先记住的固定答案制造假通过。
func verifyCompaction() error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	credentials, err := json.Marshal(map[string]string{"username": "cv" + id, "password": id + "-verify"})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var login struct {
		Token string `json:"token"`
	}
	for _, action := range []string{"register", "login"} {
		resp, err := client.Post("http://localhost:8080/api/v1/user/"+action, "application/json", bytes.NewReader(credentials))
		if err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fmt.Errorf("%s HTTP %d", action, resp.StatusCode)
		}
		if action == "login" {
			err = json.NewDecoder(resp.Body).Decode(&login)
		}
		resp.Body.Close()
		if err != nil {
			return err
		}
	}
	parts := strings.Split(login.Token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("login returned invalid token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	// 这里只读取本地服务签发的账号标识；身份认证仍由 RPC 服务端完成。
	var claims struct {
		UserID uint `json:"user_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return err
	}
	if claims.UserID == 0 {
		return fmt.Errorf("login returned no user_id")
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws://localhost:8080/api/v1/rpc?token="+url.QueryEscape(login.Token), nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()
	key := fmt.Sprintf("agent:V2:history:%d", claims.UserID)
	fmt.Printf("验收账号 cv%s，日志 %s（保留，不删除）\n", id, key)
	readLog := func() ([]session.MemoryDTO, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := rdb.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return nil, err
		}
		dtos := make([]session.MemoryDTO, len(raw))
		for i := range raw {
			if err := json.Unmarshal([]byte(raw[i]), &dtos[i]); err != nil {
				return nil, err
			}
		}
		return dtos, nil
	}
	var requestID int64
	call := func(prompt string) (string, error) {
		requestID++
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteJSON(map[string]any{"version": "v1", "id": requestID, "method": "agent/run", "params": map[string]string{"content": prompt}}); err != nil {
			return "", err
		}
		conn.SetReadDeadline(time.Now().Add(180 * time.Second))
		for {
			var response struct {
				ID     int64           `json:"id"`
				Error  json.RawMessage `json:"error"`
				Result struct {
					Reply string `json:"reply"`
				} `json:"result"`
			}
			if err := conn.ReadJSON(&response); err != nil {
				return "", err
			}
			if response.ID != requestID {
				continue
			}
			if len(response.Error) > 0 && string(response.Error) != "null" {
				return "", fmt.Errorf("RPC: %s", response.Error)
			}
			if response.Result.Reply == "" {
				return "", fmt.Errorf("empty RPC reply")
			}
			return response.Result.Reply, nil
		}
	}
	early, recent := "ARCH-"+id[:8], "RECENT-"+id[8:]
	if _, err := call("请记住待办项目的归档编号是 " + early + "。后面会问你。不要调用工具，只回复收到。"); err != nil {
		return err
	}
	dtos, err := readLog()
	if err != nil {
		return err
	}
	earlySeq := -1
	for i, dto := range dtos {
		if dto.Type == session.EventUserMessage && strings.Contains(dto.Content, early) {
			earlySeq = i
			break
		}
	}
	if earlySeq < 0 {
		return fmt.Errorf("early fact not found in Redis; check Redis endpoint")
	}
	covered := false
	for round := 1; round <= 12; round++ {
		filler := fmt.Sprintf("第%d轮临时材料，以下重复内容无须记忆，不要调用工具，只回复收到。", round) + strings.Repeat("这段文字仅用于增加上下文长度，没有新增待办事项。", 55)
		if _, err := call(filler); err != nil {
			return err
		}
		dtos, err = readLog()
		if err != nil {
			return err
		}
		for _, dto := range dtos {
			if dto.Type == session.EventCompactionSummary && dto.CompactionFrom <= earlySeq && dto.CompactionTo >= earlySeq {
				covered = true
			}
		}
		fmt.Printf("填充 %d/12，早期事实已被摘要覆盖=%t\n", round, covered)
		if covered {
			break
		}
	}
	if !covered {
		return fmt.Errorf("未触发覆盖早期事实的压缩；用 MODEL_CONTEXT_WINDOW=4000 重启服务后重试，不能判为通过")
	}
	if _, err := call("请另外记住近期工单编号是 " + recent + "。不要调用工具，只回复收到。"); err != nil {
		return err
	}
	reply, err := call("不要调用工具。请从对话记忆给出归档编号和近期工单编号，只输出 JSON，字段分别为 archive 和 recent；不知道就填 null。")
	if err != nil {
		return err
	}
	fmt.Println("模型回答:", reply)
	dtos, err = readLog()
	if err != nil {
		return err
	}
	for _, dto := range dtos {
		if dto.Type == session.EventToolCall {
			return fmt.Errorf("本场景出现工具调用，不能将答案归因于上下文摘要，请检查日志")
		}
	}
	latestSummary := -1
	recentSeq := -1
	for i, dto := range dtos {
		if dto.Type == session.EventCompactionSummary {
			latestSummary = i
		}
		if dto.Type == session.EventUserMessage && strings.Contains(dto.Content, recent) {
			recentSeq = i
		}
	}
	if latestSummary < 0 || !strings.Contains(dtos[latestSummary].Content, early) {
		return fmt.Errorf("最新摘要没有保留早期编号，不能判为保真通过")
	}
	if recentSeq < 0 || recentSeq <= dtos[latestSummary].CompactionTo {
		return fmt.Errorf("近期事实未处于未压缩尾部，本场景未满足验收前提")
	}
	var answer struct {
		Archive string `json:"archive"`
		Recent  string `json:"recent"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(reply)), &answer); err != nil {
		return fmt.Errorf("回答不是要求的 JSON: %w", err)
	}
	if answer.Archive != early || answer.Recent != recent {
		return fmt.Errorf("事实不匹配：期望 archive=%s recent=%s", early, recent)
	}
	fmt.Println("PASS: 已观察摘要覆盖早期事实，且两项随机事实回答匹配。单次样例通过不代表通用摘要质量。")
	return nil
}
