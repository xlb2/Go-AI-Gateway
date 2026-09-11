// Package sandbox 沙箱器官（了解级）：高危命令/工具在受控环境里执行。
//
// 定位：harness 解剖图里的"沙箱与审批安全"（HARNESS-STUDY M6）。
// 按"高度定位"：沙箱是真底层，用现成容器/隔离（Docker / Landlock / Seatbelt / ACL），不自己造。
// 本包只交付 seam（接口）+ 一个"无隔离"的占位实现，让你看清器官长什么样；
// 生产必须把 Executor 换成真正的容器/沙箱隔离（如 docker run --read-only --network=none 等）。
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Request 一次受控执行：命令 + 参数 + 工作目录 + 超时。
type Request struct {
	Command string
	Args    []string
	Workdir string
	Timeout time.Duration
}

// Result 执行结果。
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Executor 沙箱执行器接口：在受控环境里跑一条命令/工具调用，返回输出。
// 这是"了解级"的器官缝——换实现（本机/容器/远程隔离）不动上层。
type Executor interface {
	Run(ctx context.Context, req Request) (Result, error)
}

// LocalExecutor 本机直接执行（无隔离）——占位实现！
// ⚠️ 没有任何隔离能力，只用于了解 seam 的形状；生产必须换成容器/沙箱。
type LocalExecutor struct{}

// Run 在宿主机直接跑命令（无隔离）。Timeout 为 0 时用默认 30s。
// 注意：stdout 和 stderr 分开收——原来只用 cmd.Output() 会把 stderr 丢掉，
// 命令失败时调用方只看得到 err.Error()（"exit status 1"），拿不到真正的原因。
func (LocalExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, req.Command, req.Args...)
	cmd.Dir = req.Workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		// 超时要区分出来：调用方需要知道"没跑完"和"跑完但失败"不是一回事
		if runCtx.Err() == context.DeadlineExceeded {
			res.Stderr = fmt.Sprintf("执行超时(%s)：%s", req.Timeout, res.Stderr)
		}
		return res, err
	}
	res.ExitCode = 0
	return res, nil
}
