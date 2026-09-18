package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	bridge "go_im_gateway/internal/harness/mcp"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

// The existing test binary runs as a real stdio server in a separate process.
// TestMain routes here before Redis/model setup; stdout is reserved for MCP.
func serveMCPApprovalFixture(path string) int {
	s := server.NewMCPServer("approval-fixture", "1.0.0", server.WithToolCapabilities(false))
	s.AddTool(protocol.NewTool("record", protocol.WithString("value", protocol.Required()), protocol.WithString("mode", protocol.Required())),
		func(_ context.Context, req protocol.CallToolRequest) (*protocol.CallToolResult, error) {
			value, err := req.RequireString("value")
			if err != nil {
				return nil, err
			}
			mode, err := req.RequireString("mode")
			if err != nil {
				return nil, err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return nil, err
			}
			writeErr := json.NewEncoder(f).Encode(value)
			closeErr := f.Close()
			if writeErr != nil {
				return nil, writeErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			switch mode {
			case "error":
				return protocol.NewToolResultError("fixture-mcp-error"), nil
			case "empty-error":
				return protocol.NewToolResultError(""), nil
			default:
				return protocol.NewToolResultText("mcp-recorded:" + value), nil
			}
		})
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestMCPApprovalTransport(t *testing.T) {
	for _, mode := range []string{"success", "reject", "error", "empty-error"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, 9070)
			ctx, cancel := context.WithTimeout(e.ctx(), 20*time.Second)
			defer cancel()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "calls.jsonl")
			b, err := bridge.Connect(ctx, "fixture", binary, []string{"--mcp-approval-fixture", path})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if len(b.Tools) != 1 {
				t.Fatalf("discovered tools=%d", len(b.Tools))
			}
			remote, ok := b.Tools[0].(tool.InvokableTool)
			if !ok {
				t.Fatal("discovered tool is not invokable")
			}
			info, err := remote.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if info.Name != "mcp__fixture__record" {
				t.Fatal(info.Name)
			}
			cfg, err := agent.RuntimeConfigFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			cfg.ExtraTools = []tool.InvokableTool{remote}
			cfg.Guard.TrustedExternalServers = nil
			e.h, err = harness.NewConfigured(ctx, cfg, &recordingExecutor{})
			if err != nil {
				t.Fatal(err)
			}
			value := strings.Repeat("x", 260) + " 中文\n\"quoted\""
			args, err := json.Marshal(map[string]string{"value": value, "mode": mode})
			if err != nil {
				t.Fatal(err)
			}
			e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "waiting"}, Rules: []fakemodel.Rule{{Match: "request remote approval", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{{Name: info.Name, Arguments: string(args)}}}}}})
			if _, err := e.h.RunAgentTurn(ctx, e.uid, "request remote approval", nil); err != nil {
				t.Fatal(err)
			}
			p, err := e.h.Approvals.GetPending(ctx, e.uid)
			if err != nil || p == nil {
				t.Fatalf("pending=%v err=%v", p, err)
			}
			if p.Tool == nil || p.Tool.Arguments != string(args) {
				t.Fatal("full invocation lost")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("remote executed before approval: %v", err)
			}
			command := "auth:approve " + p.ConfirmCode()
			wantState, wantText := approval.Succeeded, "mcp-recorded:"+value
			if mode == "reject" {
				command, wantState, wantText = "auth:reject", approval.Rejected, "审批已拒绝"
			}
			if mode == "error" || mode == "empty-error" {
				wantState, wantText = approval.Unknown, "MCP 工具执行失败"
			}
			wantReply := wantText
			if mode == "success" {
				// The fake model summarizes long results; verify full data in the event and server receipt.
				wantReply = "（据工具结果）mcp-recorded:"
			}
			handled, reply := e.h.HandleApprovalCommand(ctx, e.uid, command)
			if !handled || !strings.Contains(reply, wantReply) {
				t.Fatalf("reply=%s", reply)
			}
			requireExecution(t, e, p.ID, wantState)
			if len(e.fake.Requests()) != 3 {
				t.Fatalf("model requests=%d", len(e.fake.Requests()))
			}
			if e.fake.Requests()[2].MatchedBy != "default(after-tool-result)" {
				t.Fatal("model continuation did not receive a tool result")
			}
			if problems := session.ValidateLog(e.log()); len(problems) != 0 {
				t.Fatal(problems)
			}
			found := false
			for _, event := range e.log() {
				if event.Type == session.EventToolResult && event.ToolCallID == "approval:"+p.ID && strings.Contains(event.Content, wantText) {
					if mode == "success" && event.Content != wantText {
						t.Fatal("stored MCP result differs from full server output")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("MCP outcome missing from paired history")
			}
			e.h.HandleApprovalCommand(ctx, e.uid, command)
			if len(e.fake.Requests()) != 3 {
				t.Fatal("repeated confirmation started another model request")
			}
			data, err := os.ReadFile(path)
			if mode == "reject" {
				if !os.IsNotExist(err) {
					t.Fatalf("rejected call reached server: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var received string
				// Unmarshal also rejects multiple JSON records, detecting duplicate execution.
				if err := json.Unmarshal(data, &received); err != nil {
					t.Fatal(err)
				}
				if received != value {
					t.Fatal("server received altered arguments")
				}
			}
		})
	}
}
