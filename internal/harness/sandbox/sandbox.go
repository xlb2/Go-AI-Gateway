// Package sandbox 沙箱器官（了解级）：高危命令/工具在受控环境里执行。
//
// 定位：harness 解剖图里的"沙箱与审批安全"（HARNESS-STUDY M6）。
// 按"高度定位"：沙箱是真底层，用现成容器/隔离（Docker / Landlock / Seatbelt / ACL），不自己造。
// 本包只交付 seam（接口）+ 一个"无隔离"的占位实现，让你看清器官长什么样；
// 生产必须把 Executor 换成真正的容器/沙箱隔离（如 docker run --read-only --network=none 等）。
package sandbox

import (
	"context"
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
func (LocalExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, req.Command, req.Args...)
	cmd.Dir = req.Workdir
	out, err := cmd.Output()
	if err != nil {
		code := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		return Result{Stderr: err.Error(), ExitCode: code}, err
	}
	return Result{Stdout: string(out), ExitCode: 0}, nil
}
