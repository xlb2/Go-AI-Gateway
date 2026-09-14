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

// Isolation 隔离等级。
//
// 为什么不只用一个 bool：dsh 的本机后端会**诚实上报** full / partial，
// 因为"部分隔离"和"完全没隔离"在安全决策上不是一回事 ——
// 前者可以按策略放行，后者在要求隔离的场景必须拒绝。
type Isolation int

const (
	// IsolationNone 没有任何隔离：就是在宿主机上裸跑。
	IsolationNone Isolation = iota
	// IsolationPartial 有部分约束（例如限制了网络但没限制文件系统）。
	IsolationPartial
	// IsolationFull 完整隔离（容器/沙箱，文件系统与网络都被收紧）。
	IsolationFull
)

func (i Isolation) String() string {
	switch i {
	case IsolationPartial:
		return "partial"
	case IsolationFull:
		return "full"
	default:
		return "none"
	}
}

// Executor 沙箱执行器接口：在受控环境里跑一条命令/工具调用，返回输出。
// 这是"了解级"的器官缝——换实现（本机/容器/远程隔离）不动上层。
//
// **Isolation / Describe 是接口的一部分，不是可选的**：
// 一个不声明自己隔离到什么程度的执行器，和"没隔离但不告诉你"是同义词。
// 把它放进接口里，编译期就逼着实现方表态（对齐 dsh 的 `sandbox.confine()`
// 返回受限 argv 或 fail-closed，明令禁止静默无隔离透传）。
type Executor interface {
	Run(ctx context.Context, req Request) (Result, error)
	// Isolation 你实际提供了什么级别的隔离
	Isolation() Isolation
	// Describe 一句话说明你是怎么跑的（会写进审批回执，给人看）
	Describe() string
}

// LocalExecutor 本机直接执行（无隔离）——占位实现！
// ⚠️ 没有任何隔离能力。它**如实上报** IsolationNone，
// 于是"要求隔离"的场景下会被上层 fail-closed 拒绝执行，而不是偷偷跑掉。
type LocalExecutor struct{}

// Isolation 如实上报：本机直跑等于没有隔离。
func (LocalExecutor) Isolation() Isolation { return IsolationNone }

// Describe 说明自己的真实情况（这句话会出现在审批回执里给人看）。
func (LocalExecutor) Describe() string {
	return "本机直跑（宿主机直接执行，无文件系统/网络隔离）"
}

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
