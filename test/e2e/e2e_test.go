// Package e2e 端到端测试：用假模型（test/fakemodel）+ 真 Redis，在秒级内验证
// harness 的关键行为不变量。
//
// 设计原则（借自 dsh 的 testing 文档）：
//   - **只 mock LLM**，Redis 用真的（我们的记忆/审批/折叠逻辑正是要验的东西，mock 掉就白测了）；
//   - **断言落到 Redis 的事件序列**，而不是断言模型回复的文案 ——
//     文案是模型自由发挥的，断言它必然不稳定；
//   - Redis 不可用时 **t.Skip 而不是失败**（对应 dsh 的"缺 key 自动跳过"），
//     这样 CI 或同事机器上没起 docker 也能跑。
//
// 跑法：go test ./test/e2e/ -v
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/test/fakemodel"
)

var (
	testRDB    *redis.Client
	redisReady bool
)

func TestMain(m *testing.M) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	testRDB = redis.NewClient(&redis.Options{Addr: addr})
	if err := testRDB.Ping(context.Background()).Err(); err == nil {
		redisReady = true
		session.Init(testRDB)
		approval.Init(testRDB)
		spill.Init(testRDB)
	}
	os.Exit(m.Run())
}

// requireRedis 依赖 Redis 的用例先过这一关（不可用就跳过，不报失败）。
func requireRedis(t *testing.T) {
	t.Helper()
	if !redisReady {
		t.Skip("Redis 不可用（先 docker start im_redis 或设 REDIS_ADDR），跳过需要真存储的用例")
	}
}

// env 一次 e2e 环境：假模型 server + 干净的 Redis 状态 + 注入好的 harness。
type env struct {
	t     *testing.T
	h     *harness.Harness
	fake  *fakemodel.Server
	srv   *httptest.Server
	uid   uint
	redis *redis.Client
}

func newEnv(t *testing.T, uid uint) *env {
	t.Helper()
	requireRedis(t)

	fake := fakemodel.NewServer(fakemodel.DefaultScenario())
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)

	// 把模型指向假服务器 —— 业务代码一行不用改
	t.Setenv("VOLC_ACCESS_KEY", "fake-key")
	t.Setenv("VOLC_ENDPOINT_ID", "fake-endpoint")
	t.Setenv("VOLC_BASE_URL", srv.URL+"/api/v3")

	e := &env{t: t, fake: fake, srv: srv, uid: uid, redis: testRDB}
	e.reset()
	// 测试不该在共享 Redis 里留垃圾（cmd/probe 扫 agent:* 时会被这些残留刷屏）
	t.Cleanup(e.cleanup)
	return e
}

func (e *env) cleanup() {
	ctx := context.Background()
	e.redis.Del(ctx,
		fmt.Sprintf("agent:V2:history:%d", e.uid),
		fmt.Sprintf("agent:V2:fold:%d", e.uid),
		fmt.Sprintf("agent:pending:%d", e.uid),
	)
}

func (e *env) reset() {
	ctx := context.Background()
	e.redis.Del(ctx, fmt.Sprintf("agent:V2:history:%d", e.uid))
	e.redis.Del(ctx, fmt.Sprintf("agent:V2:fold:%d", e.uid))
	e.redis.Del(ctx, fmt.Sprintf("agent:pending:%d", e.uid))
	e.fake.Reset()
}

// ctx 带 user_id —— 业务代码（agent.getUserID / guard.userFromCtx）用的就是这个字符串 key，
// 测试必须保持完全一致，否则工具会报"上下文中丢失 user_id"。
func (e *env) ctx() context.Context {
	return context.WithValue(context.Background(), "user_id", e.uid)
}

func (e *env) run(content string) string {
	e.t.Helper()
	out, err := e.h.RunAgentTurn(e.ctx(), e.uid, content, nil)
	if err != nil {
		e.t.Fatalf("RunAgentTurn 失败: %v", err)
	}
	return out
}

// log 读出当前记忆日志（展开成 DTO 序列）。
func (e *env) log() []session.MemoryDTO {
	e.t.Helper()
	raw, err := e.redis.LRange(context.Background(), fmt.Sprintf("agent:V2:history:%d", e.uid), 0, -1).Result()
	if err != nil {
		e.t.Fatalf("读日志失败: %v", err)
	}
	out := make([]session.MemoryDTO, 0, len(raw))
	for _, s := range raw {
		var dto session.MemoryDTO
		if err := json.Unmarshal([]byte(s), &dto); err != nil {
			e.t.Fatalf("日志解析失败: %v", err)
		}
		out = append(out, dto)
	}
	return out
}

func (e *env) countTypes() map[string]int {
	c := map[string]int{}
	for _, dto := range e.log() {
		c[dto.Type]++
	}
	return c
}

// newHarness 造一个用假执行器的 harness（不碰真实文件系统）。
func (e *env) newHarness(exec sandbox.Executor) *harness.Harness {
	h := harness.New()
	h.Exec = exec
	return h
}

// recordingExecutor 记录被真实"执行"了什么，避免测试真去跑命令。
type recordingExecutor struct {
	mu   sync.Mutex
	reqs []sandbox.Request
	res  sandbox.Result
	err  error
}

func (r *recordingExecutor) Run(_ context.Context, req sandbox.Request) (sandbox.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return r.res, r.err
}

func (r *recordingExecutor) calls() []sandbox.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sandbox.Request, len(r.reqs))
	copy(out, r.reqs)
	return out
}

// ---------------------------------------------------------------------------
// 纯函数用例：不需要 Redis、不需要模型，永远跑
// ---------------------------------------------------------------------------

func TestProjectMessagesPairOrDrop(t *testing.T) {
	dtos := []session.MemoryDTO{
		{Type: session.EventUserMessage, Role: "user", Content: "第一问"},
		{Type: session.EventToolCall, Role: "assistant", ToolCalls: []session.ToolCallData{{ID: "A", Name: "t"}}},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "A", ToolName: "t", Content: "结果A"},
		{Type: session.EventToolResult, Role: "tool", ToolCallID: "悬空", ToolName: "t", Content: "没有配对的结果"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "答"},
	}

	got := session.ProjectMessagesFrom(dtos, 0)

	// 期望：user + (assistant tool_call + tool 结果) + assistant = 4 条；悬空结果被丢掉
	if len(got) != 4 {
		t.Fatalf("期望 4 条消息（悬空 tool/result 被丢弃），实际 %d 条", len(got))
	}
	for _, m := range got {
		if m.Content == "没有配对的结果" {
			t.Fatal("悬空的 tool/result 不该进投影")
		}
	}
}

// baseSeq 偏移：折叠之后数组下标不再等于 seq，遮蔽区间必须跟着偏移走
func TestProjectMessagesHonoursBaseSeq(t *testing.T) {
	// 这条摘要遮蔽真实 seq 0..9；而我们只读了尾部（baseSeq=10），所以它不该遮蔽任何东西。
	dtos := []session.MemoryDTO{
		{Type: session.EventCompactionSummary, Role: "assistant", Content: "旧摘要", CompactionFrom: 0, CompactionTo: 9},
		{Type: session.EventUserMessage, Role: "user", Content: "新问题"},
		{Type: session.EventAssistantMessage, Role: "assistant", Content: "新回答"},
	}

	got := session.ProjectMessagesFrom(dtos, 10)

	// 若忘了加偏移，会把 seq 0..9 当成"已遮蔽"，从而把摘要本身也算错位；
	// 正确行为：摘要进投影（它是活跃摘要），两条新消息也进投影。
	if len(got) != 3 {
		names := make([]string, 0, len(got))
		for _, m := range got {
			names = append(names, string(m.Role)+":"+m.Content)
		}
		t.Fatalf("期望 3 条（摘要 + 2 条新消息），实际 %d 条: %v", len(got), names)
	}
}

// ---------------------------------------------------------------------------
// 端到端用例：假模型 + 真 Redis
// ---------------------------------------------------------------------------

func TestTurnAndLogInvariants(t *testing.T) {
	e := newEnv(t, 9001)
	e.h = e.newHarness(&recordingExecutor{})

	reply := e.run("你好")

	if reply == "" {
		t.Fatal("回复为空")
	}
	c := e.countTypes()
	if c[session.EventUserMessage] != 1 {
		t.Fatalf("期望 1 条 user/message，实际 %d", c[session.EventUserMessage])
	}
	if c[session.EventAssistantMessage] != 1 {
		t.Fatalf("期望 1 条 assistant/message，实际 %d", c[session.EventAssistantMessage])
	}
	if c[session.EventSystemPrompt] != 1 {
		t.Fatalf("期望 system/prompt 只写 1 次，实际 %d", c[session.EventSystemPrompt])
	}
}

func TestSystemPromptWrittenOnlyOnce(t *testing.T) {
	e := newEnv(t, 9002)
	e.h = e.newHarness(&recordingExecutor{})

	e.run("第一句")
	e.run("第二句")
	e.run("第三句")

	c := e.countTypes()
	if c[session.EventSystemPrompt] != 1 {
		t.Fatalf("3 轮之后 system/prompt 仍应只有 1 条，实际 %d（每轮重写会白涨日志）", c[session.EventSystemPrompt])
	}
	if c[session.EventUserMessage] != 3 {
		t.Fatalf("期望 3 条 user/message，实际 %d", c[session.EventUserMessage])
	}
}

func TestToolCallResultsArePaired(t *testing.T) {
	e := newEnv(t, 9003)
	e.h = e.newHarness(&recordingExecutor{})

	// 场景规则：包含"查一下历史"就调 search_memory_archive（只触发一次，避免死循环）
	e.run("帮我查一下历史")

	c := e.countTypes()
	if c[session.EventToolCall] == 0 {
		t.Fatalf("没落盘 tool/call，事件分布=%v", c)
	}
	if c[session.EventToolCall] != c[session.EventToolResult] {
		t.Fatalf("tool/call 与 tool/result 数量不等（配对不变量被破坏）：call=%d result=%d",
			c[session.EventToolCall], c[session.EventToolResult])
	}

	// 每条 tool/result 都必须带 callId，且能在 tool/call 里找到配对
	declared := map[string]bool{}
	for _, dto := range e.log() {
		if dto.Type == session.EventToolCall {
			for _, tc := range dto.ToolCalls {
				declared[tc.ID] = true
			}
		}
	}
	for _, dto := range e.log() {
		if dto.Type != session.EventToolResult {
			continue
		}
		if dto.ToolCallID == "" {
			t.Fatal("tool/result 缺 callId")
		}
		if !declared[dto.ToolCallID] {
			t.Fatalf("tool/result 的 callId %q 找不到配对的 tool/call", dto.ToolCallID)
		}
	}
}

func TestApprovalSuspendsThenReallyExecutes(t *testing.T) {
	e := newEnv(t, 9004)
	exec := &recordingExecutor{res: sandbox.Result{Stdout: "ok", ExitCode: 0}}
	e.h = e.newHarness(exec)

	// 场景规则：包含"愤怒"就起草 execute_system_defense
	e.run("气死我了，我很愤怒！")

	// 1) 应该挂起，并且**带上批准后要跑的命令**（没有命令的审批是假的）
	pending, err := (approval.RedisStore{}).GetPending(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("读挂起状态失败: %v", err)
	}
	if pending == nil {
		t.Fatal("模型调了 execute_system_defense，但没有挂起待审批动作")
	}
	if pending.Command == "" {
		t.Fatal("挂起的提案没有带可执行命令 —— 批准之后将无事可做（审批是假的）")
	}
	if pending.Reason == "" {
		t.Fatal("挂起的提案没有原因，审计时无法追溯为什么要批")
	}

	// 2) 批准之前，绝不能真的执行
	if len(exec.calls()) != 0 {
		t.Fatalf("还没批准就已经执行了 %d 次，审批形同虚设", len(exec.calls()))
	}

	// 3) 批准 → 必须经沙箱执行器真实执行
	handled, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")
	if !handled {
		t.Fatal("auth:approve 没被处理")
	}
	calls := exec.calls()
	if len(calls) != 1 {
		t.Fatalf("期望恰好执行 1 次，实际 %d 次。回执=%s", len(calls), reply)
	}
	if calls[0].Command != pending.Command {
		t.Fatalf("执行的命令与提案不一致：执行=%q 提案=%q", calls[0].Command, pending.Command)
	}

	// 4) 必须落一条审计事件（log-only，不喂模型，但要可追溯）
	c := e.countTypes()
	if c[session.EventAudit] == 0 {
		t.Fatal("审批执行后没有落 audit/action 审计事件，事后无法追溯")
	}

	// 5) 同一提案不能被执行两次
	if _, again := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve"); again != "" {
		if len(exec.calls()) != 1 {
			t.Fatalf("重复 approve 又被执行了一次，共 %d 次", len(exec.calls()))
		}
	}
}

func TestRejectDoesNotExecute(t *testing.T) {
	e := newEnv(t, 9005)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)

	e.run("气死我了，太愤怒了")

	handled, _ := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:reject")
	if !handled {
		t.Fatal("auth:reject 没被处理")
	}
	if len(exec.calls()) != 0 {
		t.Fatalf("拒绝之后仍然执行了 %d 次", len(exec.calls()))
	}
	if c := e.countTypes(); c[session.EventAudit] == 0 {
		t.Fatal("拒绝也要留审计记录")
	}
}
