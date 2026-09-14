package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultImage 容器后端默认镜像。
// 选 alpine 是因为它小（几十 MB）、自带 busybox（有 echo/ls/cat 这类基础工具），
// 够跑演示与测试。
const DefaultImage = "alpine:3.20"

// DockerExecutor 容器后端：把命令放进**一次性容器**里跑 —— 这是真隔离，
// 也是 sandbox 器官从"占位"变成"真的"的那一步。
//
// 思路对齐 codex 的 MacosSeatbelt / LinuxSeccomp 与 dsh 的 bwrap / Landlock：
// 都是"把不受信的命令关进笼子"，而不是"问一句就放它跑"。
// 这里选 Docker 是因为它跨平台、而且你机器上已经有了（Docker Desktop），
// 不用为每个 OS 各写一套。
type DockerExecutor struct {
	// Image 跑命令用的镜像；留空用 DefaultImage。
	Image string
	// Runner 真正执行 docker 命令的方式（默认 LocalExecutor）。
	//
	// 把"拼 docker 参数"和"真的去调 docker"分开：前者是纯函数、可以单测，
	// 后者才需要 docker 环境。测试里注入一个假 Runner 就能验绝大部分逻辑。
	Runner Executor
}

// Isolation 容器隔离 = 完整隔离：文件系统只读、没有网络、没有 capability、
// 进程数与内存都受限。
func (DockerExecutor) Isolation() Isolation { return IsolationFull }

// Describe 写明具体参数 —— 这句话会进审批回执，人有知情权：
// 得让他知道"笼子"到底长什么样，而不是只看到一句"已沙箱化"。
func (d DockerExecutor) Describe() string {
	return fmt.Sprintf("容器隔离（docker run --network=none --read-only --cap-drop=ALL "+
		"--pids-limit=64 --memory=256m，镜像 %s）", d.image())
}

func (d DockerExecutor) image() string {
	if strings.TrimSpace(d.Image) != "" {
		return d.Image
	}
	return DefaultImage
}

// DockerArgs 把一次请求翻译成 `docker run ...` 的 argv。
//
// **纯函数，所以能单测** —— 隔离参数漏一个就是安全漏洞，这种拼装必须被测住。
//
// 顺序很重要：镜像名之后的第一个参数才是"容器里要跑的命令"，
// 镜像与命令之间绝不能混进 flag，否则那个 flag 会被当成容器里的命令。
func (d DockerExecutor) DockerArgs(req Request) []string {
	args := []string{
		"run", "--rm",
		"--network=none",                   // 没有网络：容器里 curl 外网必须失败
		"--read-only",                      // 根文件系统只读：改不了系统文件
		"--cap-drop=ALL",                   // 丢掉全部 capability（没有 mount / ptrace / …）
		"--security-opt=no-new-privileges", // 不许通过 setuid 之类提权
		"--pids-limit=64",                  // 防 fork 炸弹
		"--memory=256m",                    // 内存上限
		"--cpus=1",                         // CPU 上限
	}
	if wd := strings.TrimSpace(req.Workdir); wd != "" {
		if abs, err := filepath.Abs(wd); err == nil {
			// 只把工作目录挂进去、而且只有它一个 ——
			// 容器看不到宿主机的其它任何路径。
			args = append(args, "-v", abs+":/work:rw", "-w", "/work")
		}
	}
	args = append(args, d.image(), req.Command)
	return append(args, req.Args...)
}

// Run 在一次性容器里执行。
//
// 内置文件动作（ActionAppendFile）**不进容器**：它本来就是"宿主机的审计落盘"，
// 纯 Go 写即可 —— 为它起个容器既没意义，也会因为 --read-only 写不进去。
func (d DockerExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if req.Kind == ActionAppendFile {
		return appendFile(req)
	}
	if err := Validate(req); err != nil {
		return Result{ExitCode: 1, Stderr: err.Error()}, err
	}
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Second
	}
	runner := d.Runner
	if runner == nil {
		runner = LocalExecutor{}
	}
	return runner.Run(ctx, Request{
		Command: "docker",
		Args:    d.DockerArgs(req),
		Timeout: req.Timeout,
	})
}

// Available 探测容器后端能不能用（docker 在不在、daemon 起没起）。
func (d DockerExecutor) Available(ctx context.Context) error {
	runner := d.Runner
	if runner == nil {
		runner = LocalExecutor{}
	}
	res, err := runner.Run(ctx, Request{
		Command: "docker",
		Args:    []string{"version", "--format", "{{.Server.Version}}"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("docker 不可用：%v", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("docker 不可用：%s", strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

// Unavailable 一个"永远拒绝执行"的执行器。
//
// 为什么需要它：配置里明确写了 SANDBOX_BACKEND=docker、但 docker 起不来时，
// 唯一安全的做法是**拒绝一切执行**，而不是悄悄退回本机直跑 ——
// "配置写了要隔离、实际却裸跑"比"根本没配隔离"危险得多：
// 后者至少人知道自己没有沙箱。
type Unavailable struct {
	// Reason 为什么不可用（会写进审批回执给人看）
	Reason string
}

// Isolation 如实上报：它连执行都做不了，当然谈不上隔离。
func (Unavailable) Isolation() Isolation { return IsolationNone }

// Describe 说明真实情况（这句话会出现在审批回执里）。
func (u Unavailable) Describe() string { return "沙箱不可用：" + u.Reason }

// Run 永远拒绝。返回错误而不是 panic —— 上层会把它包成回执给人看。
func (u Unavailable) Run(context.Context, Request) (Result, error) {
	err := fmt.Errorf("沙箱不可用，拒绝执行：%s", u.Reason)
	return Result{ExitCode: 1, Stderr: err.Error()}, err
}

// FromEnv 按 SANDBOX_BACKEND 选执行器（默认 local）。
//
//	SANDBOX_BACKEND=local   本机直跑（无隔离，会如实上报 none）—— 默认
//	SANDBOX_BACKEND=docker  容器隔离（full）；配置了但 docker 不可用时拒绝一切执行
//	SANDBOX_IMAGE           容器镜像（默认 alpine:3.20）
func FromEnv() Executor { return fromEnv(os.Getenv, probeDocker) }

// probeDocker 探活：docker 命令能不能跑、daemon 起没起。
func probeDocker() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return DockerExecutor{}.Available(ctx)
}

// fromEnv 是 FromEnv 的可测版本：读环境变量与探活都抽成参数。
//
// 于是"配置了 docker 但 docker 不可用时，会不会静默退回裸跑"这件事
// 能被单测钉死 —— 它是这个文件里最要紧的一条行为。
func fromEnv(getenv func(string) string, probe func() error) Executor {
	switch strings.ToLower(strings.TrimSpace(getenv("SANDBOX_BACKEND"))) {
	case "docker":
		d := DockerExecutor{Image: strings.TrimSpace(getenv("SANDBOX_IMAGE"))}
		if err := probe(); err != nil {
			// ⚠️ 关键：**不退回 LocalExecutor**。宁可什么都不执行。
			return Unavailable{Reason: err.Error()}
		}
		return d
	default:
		return LocalExecutor{}
	}
}
