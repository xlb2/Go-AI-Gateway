// Package appserver app-server 协议器官（了解级）：GUI/IDE 驱动 agent 的稳定、版本化、双向 JSON-RPC 契约。
//
// 定位：harness 解剖图里的"app-server 协议"（HARNESS-STUDY M11）。
// 这是 v1 的最小骨架：JSON-RPC envelope + 两个方法 + 请求/响应模型。
// 关键设计：契约带版本（Version="v1"）、双向（审批是 server→client 的反向回调，这里先做成
// client 主动发 approval/command）；流式推送给前端（agent/run 边跑边推）留作下一步。
package appserver

import (
	"context"
	"encoding/json"
	"log"

	"github.com/gorilla/websocket"
)

// ProtocolVersion 当前契约版本。
const ProtocolVersion = "v1"

// RPCError 错误码（借用 JSON-RPC 常见码段）。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Message 一条 wire 消息：请求（带 id）/ 通知（无 id）/ 响应（带 result 或 error）。
type Message struct {
	Version string          `json:"version,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// AgentRunner app-server 驱动的 agent 能力（由 harness.Harness 实现，避免本包反向依赖 harness）。
type AgentRunner interface {
	RunAgentTurn(ctx context.Context, userID uint, content string, emit func(chunk string)) (string, error)
	HandleApprovalCommand(ctx context.Context, userID uint, text string) (handled bool, reply string)
}

// ServeWS 在一个已升级的 WebSocket 上跑 JSON-RPC 循环（直到连接断开）。
func ServeWS(ctx context.Context, conn *websocket.Conn, runner AgentRunner, userID uint) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return // 断开
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			writeError(conn, nil, -32700, "parse error")
			continue
		}
		if msg.Version != ProtocolVersion {
			writeError(conn, msg.ID, -32600, "unsupported version, expected "+ProtocolVersion)
			continue
		}
		if msg.Method == "" {
			continue // 通知：骨架阶段忽略
		}
		writeMessage(conn, dispatch(ctx, runner, userID, msg))
	}
}

// dispatch 按方法名分发（v1 支持：agent/run、approval/command）。
func dispatch(ctx context.Context, runner AgentRunner, userID uint, msg Message) Message {
	switch msg.Method {
	case "agent/run":
		var p struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errResponse(msg.ID, -32602, "invalid params")
		}
		reply, err := runner.RunAgentTurn(ctx, userID, p.Content, nil)
		if err != nil {
			return errResponse(msg.ID, -32000, err.Error())
		}
		return okResponse(msg.ID, map[string]string{"reply": reply})

	case "approval/command":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errResponse(msg.ID, -32602, "invalid params")
		}
		handled, reply := runner.HandleApprovalCommand(ctx, userID, p.Text)
		return okResponse(msg.ID, map[string]any{"handled": handled, "reply": reply})

	default:
		return errResponse(msg.ID, -32601, "method not found: "+msg.Method)
	}
}

func okResponse(id *int64, v any) Message {
	data, _ := json.Marshal(v)
	return Message{Version: ProtocolVersion, ID: id, Result: data}
}

func errResponse(id *int64, code int, message string) Message {
	return Message{Version: ProtocolVersion, ID: id, Error: &RPCError{Code: code, Message: message}}
}

func writeMessage(conn *websocket.Conn, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("app-server: 序列化响应失败: %v", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("app-server: 写响应失败: %v", err)
	}
}

func writeError(conn *websocket.Conn, id *int64, code int, message string) {
	writeMessage(conn, errResponse(id, code, message))
}
