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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ActionKind 受控执行的两种形态。
type ActionKind string

const (
	// ActionExec 执行一条外部命令（argv 形态，**不经过 shell**）。
	// 空字符串也按它处理（保持老调用点的行为）。
	ActionExec ActionKind = "exec"
	// ActionAppendFile 内置动作：把一段文本追加到文件。
	//
	// 为什么需要它：有些动作本质就是"写文件"，用 `sh -c 'echo x >> y'` 去实现，
	// 等于把引号转义和代码页问题一起引进来（我们为此踩过一次坑：
	// Go 的参数转义与 cmd.exe 的引号规则互撕，报"文件名、目录名或卷标语法不正确"）。
	// 这类动作交给我们自己用 Go 做，**命令里就永远不需要引号**。
	ActionAppendFile ActionKind = "append_file"
)

// Request 一次受控执行。
//
// ⚠️ 这里只有 argv（Command + Args），**没有"命令字符串"这个概念** ——
// 命令字符串既是注入面，也是一堆转义坑的来源。
// 对齐 dsh 的 `sandbox.confine(argv, policy)`：输入就是数组。
type Request struct {
	// Kind 动作类型；留空按 ActionExec 处理。
	Kind ActionKind
	// Command 可执行文件（argv[0]）。**不允许是 shell 解释器**，见 Validate。
	Command string
	// Args 参数数组。每个参数原样传给进程，不经任何解析、不需要转义。
	Args []string
	// Workdir 工作目录（留空用进程当前目录）。
	Workdir string
	// Timeout 超时；<=0 用默认值。
	Timeout time.Duration

	// FilePath / FileText 仅 ActionAppendFile 使用。
	FilePath string
	FileText string
}

// Summary 一句话描述这个计划要干什么（写进审批回执，给人看）。
func (r Request) Summary() string {
	if r.Kind == ActionAppendFile {
		return fmt.Sprintf("内置动作：向 %s 追加一条审计记录（不经 shell、不 fork 外部进程）", r.FilePath)
	}
	return strings.TrimRight(r.Command+" "+strings.Join(r.Args, " "), " ")
}

// shellInterpreters 会被当成"命令字符串解释器"的可执行文件名。
//
// 为什么必须拦它们：一旦允许 `sh -c <一段字符串>`，argv 化就白做了 ——
// 注入面、引号转义、代码页问题会全部原路返回。
// 需要 shell 特性时应该在 Go 里显式做（比如上面的 ActionAppendFile），
// 而不是把一段字符串交给解释器去猜。
var shellInterpreters = map[string]bool{
	"cmd": true, "cmd.exe": true,
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "ash": true,
	"powershell": true, "powershell.exe": true, "pwsh": true, "pwsh.exe": true,
}

// AllowShell 是否允许把命令交给 shell 解释器执行。
// 默认**关闭**；只在排查问题时用 SANDBOX_ALLOW_SHELL=true 临时打开。
func AllowShell() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SANDBOX_ALLOW_SHELL")))
	return v == "1" || v == "true" || v == "yes"
}

// Validate 校验一次执行请求是否合法（fail-closed：非法就拒绝执行）。
//
// 这里只做**结构性**校验（缺字段、用了 shell 解释器），不做业务判断 ——
// 业务策略属于 guard 器官。
func Validate(req Request) error {
	if req.Kind == ActionAppendFile {
		if strings.TrimSpace(req.FilePath) == "" {
			return fmt.Errorf("不合法的执行计划：追加文件动作缺少 FilePath")
		}
		return nil
	}
	if strings.TrimSpace(req.Command) == "" {
		return fmt.Errorf("不合法的执行计划：没有可执行命令")
	}
	// 用 baseName 而不是 filepath.Base：后者是**平台相关**的 ——
	// 在 Linux 上 filepath.Base(`C:\Windows\System32\cmd.exe`) 会原样返回整串，
	// 于是黑名单在 Linux 上就漏判了 Windows 风格路径。安全判断不该因为跑在哪个系统而不同。
	name := strings.ToLower(baseName(req.Command))
	if shellInterpreters[name] {
		if AllowShell() {
			return nil
		}
		return fmt.Errorf("不合法的执行计划：不许把命令交给 shell 解释器（%s）。"+
			"argv 化是这个器官的前提 —— 需要 shell 特性时应该在 Go 里显式实现，"+
			"而不是把一段字符串交给解释器（那会把注入面与转义坑一起带回来）。"+
			"确实需要时用 SANDBOX_ALLOW_SHELL=true 临时放行", name)
	}
	return nil
}

// baseName 取路径最后一段，**同时按 '/' 和 '\\' 切分**。
//
// 为什么不用 filepath.Base：它是平台相关的。在 Linux 上
// filepath.Base(`C:\Windows\System32\cmd.exe`) 返回整串（因为 `\` 不是分隔符），
// 黑名单就漏判了。安全判断必须两边一致。
func baseName(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
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

// Run 在宿主机执行一个受控动作（无隔离）。Timeout 为 0 时用默认 30s。
//
// 两条保证：
//  1. **不经过 shell**：走 exec.CommandContext(Command, Args...) 直接 execve，
//     参数原样传给进程 —— 没有引号规则、没有变量展开、没有通配符。
//     （所以参数里有空格、中文都不需要转义。）
//  2. **执行器自己再校验一遍**：不信任调用方。上层（harness）已经校验过，
//     这里再来一次是纵深防御 —— 将来换容器后端时也照抄这一条。
//
// 注意：stdout 和 stderr 分开收——原来只用 cmd.Output() 会把 stderr 丢掉，
// 命令失败时调用方只看得到 err.Error()（"exit status 1"），拿不到真正的原因。
func (LocalExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if err := Validate(req); err != nil {
		return Result{ExitCode: 1, Stderr: err.Error()}, err
	}
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Second
	}

	// 内置动作：纯 Go 完成，不 fork 任何进程。
	if req.Kind == ActionAppendFile {
		return appendFile(req)
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

// appendFile 内置动作：把文本追加到文件（纯 Go，不经过任何 shell 或外部进程）。
//
// 它是 `sh -c 'echo x >> y'` 的替代品 —— 想写文件就直接写，
// 不要为了"写文件"去调一个解释器、再把路径和内容拼进一段字符串里。
func appendFile(req Request) (Result, error) {
	if dir := filepath.Dir(req.FilePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{ExitCode: 1, Stderr: err.Error()}, fmt.Errorf("创建目录失败: %w", err)
		}
	}
	f, err := os.OpenFile(req.FilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{ExitCode: 1, Stderr: err.Error()}, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	n, err := f.WriteString(req.FileText)
	if err != nil {
		return Result{ExitCode: 1, Stderr: err.Error()}, fmt.Errorf("写入失败: %w", err)
	}
	return Result{
		Stdout:   fmt.Sprintf("已追加 %d 字节到 %s", n, req.FilePath),
		ExitCode: 0,
	}, nil
}
