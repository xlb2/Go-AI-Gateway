// Package appserver app-server 协议器官（了解级）：GUI/IDE 驱动 agent 的稳定、版本化、双向、流式 JSON-RPC 契约。
//
// 定位：harness 解剖图里的"app-server 协议"（HARNESS-STUDY M11）。
// v1：JSON-RPC envelope + 流式运行、审批，以及活动运行查询/取消。
// 关键设计：契约带版本（Version="v1"）；agent/run 边跑边以 agent/chunk 通知推送流式回复。
package appserver

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go_im_gateway/internal/harness/runstate"
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

// RunController is optional so existing AgentRunner implementations remain usable.
type RunController interface {
	ActiveRun(uint) (runstate.Info, bool)
	CancelRun(uint, string) bool
}

type wsWriter struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (w *wsWriter) send(msg Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		w.cancel()
		return
	}
	if err := w.conn.WriteJSON(msg); err != nil {
		log.Printf("app-server: 写响应失败: %v", err)
		w.cancel()
	}
}

// ServeWS 在一个已升级的 WebSocket 上跑 JSON-RPC 循环（直到连接断开）。
func ServeWS(ctx context.Context, conn *websocket.Conn, runner AgentRunner, userID uint) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer func() { stopClose(); conn.Close() }()
	w := &wsWriter{conn: conn, cancel: cancel}
	var busy atomic.Bool
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return // 断开
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			w.send(errResponse(nil, -32700, "parse error"))
			continue
		}
		if msg.Version != ProtocolVersion {
			w.send(errResponse(msg.ID, -32600, "unsupported version, expected "+ProtocolVersion))
			continue
		}
		if msg.Method == "" {
			continue // 通知：骨架阶段忽略
		}
		if msg.Method == "agent/run" || msg.Method == "approval/command" {
			if !busy.CompareAndSwap(false, true) {
				w.send(errResponse(msg.ID, -32001, "connection already has an active operation"))
				continue
			}
			go func(msg Message) {
				response := dispatch(ctx, w.send, runner, userID, msg)
				busy.Store(false)
				w.send(response)
			}(msg)
			continue
		}
		w.send(dispatch(ctx, w.send, runner, userID, msg))
	}
}

// dispatch 执行一个请求；只有状态/取消等短控制操作在读循环内调用。
func dispatch(ctx context.Context, send func(Message), runner AgentRunner, userID uint, msg Message) Message {
	switch msg.Method {
	case "agent/status", "agent/cancel":
		controller, ok := runner.(RunController)
		if !ok {
			return errResponse(msg.ID, -32601, "runner does not support run control")
		}
		if msg.Method == "agent/status" {
			info, active := controller.ActiveRun(userID)
			if !active {
				return okResponse(msg.ID, map[string]any{"active": false})
			}
			return okResponse(msg.ID, map[string]any{"active": true, "run": info})
		}
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || strings.TrimSpace(p.RunID) == "" {
			return errResponse(msg.ID, -32602, "run_id is required")
		}
		return okResponse(msg.ID, map[string]bool{"cancel_requested": controller.CancelRun(userID, p.RunID)})
	case "agent/run":
		var p struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errResponse(msg.ID, -32602, "invalid params")
		}
		// 流式：emit 回调把每个 chunk 作为 agent/chunk 通知推给前端
		reply, err := runner.RunAgentTurn(ctx, userID, p.Content, func(chunk string) {
			send(Message{
				Version: ProtocolVersion,
				Method:  "agent/chunk",
				Params:  jsonParams(map[string]any{"chunk": chunk, "request_id": msg.ID}),
			})
		})
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
	return Message{Version: ProtocolVersion, ID: id, Result: jsonParams(v)}
}

func errResponse(id *int64, code int, message string) Message {
	return Message{Version: ProtocolVersion, ID: id, Error: &RPCError{Code: code, Message: message}}
}

// jsonParams 把值序列化成 params/result 的 RawMessage。
func jsonParams(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
