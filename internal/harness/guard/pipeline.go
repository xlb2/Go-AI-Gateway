package guard

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"go_im_gateway/internal/harness/metrics"
)

// Decision 把关判定三态（对应 codex 的 SafetyCheck / execpolicy Decision）。
//
// 常量顺序就是**严格度顺序**，tighten() 直接按大小取更严的那个，
// 所以这里不能随便调：Deny 比 Ask 更严——Ask 还留了"人工批准就放行"的口子，
// Deny 是终点。顺序搞反的话，一条 deny 会被后来的 ask 悄悄放水。
type Decision int

const (
	// Allow 放行
	Allow Decision = iota
	// Ask 挂起等人工审批（还有救）
	Ask
	// Deny 拒绝（终点）
	Deny
)

func (d Decision) String() string {
	switch d {
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "allow"
	}
}

// Call 一次工具调用的把关上下文。
type Call struct {
	UserID     uint
	Tool       string
	Args       string // 原始 JSON 入参
	ToolCallID string
}

// Verdict 一次判定的结果。
type Verdict struct {
	Decision Decision
	Reason   string
}

// allow / deny / ask 是判定构造器（写起来短一点，读起来像策略）。
func allow() Verdict                { return Verdict{Decision: Allow} }
func deny(reason string) Verdict    { return Verdict{Decision: Deny, Reason: reason} }
func ask(reason string) Verdict     { return Verdict{Decision: Ask, Reason: reason} }

// tighten 把两个判定合并成"更严的那个"：Deny > Ask > Allow。
//
// 这就是把关的**单调性**：后加的规则只能收紧、永远不能把别人收紧的结果放开。
// 没有这条，任意一个后来注册的"宽松"检查就能把安全层的 deny 悄悄架空。
func (v Verdict) tighten(o Verdict) Verdict {
	if o.Decision > v.Decision {
		return o
	}
	if o.Decision == v.Decision && v.Reason == "" {
		return o
	}
	return v
}

// Check 一个把关点：只看"要调用谁、参数是什么"，给出判定。
// 不允许有副作用——判定和执行必须分开，否则拒绝一个调用也会留下痕迹。
type Check func(ctx context.Context, call Call) Verdict

// PostFunc 执行后的结果改写（脱敏、截断、补上下文）。
type PostFunc func(ctx context.Context, call Call, result string) string

// AskHandler 判定为 Ask 时怎么挂起（由编排层注入，避免 guard 反向依赖 approval 包）。
type AskHandler func(ctx context.Context, call Call, reason string) (string, error)

// Pipeline 工具执行流水线：pre-execute → guard → execute → post-execute（对应 HARNESS-STUDY M5）。
//
// 四道关各自解决一件事：
//
//	pre-execute  业务策略：这个调用该不该做（钩子可拔插，allow/deny/ask）
//	guard        安全不变量：单调收紧，插件再怎么注册也放不开（Deny > Ask > Allow）
//	execute      真执行：超时 + 指标包住，失败不炸对话
//	post-execute 结果处理：脱敏/截断后再交给模型
type Pipeline struct {
	pre       []Check
	guards    []Check
	post      []PostFunc
	ask       AskHandler
	timeout   time.Duration
	// retries 默认 0：工具调用大多有副作用（写文件/发请求），
	// 失败就重试等于把副作用做两遍。只有确认幂等的工具才该开重试。
	retries int
}

// NewPipeline 构造一条带内置守卫的流水线。
func NewPipeline(ask AskHandler) *Pipeline {
	p := &Pipeline{
		ask:     ask,
		timeout: defaultToolTimeout,
		retries: 0,
	}
	p.Guard(guardApprovalRequired) // 安全不变量：登记过的工具必须走审批
	p.Guard(guardExternalTools)    // 安全不变量：外部（MCP）工具必须走审批
	p.Pre(limitArgsSize)           // 业务策略：入参过大直接拒（防上下文炸弹）
	p.Post(redactSecrets)          // 结果脱敏：密钥类字符串不进模型上下文
	return p
}

// Pre 注册一个 pre-execute 把关点。
func (p *Pipeline) Pre(f Check) *Pipeline { p.pre = append(p.pre, f); return p }

// Guard 注册一个单调守卫。
func (p *Pipeline) Guard(f Check) *Pipeline { p.guards = append(p.guards, f); return p }

// Post 注册一个结果改写。
func (p *Pipeline) Post(f PostFunc) *Pipeline { p.post = append(p.post, f); return p }

// SetTimeout 覆盖默认执行超时。
func (p *Pipeline) SetTimeout(d time.Duration) *Pipeline { p.timeout = d; return p }

// Wrap 把一个工具包成"带流水线"的工具；Info 原样透传（对模型暴露的 schema 不变）。
// 不是可调用工具（只有 Info 的 BaseTool）原样返回——流水线拦不住它，也不假装拦住了。
func (p *Pipeline) Wrap(t tool.BaseTool) tool.BaseTool {
	if t == nil {
		return nil
	}
	invokable, ok := t.(tool.InvokableTool)
	if !ok {
		return t
	}
	return &gatedTool{pipeline: p, inner: invokable}
}

type gatedTool struct {
	pipeline *Pipeline
	inner    tool.InvokableTool
}

func (g *gatedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return g.inner.Info(ctx)
}

// InvokableRun 工具真正被调用时走完整条流水线。
func (g *gatedTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	p := g.pipeline
	info, _ := g.inner.Info(ctx)
	name := "unknown"
	if info != nil && info.Name != "" {
		name = info.Name
	}
	call := Call{UserID: userFromCtx(ctx), Tool: name, Args: argumentsInJSON}

	// ---- 第 1、2 道关：判定（pre 之后再过 guard，合并规则是"更严者胜"）----
	verdict := allow()
	for _, f := range p.pre {
		verdict = verdict.tighten(f(ctx, call))
	}
	for _, f := range p.guards {
		verdict = verdict.tighten(f(ctx, call))
	}
	metrics.Default.Inc("tool_calls_total")

	switch verdict.Decision {
	case Deny:
		metrics.Default.Inc("tool_denied_total")
		// 用"工具结果"而不是 error 返回：拒绝是正常业务结果，
		// 要让模型看见原因并自己改道，而不是把整轮对话炸掉。
		return fmt.Sprintf("⛔ 该工具调用被把关拒绝（%s），未执行。原因：%s", name, verdict.Reason), nil
	case Ask:
		metrics.Default.Inc("tool_ask_total")
		if p.ask == nil {
			// 拿不准时的默认值：没人能审批就拒绝（fail-closed）——
			// 审批是"人没介入就不该做"，和限流 fail-open 是相反的一边。
			return fmt.Sprintf("⛔ 该工具调用需要人工审批但当前无法挂起（%s），已按 fail-closed 拒绝。原因：%s", name, verdict.Reason), nil
		}
		msg, err := p.ask(ctx, call, verdict.Reason)
		if err != nil {
			return "", fmt.Errorf("挂起审批失败: %w", err)
		}
		return msg, nil
	}

	// ---- 第 3 道关：真执行（超时 + 指标包住）----
	out, err := g.execute(ctx, call, argumentsInJSON, opts...)

	// ---- 第 4 道关：结果改写 ----
	if err == nil {
		for _, f := range p.post {
			out = f(ctx, call, out)
		}
	}
	return out, err
}

// defaultToolTimeout 单次工具执行的默认超时。
const defaultToolTimeout = 120 * time.Second

var (
	timeoutMu    sync.RWMutex
	toolTimeouts = map[string]time.Duration{}
)

// SetToolTimeout 给某个工具单独设超时。
//
// 为什么必须有：`delegate_tasks` 这类工具内部要再跑一轮模型（比如让子 agent 写长文），
// 耗时远超普通工具；一个统一的短超时会把它们直接误杀。
// "一刀切超时"是把关层最容易踩的坑——工具之间耗时差几个数量级，策略就得能按工具分开配。
func SetToolTimeout(toolName string, d time.Duration) {
	timeoutMu.Lock()
	defer timeoutMu.Unlock()
	toolTimeouts[toolName] = d
}

// toolTimeoutFor 取某个工具的超时覆盖，没有则返回 0。
func toolTimeoutFor(toolName string) time.Duration {
	timeoutMu.RLock()
	defer timeoutMu.RUnlock()
	return toolTimeouts[toolName]
}

// execute 第 3 道关：真执行（超时 + 指标包住）。
func (g *gatedTool) execute(ctx context.Context, call Call, args string, opts ...tool.Option) (string, error) {
	p := g.pipeline
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultToolTimeout
	}
	if d := toolTimeoutFor(call.Tool); d > 0 {
		timeout = d
	}
	var lastErr error
	for attempt := 0; attempt <= p.retries; attempt++ {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		out, err := g.inner.InvokableRun(runCtx, args, opts...)
		elapsed := time.Since(start).Seconds()
		cancel()

		metrics.Default.Add("tool_duration_seconds_total", elapsed)
		if err == nil {
			return out, nil
		}
		lastErr = err
		metrics.Default.Inc("tool_errors_total")
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			// 超时多半不是"偶然抖动"，重试只会再等一个 timeout，直接放弃
			return fmt.Sprintf("⏱️ 工具 %s 执行超时(%s)，已终止。", call.Tool, timeout), nil
		}
	}
	// 执行失败也不掀桌子：把错误包成工具结果交给模型，让它决定下一步
	return fmt.Sprintf("⚠️ 工具 %s 执行失败：%v", call.Tool, lastErr), nil
}


// ---- 内置守卫 ----

// approvalRequired 工具名 → 必须先人工审批的原因。
//
// 这是把"某个工具必须审批"从**工具自己的实现细节**提升成**流水线的不变量**：
// 工具体内怎么写都不影响它，守卫层永远会把它变成 ask。
// 默认空表——需要强制审批的工具由调用方在启动时用 RequireApproval 登记
// （项目当前真正执行高危动作的是审批通过后的命令，不经过工具层，所以默认没有必须登记的工具）。
var (
	approvalMu       sync.RWMutex
	approvalRequired = map[string]string{}
)

// RequireApproval 把某个工具登记为"调用前必须人工审批"。
func RequireApproval(toolName, reason string) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	approvalRequired[toolName] = reason
}

// RequiredApprovals 返回当前登记表的副本（启动日志/排查用）。
func RequiredApprovals() map[string]string {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	out := make(map[string]string, len(approvalRequired))
	for k, v := range approvalRequired {
		out[k] = v
	}
	return out
}

func guardApprovalRequired(_ context.Context, call Call) Verdict {
	approvalMu.RLock()
	reason, ok := approvalRequired[call.Tool]
	approvalMu.RUnlock()
	if ok {
		return ask(reason)
	}
	return allow()
}

// externalToolPrefix MCP 桥进来的工具名前缀（mcp__server__tool）。
const externalToolPrefix = "mcp__"

// trustedExternalServers 允许自动放行的外部 server（默认空 = 全部要审批）。
var (
	trustedMu              sync.RWMutex
	trustedExternalServers = map[string]bool{}
)

// TrustExternalServer 把某个 MCP server 标记为可信任（它的工具自动放行）。
func TrustExternalServer(server string) {
	trustedMu.Lock()
	defer trustedMu.Unlock()
	trustedExternalServers[server] = true
}

// guardExternalTools 外部（MCP）工具默认必须人工审批：
// 它们的实现不在本项目里，不能默认当成自己人。
func guardExternalTools(_ context.Context, call Call) Verdict {
	if len(call.Tool) <= len(externalToolPrefix) || call.Tool[:len(externalToolPrefix)] != externalToolPrefix {
		return allow()
	}
	rest := call.Tool[len(externalToolPrefix):]
	server := rest
	for i := 0; i < len(rest); i++ {
		if rest[i] == '_' {
			server = rest[:i]
			break
		}
	}
	trustedMu.RLock()
	trusted := trustedExternalServers[server]
	trustedMu.RUnlock()
	if trusted {
		return allow()
	}
	return ask(fmt.Sprintf("外部 MCP 工具（server=%s）默认需人工审批", server))
}

// 工具入参大小上限：防止一个巨型参数把上下文/日志撑爆。
const (
	maxToolArgsBytes = 8 * 1024
	// largeArgCap 合法地"吃大入参"的工具单独放宽到 256KB。
	// 不能只有一把尺子：store_large_content 的存在意义就是承接大段内容，
	// 用 8KB 去卡它等于把这个工具废掉——策略要按工具分，不能一刀切。
	largeArgCap = 256 * 1024
)

// largeArgTools 允许多大入参的工具白名单。
var largeArgTools = map[string]bool{
	"store_large_content": true,
}

func limitArgsSize(_ context.Context, call Call) Verdict {
	limit := maxToolArgsBytes
	if largeArgTools[call.Tool] {
		limit = largeArgCap
	}
	if len(call.Args) > limit {
		return deny(fmt.Sprintf("入参 %d 字节超过该工具上限 %d，拒绝执行（大内容请用 store_large_content 外存）",
			len(call.Args), limit))
	}
	return allow()
}

// ---- 内置 post ----

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(sk|rk)-[A-Za-z0-9]{16,}\b`),      // OpenAI/Stripe 风格
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                  // AWS Access Key
	regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?key|token)\s*[:=]\s*["']?[A-Za-z0-9_\-]{16,}`), // key=xxx
}

// redactSecrets 工具结果进模型上下文之前先给密钥类字符串打码。
// 为什么放 post 而不是让每个工具自己做：脱敏是横切需求，
// 工具作者会忘，而这里漏一个就漏一批（对应 M5 的"post-execute 可改写结果"）。
func redactSecrets(_ context.Context, _ Call, result string) string {
	for _, re := range secretPatterns {
		result = re.ReplaceAllStringFunc(result, func(m string) string {
			if len(m) <= 8 {
				return "***"
			}
			return m[:4] + "***REDACTED***"
		})
	}
	return result
}

// userFromCtx 从 context 里取 user_id（和 agent 包用同一个 key，跨包只认这一个约定）。
func userFromCtx(ctx context.Context) uint {
	v := ctx.Value("user_id")
	switch t := v.(type) {
	case uint:
		return t
	case int:
		return uint(t)
	case float64:
		return uint(t)
	default:
		return 0
	}
}
