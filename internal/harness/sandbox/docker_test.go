package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

// 隔离参数漏一个就是安全漏洞，所以逐个钉住。
// 这些参数不是"锦上添花"：没有 --network=none，容器里就能连出去；
// 没有 --read-only / --cap-drop=ALL，容器里的进程就能动系统。
func TestDockerArgsHasIsolationFlags(t *testing.T) {
	got := strings.Join(DockerExecutor{}.DockerArgs(Request{Command: "echo", Args: []string{"hi"}}), " ")
	for _, want := range []string{
		"--network=none",                   // 没有网络
		"--read-only",                      // 根文件系统只读
		"--cap-drop=ALL",                   // 没有 capability
		"--security-opt=no-new-privileges", // 不许提权
		"--pids-limit=64",                  // 防 fork 炸弹
		"--memory=256m",
		"--cpus=1",
		"--rm", // 用完就扔
	} {
		if !strings.Contains(got, want) {
			t.Errorf("docker 参数里缺 %q：\n%s", want, got)
		}
	}
}

// 顺序：镜像名之后的**第一个参数**必须是容器里要跑的命令。
// 中间混进 flag 的话，那个 flag 会被当成命令去执行（静默跑错东西）。
func TestDockerArgsCommandFollowsImage(t *testing.T) {
	args := DockerExecutor{Image: "alpine:3.20"}.DockerArgs(
		Request{Command: "echo", Args: []string{"a", "b"}})

	idx := -1
	for i, a := range args {
		if a == "alpine:3.20" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("参数里没有镜像名：%v", args)
	}
	want := []string{"echo", "a", "b"}
	got := args[idx+1:]
	if len(got) != len(want) {
		t.Fatalf("镜像之后应是 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("镜像之后的参数 = %v，期望 %v", got, want)
		}
	}
}

// 只挂工作目录，而且只有它一个 —— 容器不该看得见宿主机的其它路径。
func TestDockerArgsMountsOnlyWorkdir(t *testing.T) {
	args := DockerExecutor{}.DockerArgs(Request{Command: "ls", Workdir: "/tmp/abc"})

	mounts := 0
	for i, a := range args {
		if a != "-v" {
			continue
		}
		mounts++
		if !strings.HasSuffix(args[i+1], ":/work:rw") {
			t.Fatalf("挂载参数意外：%s", args[i+1])
		}
	}
	if mounts != 1 {
		t.Fatalf("期望恰好 1 个挂载点，实际 %d 个：%v", mounts, args)
	}

	// 注意：不能写成 `for _, a := range DockerExecutor{}.DockerArgs(...)` ——
	// for 的控制子句里复合字面量后面的 `{` 会被当成语句块的开始（编译不过）。
	noWorkdir := DockerExecutor{}.DockerArgs(Request{Command: "ls"})
	for _, a := range noWorkdir {
		if a == "-v" {
			t.Fatal("没有 Workdir 却挂了卷")
		}
	}
}

// Run 必须把请求翻译成"去调 docker"，而不是自己直接跑那条命令 ——
// 直接跑就等于沙箱没生效。
func TestDockerRunGoesThroughDocker(t *testing.T) {
	fake := &recordingRunner{}
	d := DockerExecutor{Image: "alpine:3.20", Runner: fake}

	if _, err := d.Run(context.Background(), Request{Command: "echo", Args: []string{"hi"}}); err != nil {
		t.Fatalf("不该报错：%v", err)
	}

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("期望恰好调用 runner 一次，实际 %d 次", len(calls))
	}
	if calls[0].Command != "docker" {
		t.Fatalf("应该去调 docker，实际调了 %q（等于绕过沙箱直接跑命令）", calls[0].Command)
	}
	if !strings.Contains(strings.Join(calls[0].Args, " "), "alpine:3.20") {
		t.Fatalf("docker 参数里没有镜像名：%v", calls[0].Args)
	}
}

// 内置文件动作不进容器：它就是宿主机上的落盘，
// 起个容器既没意义，也会因为 --read-only 写不进去。
func TestDockerRunAppendFileStaysLocal(t *testing.T) {
	fake := &recordingRunner{}
	d := DockerExecutor{Runner: fake}

	path := t.TempDir() + "/a.log"
	if _, err := d.Run(context.Background(), Request{
		Kind: ActionAppendFile, FilePath: path, FileText: "hello\n",
	}); err != nil {
		t.Fatalf("内置动作不该报错：%v", err)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("内置文件动作不该去调 docker")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("文件没写对：%v / %q", err, data)
	}
}

// 容器后端是唯一真隔离的那一档，必须如实上报 full。
func TestDockerExecutorReportsFullIsolation(t *testing.T) {
	d := DockerExecutor{}
	if d.Isolation() != IsolationFull {
		t.Fatalf("容器后端应上报 full，实际 %s", d.Isolation())
	}
	// Describe 要写明具体参数 —— 人有权知道"笼子"长什么样，
	// 而不是只看到一句"已沙箱化"。
	if !strings.Contains(d.Describe(), "network=none") {
		t.Fatalf("Describe 应写明具体参数，实际：%s", d.Describe())
	}
}

// ⚠️ 这个文件里最要紧的一条：
// 配置要求 docker、但 docker 不可用时，**绝不能静默退回本机直跑** ——
// "配了要隔离却实际裸跑"比"根本没配隔离"危险得多。
func TestFromEnvDockerUnavailableRefusesInsteadOfFallingBack(t *testing.T) {
	getenv := func(k string) string {
		if k == "SANDBOX_BACKEND" {
			return "docker"
		}
		return ""
	}
	exec := fromEnv(getenv, func() error { return errors.New("docker 不可用：连不上 daemon") })

	if _, ok := exec.(Unavailable); !ok {
		t.Fatalf("docker 不可用时应返回 Unavailable，实际 %T", exec)
	}
	if _, ok := exec.(LocalExecutor); ok {
		t.Fatal("绝不能退回 LocalExecutor（那就成了悄悄裸跑）")
	}
	// 而且它必须真的拒绝一切执行
	if _, err := exec.Run(context.Background(), Request{Command: "echo"}); err == nil {
		t.Fatal("Unavailable 必须拒绝执行")
	}
	if exec.Isolation() != IsolationNone {
		t.Fatalf("Unavailable 应如实上报 none，实际 %s", exec.Isolation())
	}
}

func TestFromEnvDefaultIsLocal(t *testing.T) {
	exec := fromEnv(func(string) string { return "" }, func() error {
		t.Fatal("没配 docker 就不该去探活")
		return nil
	})
	if _, ok := exec.(LocalExecutor); !ok {
		t.Fatalf("默认应是 LocalExecutor，实际 %T", exec)
	}
}

func TestFromEnvDockerAvailableReturnsDocker(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case "SANDBOX_BACKEND":
			return "docker"
		case "SANDBOX_IMAGE":
			return "alpine:3.20"
		}
		return ""
	}
	exec := fromEnv(getenv, func() error { return nil })

	d, ok := exec.(DockerExecutor)
	if !ok {
		t.Fatalf("docker 可用时应返回 DockerExecutor，实际 %T", exec)
	}
	if d.Image != "alpine:3.20" {
		t.Fatalf("镜像应从 SANDBOX_IMAGE 读到，实际 %q", d.Image)
	}
	if d.Isolation() != IsolationFull {
		t.Fatalf("应上报 full，实际 %s", d.Isolation())
	}
}

// recordingRunner 记录被要求执行了什么，直接返回成功（不真跑任何进程）。
type recordingRunner struct {
	mu   sync.Mutex
	reqs []Request
	res  Result
	err  error
}

func (r *recordingRunner) Run(_ context.Context, req Request) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return r.res, r.err
}

func (r *recordingRunner) Isolation() Isolation { return IsolationNone }
func (r *recordingRunner) Describe() string     { return "测试用假执行器" }

func (r *recordingRunner) snapshot() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Request, len(r.reqs))
	copy(out, r.reqs)
	return out
}
