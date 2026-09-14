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
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/tokenmeter"
	"go_im_gateway/test/fakemodel"

	"github.com/cloudwego/eino/schema"
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
// 隔离等级可配：零值 = IsolationNone（和真实的 LocalExecutor 一样诚实上报）。
type recordingExecutor struct {
	mu    sync.Mutex
	reqs  []sandbox.Request
	res   sandbox.Result
	err   error
	level sandbox.Isolation
	desc  string
}

func (r *recordingExecutor) Run(_ context.Context, req sandbox.Request) (sandbox.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return r.res, r.err
}

func (r *recordingExecutor) Isolation() sandbox.Isolation { return r.level }

func (r *recordingExecutor) Describe() string {
	if r.desc != "" {
		return r.desc
	}
	return "测试执行器（隔离等级 " + r.level.String() + "）"
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

// ---------------------------------------------------------------------------
// 上下文预算：按 token 判断，不按消息条数
// ---------------------------------------------------------------------------

// smallBudget 把窗口缩到 2000 token（触发线 1600 / 保留线 320），
// 这样几条消息内就能看到"该不该压"的行为，不用真喂几万 token。
func smallBudget() tokenmeter.Budget {
	return tokenmeter.Budget{ContextWindow: 2000, ThresholdRatio: 0.8, RetainRatio: 0.16}.Sanitized()
}

// 20 条**短**消息：既不该触发压缩，也不该被条数截断。
//
// 这条用例正是 P0-2 要修的毛病：以前窗口是 `MaxHistory = 20` 条，
// 20 轮之后早期消息被无条件丢掉 —— 哪怕它们一共才几百 token、便宜得很。
func TestManyShortMessagesNeitherCompactNorTruncate(t *testing.T) {
	e := newEnv(t, 9010)
	e.h = e.newHarness(&recordingExecutor{})
	session.SetBudget(smallBudget())
	defer session.SetBudget(tokenmeter.DefaultBudget())

	for i := 0; i < 20; i++ {
		e.run("闲聊一句就行")
	}

	if c := e.countTypes(); c[session.EventCompactionSummary] != 0 {
		t.Fatalf("20 条短消息远没到触发线(1600 token)，不该压缩，实际压了 %d 次", c[session.EventCompactionSummary])
	}

	hist, err := e.h.Sessions.GetHistory(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	// 20 轮 = 20 条 user + 20 条 assistant，一共才几百 token，应该全留住
	if len(hist) != 40 {
		t.Fatalf("40 条短消息应该全部保留（按 token 算很便宜），实际只回了 %d 条 —— 还在按条数截断？", len(hist))
	}
}

// 几条**长**消息：应该触发压缩，而不是硬塞给模型。
func TestLongMessagesTriggerCompaction(t *testing.T) {
	e := newEnv(t, 9011)
	e.h = e.newHarness(&recordingExecutor{})
	session.SetBudget(smallBudget())
	defer session.SetBudget(tokenmeter.DefaultBudget())

	// 每条约 1100 个汉字（≈ 800 token），三轮就顶过 1600 的触发线
	long := strings.Repeat("这是一段很长的用户输入，用来把上下文迅速顶到触发线以上。", 40)

	const turns = 5
	for i := 0; i < turns; i++ {
		e.run(long)
	}

	c := e.countTypes()
	if c[session.EventCompactionSummary] == 0 {
		t.Fatalf("几条长消息后用量早超过窗口的 80%%，却没触发压缩 —— 说明还在按条数判断。事件分布=%v", c)
	}
	if c[session.EventCheckpoint] == 0 {
		t.Fatal("压缩后应该推进折叠水位（session/checkpoint）")
	}

	// 压缩要真的把窗口收回来，而不是"记了一笔摘要，历史照样全喂"
	hist, err := e.h.Sessions.GetHistory(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if len(hist) >= turns*2 {
		t.Fatalf("一共产生了 %d 条消息，压缩后投影里还有 %d 条 —— 等于没压", turns*2, len(hist))
	}
	if got := tokenmeter.Used(hist); got > session.GetBudget().TriggerTokens() {
		t.Fatalf("压缩后仍在触发线以上：%d > %d，下一轮会立刻又压一次", got, session.GetBudget().TriggerTokens())
	}
}

// 单条消息本身就撑爆窗口时：没有"早期对话"可压 —— 压缩应该**老实返回、不硬造空摘要**，
// 由 GetHistory 的硬裁兜底（压缩是 best-effort，所以兜底不能也依赖它）。
//
// 注意窗口要设得比"单条消息还小"，硬裁才会真的被触发。
// 第一版这个用例窗口给的是 2000，而两条消息加起来才 1555 —— 裁剪逻辑压根没跑，
// 断言却照样通过，属于"假绿"。假绿的测试比没有测试更糟：它给的是假信心。
func TestHugeSingleMessageFallsBackToHardTrim(t *testing.T) {
	e := newEnv(t, 9013)
	e.h = e.newHarness(&recordingExecutor{})

	// 窗口 500 / 触发线 400 / 保留线 80。
	// 单条 1600 汉字 ≈ 1120 token，无论校准系数怎么偏都必然超过窗口 —— 裁剪一定被触发。
	session.SetBudget(tokenmeter.Budget{ContextWindow: 500, ThresholdRatio: 0.8, RetainRatio: 0.16}.Sanitized())
	defer session.SetBudget(tokenmeter.DefaultBudget())
	window := session.GetBudget().ContextWindow

	huge := strings.Repeat("超长内容", 400)
	e.run(huge)
	e.run(huge)

	hist, err := e.h.Sessions.GetHistory(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}

	// 1) 硬保护生效：喂给模型的必须落在窗口以内
	if got := tokenmeter.Used(hist); got > window {
		t.Fatalf("投影超出窗口硬顶：%d > %d", got, window)
	}
	// 2) 而且确实裁掉了东西（不是靠"本来就装得下"蒙混过去的）
	msgs := e.countTypes()
	if len(hist) >= msgs[session.EventUserMessage]+msgs[session.EventAssistantMessage] {
		t.Fatalf("窗口只有 %d，巨型消息必然超限，硬裁却没裁掉任何东西：投影 %d 条",
			window, len(hist))
	}
	// 3) 关键行为：没有硬造一条空摘要
	//    —— 这时候压根没有"早期对话"可压，造摘要等于凭空捏造一段历史
	if msgs[session.EventCompactionSummary] != 0 {
		t.Fatalf("没有可压的早期对话，却写了 %d 条摘要（凭空捏造历史）",
			msgs[session.EventCompactionSummary])
	}
	// 4) 硬裁只影响"投影"，日志一条不动（事件溯源：日志是唯一真相）
	if msgs[session.EventUserMessage] != 2 {
		t.Fatalf("日志里的用户消息应该是 2 条（裁剪不该动日志），实际 %d 条", msgs[session.EventUserMessage])
	}
}

// ---------------------------------------------------------------------------
// 沙箱诚实上报 + fail-closed
// ---------------------------------------------------------------------------

// 审批回执必须写明"隔离等级"和"怎么跑的"。
// 人批的是"沙箱内的动作"还是"裸跑"，是完全不同的两件事；不写清楚，
// 审批链上流通的就是假信息 —— 那比没有沙箱更危险。
func TestApprovalReceiptReportsIsolation(t *testing.T) {
	e := newEnv(t, 9014)
	exec := &recordingExecutor{
		res:   sandbox.Result{Stdout: "ok", ExitCode: 0},
		level: sandbox.IsolationFull,
		desc:  "docker run --network=none --read-only",
	}
	e.h = e.newHarness(exec)

	e.run("气死我了，我很愤怒！")
	_, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")

	if !strings.Contains(reply, "full") {
		t.Fatalf("回执里没有隔离等级，人无法判断批的是什么：\n%s", reply)
	}
	if !strings.Contains(reply, "docker run") {
		t.Fatalf("回执里没有执行方式（Describe），人无法核对：\n%s", reply)
	}
}

// 策略要求隔离、而当前沙箱给不出 → **拒绝执行**，不是"跑了再说"（fail-closed）。
func TestApprovalRefusedWithoutIsolation(t *testing.T) {
	e := newEnv(t, 9015)
	exec := &recordingExecutor{res: sandbox.Result{Stdout: "ok"}} // 零值 = IsolationNone
	e.h = e.newHarness(exec)

	t.Setenv("REQUIRE_SANDBOX_ISOLATION", "true")

	e.run("气死我了，我很愤怒！")
	handled, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")

	if !handled {
		t.Fatal("auth:approve 没被处理")
	}
	if len(exec.calls()) != 0 {
		t.Fatalf("要求隔离却没有隔离，仍然执行了 %d 次 —— fail-closed 没生效", len(exec.calls()))
	}
	if !strings.Contains(reply, "拒绝") {
		t.Fatalf("回执应明确说\"已拒绝执行\"，实际：\n%s", reply)
	}
	// 被拒绝也是一次"决策"，同样要可追溯（为什么没执行）
	if c := e.countTypes(); c[session.EventAudit] == 0 {
		t.Fatal("因无隔离被拒绝，也要留审计记录")
	}
}

// 没要求隔离时可以执行，但回执**必须带上"本次没有任何隔离"的警告** ——
// 否则人以为自己批的是沙箱内动作，实际是裸跑。
func TestApprovalWarnsWhenRunningWithoutIsolation(t *testing.T) {
	e := newEnv(t, 9016)
	exec := &recordingExecutor{res: sandbox.Result{Stdout: "ok", ExitCode: 0}}
	e.h = e.newHarness(exec)

	e.run("气死我了，我很愤怒！")
	_, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")

	if len(exec.calls()) != 1 {
		t.Fatalf("默认不要求隔离时应该照常执行，实际执行了 %d 次", len(exec.calls()))
	}
	if !strings.Contains(reply, "没有任何隔离") {
		t.Fatalf("无隔离执行却没给出明确警告，等于让人以为有沙箱：\n%s", reply)
	}
}

// ---------------------------------------------------------------------------
// 崩溃修复与流中断（P1-1 / P1-2）
// ---------------------------------------------------------------------------

// 进程被杀会留下"开放轮次"：user/message 后面什么都没有。
// Repair 要认出它并补一条**合成**收尾 —— 只追加，不改历史。
func TestRepairClosesOpenTurn(t *testing.T) {
	e := newEnv(t, 9018)
	e.h = e.newHarness(&recordingExecutor{})

	// 直接制造这个状态：写了一句话，但没有任何回复（模拟进程被杀）
	if err := e.h.Sessions.SaveMessage(e.ctx(), e.uid, schema.UserMessage("我被杀之前说的那句话")); err != nil {
		t.Fatalf("造数据失败: %v", err)
	}

	n, err := e.h.Sessions.Repair(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("Repair 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应该识别出 1 个未闭合轮次，实际 %d", n)
	}

	// 收尾事件必须写明是 interrupted（而不是冒充正常结束）
	found := false
	for _, dto := range e.log() {
		if dto.Type == session.EventTurnEnd && strings.Contains(dto.Content, "interrupted") {
			found = true
		}
	}
	if !found {
		t.Fatal("没补出 turn/end{interrupted}，下一轮仍然分不清\"模型没回\"和\"进程死了\"")
	}

	// 幂等：修完就不该再认为自己要修（否则每轮都补一条，日志白涨）
	if n2, _ := e.h.Sessions.Repair(e.ctx(), e.uid); n2 != 0 {
		t.Fatalf("Repair 不幂等：修完还说有 %d 轮未闭合", n2)
	}

	// 只追加：原来那条用户消息必须还在（日志是唯一真相，修复不改写历史）
	c := e.countTypes()
	if c[session.EventUserMessage] != 1 {
		t.Fatalf("修复动到了历史：用户消息应该还是 1 条，实际 %d 条", c[session.EventUserMessage])
	}
}

// 流被切断时：已投递的文本照存，但必须**标记为中断**，且不能写"正常收尾"。
func TestInterruptedStreamIsMarked(t *testing.T) {
	e := newEnv(t, 9019)
	e.h = e.newHarness(&recordingExecutor{})

	// 场景规则：包含"掐断"就让假模型发一片内容后把流弄坏
	e.run("掐断")

	marked := false
	for _, dto := range e.log() {
		if dto.Type == session.EventAssistantMessage && dto.Interrupted {
			marked = true
		}
		if dto.Type == session.EventTurnEnd && dto.Content == "completed" {
			t.Fatal("被中断的轮次写了 completed 收尾 —— 等于假装这轮正常结束了，Repair 就再也认不出它")
		}
	}
	if !marked {
		t.Fatal("流被中途切断，却没把这条回复标记为 interrupted —— 半截回复会冒充完整回答写进历史")
	}

	// 半截回复要喂给模型时带上说明，否则模型以为自己当时把话说完了
	hist, err := e.h.Sessions.GetHistory(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	sawNote := false
	for _, m := range hist {
		if strings.Contains(m.Content, "上一轮回复被中断") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatal("投影里没有\"回复被中断\"的说明，模型会接着半截话往下答")
	}

	// 下一轮开始时，Repair 应该认出上一轮没闭合并补收尾
	e.run("再说一句正常的话")
	repaired := false
	for _, dto := range e.log() {
		if dto.Type == session.EventTurnEnd && strings.Contains(dto.Content, "interrupted") {
			repaired = true
		}
	}
	if !repaired {
		t.Fatal("上一轮被中断（未闭合），下一轮没有补合成收尾 —— Repair 与中断标记没接上")
	}
}

// 真实 usage 要落盘，并用来校准估算系数
func TestUsageIsRecordedAndCalibrates(t *testing.T) {
	e := newEnv(t, 9012)
	e.h = e.newHarness(&recordingExecutor{})

	tokenmeter.ResetCalibration()
	defer tokenmeter.ResetCalibration()

	e.run("你好")

	if c := e.countTypes(); c[session.EventUsage] == 0 {
		t.Fatal("模型返回了 usage，却没有落盘 usage/report 事件 —— 预算判断就永远只能拍脑袋")
	}
	if _, n := tokenmeter.Calibration(); n == 0 {
		t.Fatal("usage 没有喂给校准器，估算系数永远是 1.0")
	}
}

// ---------------------------------------------------------------------------
// LLM 重试（P1-3）
// ---------------------------------------------------------------------------

// 上游瞬时故障（429 限流）不该把整轮打挂：要退避重试，且每次重试都留痕。
func TestLLMRetryRecoversFromRateLimit(t *testing.T) {
	e := newEnv(t, 9020)
	e.h = e.newHarness(&recordingExecutor{})
	// 退避压到 1ms：真等 500ms+1s 会把"秒级回归"变成"慢速回归"
	t.Setenv("RETRY_BASE_DELAY_MS", "1")
	t.Setenv("RETRY_MAX_DELAY_MS", "1")

	// 前 2 次请求抛 429（走真 SDK，不是直接喂 error），第 3 次才正常回复
	e.fake.SetScenario(fakemodel.Scenario{
		Default: fakemodel.Reply{Text: "重试之后终于成功了。"},
		Faults:  []fakemodel.Fault{{Status: 429, Times: 2}},
	})

	reply := e.run("你好")
	if !strings.Contains(reply, "重试之后终于成功了") {
		t.Fatalf("重试之后应该拿到正常回复，实际：%q", reply)
	}

	c := e.countTypes()
	if c[session.EventLLMRetry] != 2 {
		t.Fatalf("应该留下 2 条 llm/retry 事件，实际 %d 条（事件分布=%v）", c[session.EventLLMRetry], c)
	}

	// 事件里必须写清"为什么重试"：只说"重试了 2 次"对排查毫无用处
	sawReason := false
	for _, dto := range e.log() {
		if dto.Type == session.EventLLMRetry && strings.Contains(dto.Content, "rate_limit") {
			sawReason = true
		}
	}
	if !sawReason {
		t.Fatal("llm/retry 事件没写清原因是 rate_limit —— 事后排查等于没有线索")
	}

	// 重试是"过程"，这一轮仍然应该正常收尾（而不是被当成中断）
	if c[session.EventTurnEnd] == 0 {
		t.Fatal("重试成功之后这一轮应该正常收尾")
	}
}

// 参数错（400）重试一百次还是同样的错：必须**立刻失败**，不许白等（还烧钱）。
func TestNonRetryableErrorFailsFast(t *testing.T) {
	e := newEnv(t, 9021)
	e.h = e.newHarness(&recordingExecutor{})
	t.Setenv("RETRY_BASE_DELAY_MS", "1")

	e.fake.SetScenario(fakemodel.Scenario{
		Default: fakemodel.Reply{Text: "不该走到这里"},
		Faults:  []fakemodel.Fault{{Status: 400, Times: 3}},
	})

	if _, err := e.h.RunAgentTurn(e.ctx(), e.uid, "你好", nil); err == nil {
		t.Fatal("400 参数错应该直接失败，而不是被重试到成功")
	}
	if c := e.countTypes(); c[session.EventLLMRetry] != 0 {
		t.Fatalf("不该重试的错误却留下了 %d 条 llm/retry", c[session.EventLLMRetry])
	}
}

// ---------------------------------------------------------------------------
// 写入侧不变量（P1-4）与日志格式版本（P1-5）
// ---------------------------------------------------------------------------

// 缺 ToolCallID 的 tool/result 必须被**拒绝写入**，
// 而不是写进日志再靠投影默默丢掉 —— 那样问题永远查不出来。
func TestWriteInvariantsRejectDirtyEvents(t *testing.T) {
	e := newEnv(t, 9022)
	e.h = e.newHarness(&recordingExecutor{})
	store := session.RedisStore{}

	err := store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventToolResult, Role: "tool", Content: "一条没有 ToolCallID 的工具结果",
	})
	if err == nil {
		t.Fatal("缺 ToolCallID 的 tool/result 应该被拒绝")
	}
	if !strings.Contains(err.Error(), "ToolCallID") {
		t.Fatalf("错误信息要指出违反的是哪条不变量，实际：%v", err)
	}
	for _, dto := range e.log() {
		if dto.Type == session.EventToolResult {
			t.Fatal("被拒绝的事件仍然进了日志 —— 那这道校验等于没做")
		}
	}

	// 合法事件要照常放行（别把校验做成"一律拒绝"）
	if err := store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventToolResult, Role: "tool", Content: "正常结果", ToolCallID: "call-1",
	}); err != nil {
		t.Fatalf("合法事件不该被拒：%v", err)
	}
}

// 日志格式比本程序新时：宁可明确拒绝加载，也不要用零值猜；
// 而没有版本号的老日志必须照常读 —— 不能因为升级一次就废掉用户全部历史。
func TestUnknownLogVersionRefusesToLoad(t *testing.T) {
	e := newEnv(t, 9023)
	e.h = e.newHarness(&recordingExecutor{})

	future := `{"type":"user/message","role":"user","content":"来自未来的日志","v":99,` +
		`"time":"2026-09-14T00:00:00Z"}`
	if err := e.redis.RPush(e.ctx(), fmt.Sprintf("agent:V2:history:%d", e.uid), future).Err(); err != nil {
		t.Fatalf("塞测试数据失败: %v", err)
	}

	_, err := e.h.Sessions.GetHistory(e.ctx(), e.uid)
	if err == nil {
		t.Fatal("读到 v99 的日志应该明确报错，而不是静默按零值继续跑")
	}
	if !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("错误信息要说清是「格式不认识」，实际：%v", err)
	}

	// 老日志（没有 v 字段）必须照常读
	e2 := newEnv(t, 9024)
	e2.h = e2.newHarness(&recordingExecutor{})
	legacy := `{"type":"user/message","role":"user","content":"加版本号之前的老日志",` +
		`"time":"2026-09-14T00:00:00Z"}`
	e2.redis.RPush(e2.ctx(), fmt.Sprintf("agent:V2:history:%d", e2.uid), legacy)
	hist, err := e2.h.Sessions.GetHistory(e2.ctx(), e2.uid)
	if err != nil {
		t.Fatalf("没有 v 字段的老日志必须照常读，实际报错：%v", err)
	}
	if len(hist) == 0 {
		t.Fatal("老日志没有被投影出来")
	}
}
