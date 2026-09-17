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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/metrics"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/subagent"
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
	// 接上子 agent 构造器（正常由 cmd/api 注入）。不接的话 `delegate_task` 只会返回
	// "runner 未设置"，子 agent 根本不跑 —— 那样"父子 step 不互相污染"这条就测不到。
	subagent.SetRunner(func(ctx context.Context) (subagent.ChildAgent, error) {
		return agent.NewLoop(ctx)
	})
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

// approve 走**两步确认**（P2-2）：先输入 auth:approve 拿回显里的确认码，再用码确认执行。
// 返回第二步的回执；若第一步就没有码（例如已超时作废），原样返回第一步的结果。
func (e *env) approve() (bool, string) {
	e.t.Helper()
	handled, prompt := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")
	if !handled {
		return false, ""
	}
	code := extractConfirmCode(prompt)
	if code == "" {
		return true, prompt
	}
	return e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+code)
}

// confirmCodeRe 从回显里抓确认码（回显那行是"确认执行请输入：auth:approve ab12cd"）。
var confirmCodeRe = regexp.MustCompile(`auth:approve ([0-9a-f]{6})`)

func extractConfirmCode(s string) string {
	if m := confirmCodeRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
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

	// P3-1：step 边界必须闭合。这次对话是"1 次工具调用 → 2 个 step"：
	// 第 1 步 handoff（调了工具、交棒），第 2 步 completed（模型不再要工具）。
	if c[session.EventStepStart] != 2 || c[session.EventStepEnd] != 2 {
		t.Fatalf("期望 2 组闭合 step，实际 start=%d end=%d", c[session.EventStepStart], c[session.EventStepEnd])
	}
	var lastReason string
	for _, dto := range e.log() {
		if dto.Type == session.EventStepEnd {
			lastReason = dto.Content
		}
	}
	if lastReason != "completed" {
		t.Fatalf("最后一步的结束原因应为结构化的 completed，实际 %q", lastReason)
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

	// 1) 应该挂起，并且**带上批准后要执行的计划**（没有计划的审批是假的）
	pending, err := (approval.RedisStore{}).GetPending(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("读挂起状态失败: %v", err)
	}
	if pending == nil {
		t.Fatal("模型调了 execute_system_defense，但没有挂起待审批动作")
	}
	if pending.Plan.Kind == "" {
		t.Fatal("挂起的提案没有带执行计划 —— 批准之后将无事可做（审批是假的）")
	}
	// 计划必须是合法的：不能是"交给 shell 解释器"那种（argv 化是 P2-1 的要求）
	if err := sandbox.Validate(pending.Plan); err != nil {
		t.Fatalf("挂起了一个非法的执行计划：%v", err)
	}
	// 而且计划里**不该出现 shell 解释器**：这个动作本质是写文件，就该用内置文件动作
	if pending.Plan.Kind != sandbox.ActionAppendFile {
		t.Fatalf("防御动作应该是内置文件动作（不经 shell），实际是 %q", pending.Plan.Kind)
	}
	if pending.Reason == "" {
		t.Fatal("挂起的提案没有原因，审计时无法追溯为什么要批")
	}

	// 2) 批准之前，绝不能真的执行
	if len(exec.calls()) != 0 {
		t.Fatalf("还没批准就已经执行了 %d 次，审批形同虚设", len(exec.calls()))
	}

	// 3) 批准 → 必须经沙箱执行器真实执行
	handled, reply := e.approve()
	if !handled {
		t.Fatal("auth:approve 没被处理")
	}
	calls := exec.calls()
	if len(calls) != 1 {
		t.Fatalf("期望恰好执行 1 次，实际 %d 次。回执=%s", len(calls), reply)
	}
	// 执行的必须**就是提案里那一个计划**（不能"批准时另拼一个"）
	if calls[0].Kind != pending.Plan.Kind || calls[0].FilePath != pending.Plan.FilePath {
		t.Fatalf("执行的计划与提案不一致：执行=%+v 提案=%+v", calls[0], pending.Plan)
	}
	// 而且计划里绝不能出现外部命令 —— 这个动作不该经过任何 shell 或子进程
	if calls[0].Command != "" {
		t.Fatalf("内置文件动作不该带外部命令，实际 command=%q（说明还在拼 shell 命令）", calls[0].Command)
	}

	// 4) 必须落一条审计事件（log-only，不喂模型，但要可追溯）
	c := e.countTypes()
	if c[session.EventAudit] == 0 {
		t.Fatal("审批执行后没有落 audit/action 审计事件，事后无法追溯")
	}

	// 5) 同一提案不能被执行两次
	if _, again := e.approve(); again != "" {
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

	// RunAgentTurn 在追加当前轮之前压缩；检查压后预算前再压一次，
	// 避免把刚追加的新轮次也算作“压完仍超限”。
	if err := e.h.Sessions.Compact(e.ctx(), e.uid, agent.Summarize); err != nil {
		t.Fatalf("最终压缩失败: %v", err)
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
	_, reply := e.approve()

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
	handled, reply := e.approve()

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
	_, reply := e.approve()

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

// P3-1（3.2）：崩溃现场除了"开放轮次"，还会留下"开放 step"和"悬空工具调用"，
// Repair 必须一起补掉 —— 否则 step 进度不可重建、投影像看到悬空的工具调用。
func TestRepairClosesOpenStepAndDanglingToolCall(t *testing.T) {
	e := newEnv(t, 9031)
	e.h = e.newHarness(&recordingExecutor{})
	store := session.RedisStore{}

	// 制造崩溃现场：开了轮次 + 开了 step + 派了工具调用，然后就死了。
	// 注意 tool/call 里带了 callId —— 这是它和结果配对的唯一凭据。
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("造数据失败: %v", err)
		}
	}
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventUserMessage, Role: "user", Content: "崩溃前那句",
	}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventStepStart, Role: "system",
	}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventToolCall, Role: "assistant",
		ToolCalls: []session.ToolCallData{{ID: "call-x", Name: "echo", Arguments: "{}"}},
	}))

	n, err := e.h.Sessions.Repair(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("Repair 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应该识别出 1 个未闭合轮次，实际 %d", n)
	}

	var sawStepEnd, sawCallResult, sawTurnEnd bool
	for _, dto := range e.log() {
		switch dto.Type {
		case session.EventStepEnd:
			sawStepEnd = true
		case session.EventToolResult:
			if dto.ToolCallID == "call-x" {
				sawCallResult = true
			}
		case session.EventTurnEnd:
			sawTurnEnd = true
		}
	}
	if !sawStepEnd {
		t.Fatal("没补出 step/end —— 开放 step 会让\"跑到第几步\"不可重建")
	}
	if !sawCallResult {
		t.Fatal("没补出悬空 tool/call 的结果 —— 投影会看到悬空的工具调用")
	}
	if !sawTurnEnd {
		t.Fatal("没补出 turn/end")
	}

	// 只追加：原始事件一条没改
	if c := e.countTypes(); c[session.EventToolCall] != 1 || c[session.EventUserMessage] != 1 {
		t.Fatalf("修复动到了历史：%v", c)
	}

	// 幂等：修完就认为自己干净了
	if n2, _ := e.h.Sessions.Repair(e.ctx(), e.uid); n2 != 0 {
		t.Fatalf("Repair 不幂等：修完还说有 %d 轮未闭合", n2)
	}
}

// P3-1（3.3a）：ResumePoint 是"续跑到哪"的观察口 —— 闭合了几个 step、末尾轮次是否还开着。
func TestResumePointReportsLastClosedStep(t *testing.T) {
	e := newEnv(t, 9032)
	e.h = e.newHarness(&recordingExecutor{})
	store := session.RedisStore{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("造数据: %v", err)
		}
	}

	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{Type: session.EventUserMessage, Role: "user", Content: "hi"}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{Type: session.EventStepStart, Role: "system"}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{Type: session.EventStepEnd, Role: "system", Content: "completed"}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{Type: session.EventStepStart, Role: "system"}))

	closed, open, err := store.ResumePoint(e.ctx(), e.uid)
	if err != nil {
		t.Fatalf("ResumePoint 失败: %v", err)
	}
	if closed != 1 {
		t.Fatalf("已闭合 step 应为 1，实际 %d", closed)
	}
	if !open {
		t.Fatal("末尾没有 turn/end，openTurn 应为 true")
	}
}

// P3-1（3.3b）：崩溃后"续跑"必须从日志接着跑，**不重放已派发的工具**。
func TestResumeTurnContinuesWithoutReplayingTool(t *testing.T) {
	e := newEnv(t, 9033)
	e.h = e.newHarness(&recordingExecutor{})
	store := session.RedisStore{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("造数据: %v", err)
		}
	}

	// 崩溃现场：用户问了话、模型派了 search_memory_archive（tool/call 已落盘，因为它是**执行前**落盘），
	// 但工具结果还没落 —— 进程死了。
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventUserMessage, Role: "user", Content: "帮我查一下历史",
	}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventStepStart, Role: "system",
	}))
	must(store.AppendEvent(e.ctx(), e.uid, session.MemoryDTO{
		Type: session.EventToolCall, Role: "assistant",
		ToolCalls: []session.ToolCallData{{ID: "call-r", Name: "search_memory_archive", Arguments: `{"query":"历史"}`}},
	}))

	before := e.countTypes()[session.EventToolCall] // = 1

	reply, resumed, err := e.h.ResumeTurn(e.ctx(), e.uid, nil)
	if err != nil {
		t.Fatalf("ResumeTurn 失败: %v", err)
	}
	if !resumed {
		t.Fatal("应该识别出可续跑的开放轮次")
	}
	if reply == "" {
		t.Fatal("续跑应产出回复")
	}

	// 关键断言：续跑**没有重放工具**（tool/call 事件数不增加）。
	// 若 ResumeTurn 是"拿原输入重跑一轮"，模型会再调一次工具 → 这里就会 > 1。
	after := e.countTypes()
	if after[session.EventToolCall] != before {
		t.Fatalf("续跑重放了工具调用（副作用会做两遍）：before=%d after=%d",
			before, after[session.EventToolCall])
	}

	// 续跑要正常收尾，且不再留开放轮次
	if after[session.EventTurnEnd] == 0 {
		t.Fatal("续跑没落 turn/end")
	}
	if _, open, _ := store.ResumePoint(e.ctx(), e.uid); open {
		t.Fatal("续跑之后不该还有开放轮次")
	}
}

// P3-1 回归：子 agent 的 step 事件**不该写进父会话日志**。
//
// 真实数据里踩过：`delegate_task` 的子 agent 与父 agent 同 userID 写同一份日志，
// 两边 step/start、step/end 交错 → 父日志的 Repair 误判"开放 step"、反复补事件（越补越多）。
func TestNestedAgentDoesNotPolluteParentStepLog(t *testing.T) {
	e := newEnv(t, 9034)
	e.h = e.newHarness(&recordingExecutor{})

	// 父 agent 调一次 delegate_task；子 agent 拿到任务文本后只回一句话。
	e.fake.SetScenario(fakemodel.Scenario{
		Default: fakemodel.Reply{Text: "子任务完成。"},
		Rules: []fakemodel.Rule{{
			Match: "派个活",
			Reply: fakemodel.Reply{ToolCalls: []fakemodel.ToolCall{
				{Name: "delegate_task", Arguments: `{"task":"随便做点事"}`},
			}},
		}},
	})

	e.run("帮我派个活")

	c := e.countTypes()
	if c[session.EventToolCall] == 0 {
		t.Fatalf("没触发 delegate_task（场景没命中？事件分布=%v）", c)
	}
	if c[session.EventStepStart] != c[session.EventStepEnd] {
		t.Fatalf("step 必须成对：start=%d end=%d —— 子 agent 的 step 漏进父日志了",
			c[session.EventStepStart], c[session.EventStepEnd])
	}
	for _, p := range session.ValidateLog(e.log()) {
		if strings.HasPrefix(p, "（提示）") {
			continue // 末尾悬空是崩溃的正常现象
		}
		t.Fatalf("父日志序列被写脏：%s", p)
	}
}

// 流被切断时：已投递的文本照存，但必须**标记为中断**，且不能写"正常收尾"。
func TestInterruptedStreamIsMarked(t *testing.T) {
	e := newEnv(t, 9019)
	e.h = e.newHarness(&recordingExecutor{})

	// 场景规则：包含"掐断"就让假模型发一片内容后把流弄坏
	partial, runErr := e.h.RunAgentTurn(e.ctx(), e.uid, "掐断", nil)
	if runErr == nil || partial == "" {
		t.Fatalf("中断必须返回部分回复及错误: reply=%q err=%v", partial, runErr)
	}

	marked := false
	for _, dto := range e.log() {
		if dto.Type == session.EventAssistantMessage && dto.Interrupted {
			marked = true
		}
		if dto.Type == session.EventTurnEnd && strings.Contains(dto.Content, "completed") {
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

// ---------------------------------------------------------------------------
// 命令 argv 化（P2-1）
// ---------------------------------------------------------------------------

// P2-1 的验收：内置文件动作**不经 shell** 也能完成，
// 而且带空格的路径与中文内容都安全（这两样正是以前拼 shell 命令时必然出问题的地方）。
func TestApprovalAppendFileNeedsNoShell(t *testing.T) {
	e := newEnv(t, 9025)
	// 用**真**执行器：这个用例要验的就是"真的写进去了"，用 fake 就没意义了
	e.h = e.newHarness(sandbox.LocalExecutor{})

	// 路径里同时放空格和中文 —— 以前这套组合会撞上 "文件名、目录名或卷标语法不正确"
	auditPath := filepath.Join(t.TempDir(), "带 空格 的目录", "defense.log")
	t.Setenv("DEFENSE_AUDIT_FILE", auditPath)

	e.run("气死我了，我很愤怒！")

	handled, reply := e.approve()
	if !handled {
		t.Fatal("auth:approve 没被处理")
	}

	// 回执里不该再出现任何 shell 解释器 —— 这是 argv 化的直接证据
	for _, bad := range []string{"cmd /c", "sh -c", "powershell"} {
		if strings.Contains(reply, bad) {
			t.Fatalf("回执里出现了 shell 调用（%q），说明计划还在拼命令串：\n%s", bad, reply)
		}
	}

	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("审批通过了但审计文件没写进去（执行链断了）：%v", err)
	}
	content := string(data)
	if !strings.Contains(content, "BLOCK-DECISION") {
		t.Fatalf("审计文件内容不对：%q", content)
	}
	// 中文要原样落盘：以前经 cmd/sh 重定向会被终端代码页转成乱码，
	// 现在字节直接写文件，与代码页无关 —— 这是根治而不是绕开。
	if !strings.Contains(content, "愤怒") {
		t.Fatalf("中文没写进审计文件（应该不经 shell 直接写）：%q", content)
	}
}

// 没有执行计划的挂起（例如被工具流水线拦下的"某次工具调用"）：
// 批准后必须**如实拒绝**，而不是假装执行过 —— 宁可留一个诚实的能力缺口。
func TestApproveWithoutPlanIsRefusedNotFaked(t *testing.T) {
	e := newEnv(t, 9026)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)

	// 手工制造这种挂起（guard 的 ask 处理就是这样：只有工具名和参数，没有 argv）
	if err := (approval.RedisStore{}).SetPending(e.ctx(), e.uid, approval.PendingAction{
		Action: "mcp__ext__some_tool", Param: "{}", Reason: "外部 MCP 工具默认需人工审批",
	}); err != nil {
		t.Fatalf("挂起失败: %v", err)
	}

	_, reply := e.approve()

	if len(exec.calls()) != 0 {
		t.Fatalf("没有执行计划却执行了 %d 次 —— 那就是凭空执行", len(exec.calls()))
	}
	if !strings.Contains(reply, "拒绝") {
		t.Fatalf("应如实拒绝执行，实际回执：\n%s", reply)
	}
	// 拒绝也是一次决策，同样要可追溯
	if c := e.countTypes(); c[session.EventAudit] == 0 {
		t.Fatal("拒绝执行也要留审计记录")
	}
}

// ---------------------------------------------------------------------------
// 审批工程细节（P2-2）
// ---------------------------------------------------------------------------

// 两步确认：只敲 auth:approve **不执行**，必须把回显里的确认码再打一遍。
func TestApprovalNeedsTwoSteps(t *testing.T) {
	e := newEnv(t, 9027)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)

	e.run("气死我了，我很愤怒！")

	// 第一步：只回显，不执行
	handled, prompt := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")
	if !handled {
		t.Fatal("auth:approve 应该被处理")
	}
	if len(exec.calls()) != 0 {
		t.Fatalf("只敲一次 auth:approve 就执行了 %d 次 —— 两步确认没生效", len(exec.calls()))
	}
	// 回显里必须能看到"将要执行什么"：人是在为这段具体内容背书
	if !strings.Contains(prompt, "将要执行") {
		t.Fatalf("回显里没有执行计划原文，人没法确认自己在批什么：\n%s", prompt)
	}

	code := extractConfirmCode(prompt)
	if code == "" {
		t.Fatalf("回显里没有确认码：\n%s", prompt)
	}

	// 错误的确认码 → 拒绝执行（挑一个和真码不同的值）
	wrong := "000000"
	if wrong == code {
		wrong = "ffffff"
	}
	if _, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+wrong); strings.Contains(reply, "已真实执行") {
		t.Fatalf("错误的确认码居然执行了：%s", reply)
	}
	if len(exec.calls()) != 0 {
		t.Fatal("错误确认码不该执行任何动作")
	}

	// 正确的确认码 → 执行
	if _, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve "+code); !strings.Contains(reply, "已真实执行") {
		t.Fatalf("正确确认码应该执行，实际：%s", reply)
	}
	if len(exec.calls()) != 1 {
		t.Fatalf("期望执行 1 次，实际 %d 次", len(exec.calls()))
	}
}

// 超时必须**显式**说出来：不能让人以为提案还挂着，也不能静默退化成"没有待审批任务"。
func TestExpiredApprovalSaysSo(t *testing.T) {
	e := newEnv(t, 9028)
	exec := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	e.h = e.newHarness(exec)

	// 手工挂一个"已经过期"的提案 —— 对应真实场景：用户过了有效期才想起来批
	past := time.Now().Add(-time.Minute)
	if err := (approval.RedisStore{}).SetPending(e.ctx(), e.uid, approval.PendingAction{
		Action:      "execute_system_defense",
		Param:       "愤怒",
		Reason:      "测试用",
		Plan:        sandbox.Request{Kind: sandbox.ActionAppendFile, FilePath: "/tmp/expired.log", FileText: "x\n"},
		RequestedAt: past.Add(-approval.TTL),
		ExpiresAt:   past,
	}); err != nil {
		t.Fatalf("挂起失败: %v", err)
	}

	handled, reply := e.h.HandleApprovalCommand(e.ctx(), e.uid, "auth:approve")
	if !handled {
		t.Fatal("超时的提案也要被当成审批命令处理，否则会掉进正常对话、用户更摸不着头脑")
	}
	if !strings.Contains(reply, "超时") {
		t.Fatalf("回执应明确说「已超时作废」，实际：\n%s", reply)
	}
	if len(exec.calls()) != 0 {
		t.Fatal("已过期的提案不该执行")
	}
}

// ---------------------------------------------------------------------------
// 指标直方图（P2-3）
// ---------------------------------------------------------------------------

// 只有"总和"的话能算出平均耗时，但看不到分布 —— 平均 200ms 到底是
// "每次 200ms"还是"99 次 10ms + 1 次 19 秒"，前者没事、后者是事故。
// 所以 /metrics 里必须有桶。
func TestMetricsExposeHistogramBuckets(t *testing.T) {
	e := newEnv(t, 9029)
	e.h = e.newHarness(&recordingExecutor{})

	e.run("你好")

	out := metrics.Default.Render()
	if !strings.Contains(out, `turn_latency_seconds_bucket{le="1"}`) {
		t.Fatalf("导出里没有 turn 耗时的桶（只有总和就看不到 P50/P99）：\n%s", out)
	}
	if !strings.Contains(out, "turn_latency_seconds_count") {
		t.Fatalf("直方图缺 _count（没有它算不出分位数）：\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// 沙箱容器后端（P2-4）
// ---------------------------------------------------------------------------

// 容器后端下审批链要**经 docker 执行**，回执要如实写 full。
//
// 用注入的假 Runner：这条测的是"链路与回执"，不是"docker 本身能不能跑"
// （后者依赖环境，不该进秒级回归）。
func TestApprovalUnderDockerBackendGoesThroughContainer(t *testing.T) {
	e := newEnv(t, 9030)
	fake := &recordingExecutor{res: sandbox.Result{ExitCode: 0}}
	// 真的 DockerExecutor，只把"最后一跳"换成假的 —— 参数拼装逻辑是真的
	e.h = e.newHarness(sandbox.DockerExecutor{Image: "alpine:3.20", Runner: fake})

	// 手工挂一个"跑外部命令"的提案：防御动作是内置文件动作、不进容器，
	// 这里要验的正是"外部命令必须经容器"那条路。
	if err := (approval.RedisStore{}).SetPending(e.ctx(), e.uid, approval.PendingAction{
		Action: "run_diagnostic",
		Param:  "echo hi",
		Reason: "测试：容器后端",
		Plan:   sandbox.Request{Command: "echo", Args: []string{"hi"}},
	}); err != nil {
		t.Fatalf("挂起失败: %v", err)
	}

	_, reply := e.approve()
	if !strings.Contains(reply, "已真实执行") {
		t.Fatalf("审批应该执行成功，实际回执：\n%s", reply)
	}

	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("期望恰好执行 1 次，实际 %d 次", len(calls))
	}
	// 关键：递到最后一跳的必须是 docker，而不是裸跑那条命令
	if calls[0].Command != "docker" {
		t.Fatalf("应经 docker 执行，实际直接跑了 %q（沙箱没生效）", calls[0].Command)
	}
	joined := strings.Join(calls[0].Args, " ")
	if !strings.Contains(joined, "--network=none") {
		t.Fatalf("docker 参数里没有隔离参数：%v", calls[0].Args)
	}
	if !strings.Contains(joined, "echo hi") {
		t.Fatalf("原命令没被带进容器：%v", calls[0].Args)
	}

	// 回执要如实写 full，而且**不该**再出现"没有任何隔离"的警告
	if !strings.Contains(reply, "full") {
		t.Fatalf("回执应写明隔离等级 full，实际：\n%s", reply)
	}
	if strings.Contains(reply, "没有任何隔离") {
		t.Fatalf("full 隔离却给了无隔离警告，回执在说谎：\n%s", reply)
	}
}
