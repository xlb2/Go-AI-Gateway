package e2e

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go_im_gateway/internal/cli"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/workspace"
	"go_im_gateway/test/fakemodel"
)

func TestWorkspaceHarnessRead(t *testing.T) {
	e := newEnv(t, 9160)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// workspace-fact\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tools, err := w.Tools()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExtraTools = append(cfg.ExtraTools, tools...)
	e.h, err = harness.NewConfigured(e.ctx(), cfg, sandbox.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	e.fake.SetScenario(fakemodel.Scenario{Rules: []fakemodel.Rule{{Match: "inspect", Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{
		{Name: "read_file", Arguments: `{"path":"main.go","start_line":2,"lines":1}`},
		{Name: "read_file", Arguments: `{"lines":1, "start_line":2, "path":"main.go"}`},
		{Name: "read_file", Arguments: `{"path":"main.go","start_line":1,"lines":1}`},
	}}}}})
	var output bytes.Buffer
	if err := cli.Run(e.ctx(), e.h, e.uid, io.NopCloser(strings.NewReader("inspect\n/exit\n")), &output, nil); err != nil {
		t.Fatal(err)
	}
	out := output.String()
	if !strings.Contains(out, "Tools: 3 calls") || !strings.Contains(out, "1 more | 0 clipped | 0 limited | 1 repeated") || !strings.Contains(out, "read main.go:2-2") || !strings.Contains(out, "read main.go:1-1") {
		t.Fatalf("missing evidence: %s", out)
	}
	counts := e.countTypes()
	seen := false
	for _, req := range e.fake.Requests() {
		for _, msg := range req.Messages {
			if msg.Role == "tool" && strings.Contains(msg.Content, "workspace-fact") && strings.Contains(msg.Content, `"line":2`) {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("provider did not receive source evidence")
	}
	if counts[session.EventToolCall] != 1 || counts[session.EventToolResult] != 3 {
		t.Fatalf("missing tool evidence: %v", counts)
	}
}
