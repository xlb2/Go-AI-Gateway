package sandbox

import (
	"strings"
	"testing"
)

// 核心不变量：不许把命令交给 shell 解释器。
// 一旦允许 `sh -c <一段字符串>`，argv 化就白做了 —— 注入面、引号转义、
// 代码页问题会全部原路返回（本项目为此踩过一次坑）。
func TestValidateRejectsShellInterpreters(t *testing.T) {
	for _, cmd := range []string{
		"cmd", "cmd.exe", "CMD", "C:\\Windows\\System32\\cmd.exe",
		"sh", "/bin/sh", "/bin/bash", "dash", "zsh",
		"powershell", "powershell.exe", "pwsh",
	} {
		if err := Validate(Request{Command: cmd, Args: []string{"-c", "echo hi"}}); err == nil {
			t.Errorf("%q 应该被拒绝（shell 解释器）", cmd)
		}
	}
}

// 拒绝的理由要写清是"argv 化的要求"，否则调用方只会以为随手拦了一下。
func TestValidateRejectionExplainsWhy(t *testing.T) {
	err := Validate(Request{Command: "sh", Args: []string{"-c", "echo x >> y"}})
	if err == nil {
		t.Fatal("应该被拒")
	}
	if !strings.Contains(err.Error(), "argv") {
		t.Errorf("拒绝理由应说明是 argv 化的要求，实际：%v", err)
	}
}

// 不能误杀：名字里含 "sh" 但不是解释器的命令要放过。
func TestValidateAllowsPlainCommands(t *testing.T) {
	for _, cmd := range []string{"whoami", "ssh", "sha256sum", "git", "/usr/bin/env"} {
		if err := Validate(Request{Command: cmd}); err != nil {
			t.Errorf("%q 是普通命令，不该被拒：%v", cmd, err)
		}
	}
}

func TestValidateRequiresFields(t *testing.T) {
	if err := Validate(Request{}); err == nil {
		t.Error("空计划（没有命令也不是内置动作）应该被拒")
	}
	if err := Validate(Request{Kind: ActionAppendFile}); err == nil {
		t.Error("追加文件动作缺 FilePath 应该被拒")
	}
	if err := Validate(Request{Kind: ActionAppendFile, FilePath: "/tmp/a.log", FileText: "hi"}); err != nil {
		t.Errorf("合法的追加文件动作不该被拒：%v", err)
	}
}

// 显式开关只该在排查问题时临时用；打开后放行。
func TestValidateShellOverride(t *testing.T) {
	t.Setenv("SANDBOX_ALLOW_SHELL", "true")
	if err := Validate(Request{Command: "sh", Args: []string{"-c", "echo hi"}}); err != nil {
		t.Fatalf("显式打开 SANDBOX_ALLOW_SHELL 后应该放行：%v", err)
	}
	// 开关认的是那几个真值，别把 "false" 也当打开
	t.Setenv("SANDBOX_ALLOW_SHELL", "false")
	if err := Validate(Request{Command: "sh"}); err == nil {
		t.Fatal("SANDBOX_ALLOW_SHELL=false 不该放行")
	}
}

// Summary 要一句话说清"要执行什么"（它会写进审批回执给人看）。
func TestSummary(t *testing.T) {
	if got := (Request{Kind: ActionAppendFile, FilePath: "/tmp/a.log"}).Summary(); !strings.Contains(got, "/tmp/a.log") {
		t.Errorf("追加文件动作的摘要要带上路径，实际 %q", got)
	}
	if got := (Request{Command: "whoami"}).Summary(); got != "whoami" {
		t.Errorf("命令摘要 = %q，期望 whoami", got)
	}
	if got := (Request{Command: "git", Args: []string{"status", "--short"}}).Summary(); got != "git status --short" {
		t.Errorf("带参命令摘要 = %q，期望 \"git status --short\"", got)
	}
}
