package workspace_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"go_im_gateway/internal/workspace"
)

func setup(t *testing.T) (string, map[string]tool.InvokableTool) {
	t.Helper()
	dir := t.TempDir()
	w, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	ts, err := w.Tools()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]tool.InvokableTool{}
	for _, v := range ts {
		info, err := v.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		m[info.Name] = v
	}
	return dir, m
}
func put(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func invoke(t *testing.T, m map[string]tool.InvokableTool, name, args string) string {
	t.Helper()
	out, err := m[name].InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) || len(out) > 8000 {
		t.Fatalf("invalid output: %s", out)
	}
	return out
}

func TestWorkspaceReadAndSearch(t *testing.T) {
	dir, m := setup(t)
	put(t, dir, "main.go", "package main\n// 中文 needle\nfunc main() {}\n")
	listed := invoke(t, m, "list_files", `{}`)
	if !strings.Contains(listed, "main.go") {
		t.Fatal(listed)
	}
	read := invoke(t, m, "read_file", `{"path":"main.go","start_line":2,"lines":1}`)
	if !strings.Contains(read, `"line":2`) || !strings.Contains(read, "中文 needle") || !strings.Contains(read, `"next":3`) {
		t.Fatal(read)
	}
	search := invoke(t, m, "search_files", `{"query":"needle"}`)
	if !strings.Contains(search, `"line":2`) || !strings.Contains(search, "main.go") {
		t.Fatal(search)
	}
}

func TestWorkspaceEmptySearchFields(t *testing.T) {
	dir, m := setup(t)
	out := invoke(t, m, "search_files", `{"query":"absent"}`)
	if !strings.Contains(out, `"lines":[]`) || !strings.Contains(out, `"skipped":0`) || !strings.Contains(out, `"truncated":false`) {
		t.Fatal(out)
	}
	put(t, dir, ".env", "absent")
	out = invoke(t, m, "search_files", `{"query":"absent"}`)
	if !strings.Contains(out, `"lines":[]`) || !strings.Contains(out, `"skipped":1`) {
		t.Fatal(out)
	}
}

func TestWorkspaceSearchContext(t *testing.T) {
	dir, m := setup(t)
	put(t, dir, "sample.go", "before\nneedle one\nbetween\nneedle two\nafter\nend\n")
	decode := func(args string) []struct {
		Line  int  `json:"line"`
		Match bool `json:"match"`
	} { t.Helper(); var got struct {
		Lines []struct {
			Line  int  `json:"line"`
			Match bool `json:"match"`
		} `json:"lines"`
	}; if err := json.Unmarshal([]byte(invoke(t, m, "search_files", args)), &got); err != nil {
		t.Fatal(err)
	}; return got.Lines }
	lines := decode(`{"query":"needle","context_lines":1}`)
	if len(lines) != 5 {
		t.Fatalf("overlap not merged: %+v", lines)
	}
	for i, line := range lines {
		if line.Line != i+1 || line.Match != (i == 1 || i == 3) {
			t.Fatalf("bad evidence: %+v", lines)
		}
	}
	if got := decode(`{"query":"needle","context_lines":0}`); len(got) != 2 || got[0].Line != 2 || got[1].Line != 4 {
		t.Fatalf("zero context: %+v", got)
	}
	if got := decode(`{"query":"needle"}`); len(got) != 6 {
		t.Fatalf("default context: %+v", got)
	}
	if _, err := m["search_files"].InvokableRun(context.Background(), `{"query":"needle","context_lines":6}`); err == nil {
		t.Fatal("invalid context accepted")
	}
}

func TestWorkspaceResultLimitsDistinct(t *testing.T) {
	dir, m := setup(t)
	put(t, dir, "short.txt", "first\nsecond\n")
	check := func(name, args string, more, clipped, limited bool) {
		t.Helper()
		var got struct {
			HasMore   bool `json:"has_more"`
			Clipped   bool `json:"content_truncated"`
			Limited   bool `json:"scan_limited"`
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(invoke(t, m, name, args)), &got); err != nil {
			t.Fatal(err)
		}
		if got.HasMore != more || got.Clipped != clipped || got.Limited != limited || got.Truncated != (more || clipped || limited) {
			t.Fatalf("wrong limit classification: %+v", got)
		}
	}
	check("read_file", `{"path":"short.txt","lines":1}`, true, false, false)
	put(t, dir, "long.txt", strings.Repeat("x", 1500))
	check("read_file", `{"path":"long.txt"}`, false, true, false)
	put(t, dir, "budget.txt", strings.Repeat(strings.Repeat("x", 900)+"\n", 20))
	check("read_file", `{"path":"budget.txt"}`, true, true, false)
	put(t, dir, "hits.txt", strings.Repeat("needle\n", 60))
	check("search_files", `{"query":"needle","context_lines":0}`, false, false, true)
}

func TestWorkspaceBoundaries(t *testing.T) {
	dir, m := setup(t)
	put(t, dir, ".env", "TOKEN=private")
	put(t, dir, "credentials.json", "private")
	put(t, dir, "main.go", "safe")
	for _, path := range []string{"../outside.go", ".env", "credentials.json", "/etc/passwd", "C:/private", "main.go:stream"} {
		args, _ := json.Marshal(map[string]string{"path": path})
		if _, err := m["read_file"].InvokableRun(context.Background(), string(args)); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	for _, toolName := range []string{"list_files", "search_files"} {
		args := `{}`
		if toolName == "search_files" {
			args = `{"query":"private"}`
		}
		if out := invoke(t, m, toolName, args); strings.Contains(out, "private") || strings.Contains(out, "credentials") || strings.Contains(out, ".env") {
			t.Fatal(out)
		}
	}
	outside := t.TempDir()
	put(t, outside, "outside.go", "outside")
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Fatalf("symlink fixture (run in WSL): %v", err)
	}
	if _, err := m["read_file"].InvokableRun(context.Background(), `{"path":"link.go"}`); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Symlink(".env", filepath.Join(dir, "alias.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := m["read_file"].InvokableRun(context.Background(), `{"path":"alias.go"}`); err == nil {
		t.Fatal("sensitive symlink accepted")
	}
}

func TestWorkspaceLimitsAndCancellation(t *testing.T) {
	dir, m := setup(t)
	put(t, dir, "large.txt", strings.Repeat("x", 1024*1024+1))
	put(t, dir, "binary.txt", "a\x00b")
	for _, path := range []string{"large.txt", "binary.txt"} {
		args, _ := json.Marshal(map[string]string{"path": path})
		if _, err := m["read_file"].InvokableRun(context.Background(), string(args)); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	put(t, dir, "long.txt", strings.Repeat("中", 3000)+"\n"+strings.Repeat("needle\n", 80))
	out := invoke(t, m, "read_file", `{"path":"long.txt"}`)
	if !strings.Contains(out, `"truncated":true`) || !strings.Contains(out, "line truncated") {
		t.Fatal(out)
	}
	out = invoke(t, m, "search_files", `{"query":"needle"}`)
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatal(out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m["search_files"].InvokableRun(ctx, `{"query":"needle"}`); err == nil {
		t.Fatal("cancellation ignored")
	}
}
