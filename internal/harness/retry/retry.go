// Package retry 给模型调用加一层"退避重试"，并把每次重试落成日志事件。
//
// 对应 HARNESS-TODO 的 P1-3。为什么需要：一次 429 / 网络抖动 / 上游 5xx
// 就能把整轮对话打挂 —— 对真实用户来说，这表现为"聊着聊着突然报错"，
// 而这类故障绝大多数是瞬时的，重试一次就好了。
//
// 四条设计纪律（前三条来自 dsh 的 llm-retry）：
//
//  1. **只重试可重试的错误**。429 / 5xx / 连接与超时值得重试；参数错、鉴权错、
//     上下文超长，重试一百次还是同样的错，只是白等（还烧钱）。
//  2. **重试必须可见**。每次退避前回调 Observer，编排层把它落成 llm/retry 事件；
//     否则"这一轮为什么这么慢"事后完全查不出来。
//  3. **流只有"建流失败"才重试**。一旦已经开始往用户那里吐字，重试会导致重复输出，
//     那比失败更糟。流中途断掉是另一件事（P1-2：标 interrupted，交给 Repair），不是重试。
//  4. **不猜状态码**。底层 SDK 的 APIError / RequestError 会把自己的状态码
//     格式化进 Error() 文本（"error, status code: 429, ..."），所以解析它即可 ——
//     好处是不用把具体 SDK 类型写成依赖（换 SDK 时这里不用改）。
package retry

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Policy 退避策略。默认值对齐 dsh 的 llm-retry：
// 500ms 起、指数退避、上限 10s、抖动 ±10%、最多 5 次。
type Policy struct {
	// MaxAttempts 最多重试几次（**不含**首次调用）。
	MaxAttempts int
	// BaseDelay 第一次退避的时长（之后每次翻倍）。
	BaseDelay time.Duration
	// MaxDelay 单次退避的上限。
	MaxDelay time.Duration
	// Jitter 抖动比例（0~1）。为什么要抖动：多个会话同时被限流时，
	// 固定退避会让它们在同一时刻一起重试，把上游再打挂一次（惊群）。
	Jitter float64
}

// DefaultPolicy 生产默认策略。
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts: 5,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Jitter:      0.1,
	}
}

// Backoff 返回第 attempt 次重试前应等待的时长（attempt 从 0 开始）。
// 纯函数，好测：指数增长 → 夹上限 → 加对称抖动 → 不出现负数。
func (p Policy) Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := float64(p.BaseDelay)
	if base <= 0 {
		return 0
	}
	d := base
	for i := 0; i < attempt && d < float64(p.MaxDelay); i++ {
		d *= 2
	}
	if max := float64(p.MaxDelay); max > 0 && d > max {
		d = max
	}
	if p.Jitter > 0 {
		// 对称抖动：(1 ± jitter)。乘性抖动比加性更合理 —— 退避越长抖得越多。
		d *= 1 + p.Jitter*(2*rand.Float64()-1)
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// Observer 即将退避重试时的回调。
// attempt 从 1 开始（"这是第几次重试"），reason 是分类原因（rate_limit / server / timeout / transport）。
type Observer func(attempt int, reason string, backoff time.Duration)

// Config 包装参数。
type Config struct {
	Policy Policy
	// Observer 每次退避前调用；为 nil 则只有重试、没有记录（测试可用）。
	Observer Observer
	// InitialAttempt 已经用掉的重试次数（同一轮内多次模型调用共享预算，见 harness 侧）。
	InitialAttempt int
}

// Wrap 把一个 ChatModel 包成"带重试"的模型。
//
// 用嵌入而不是重写接口：BindTools 之类的其余方法原样透传，
// 以后 Eino 给 ChatModel 加方法也不用改这里。
func Wrap(inner model.ChatModel, cfg Config) model.ChatModel {
	if inner == nil {
		return nil
	}
	if cfg.Policy.MaxAttempts <= 0 {
		cfg.Policy = DefaultPolicy()
	}
	return &retrying{ChatModel: inner, cfg: cfg}
}

type retrying struct {
	// 匿名字段（嵌入）：没被覆写的方法（BindTools 等）自动透传，
	// 以后 Eino 给 ChatModel 加方法也不用改这里。
	model.ChatModel
	cfg Config
}

// Generate 全有或全无的一次调用，可以整体重试。
func (r *retrying) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		out, err := r.ChatModel.Generate(ctx, input, opts...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !r.shouldRetry(ctx, err, attempt) {
			return nil, err
		}
		if err := r.pause(ctx, attempt+1, err); err != nil {
			// 调用方取消了：把**原始错误**返回，而不是 ctx 的错误 ——
			// 排查的人需要知道"是上游 503 之后用户关掉了页面"，不是只看到 context canceled。
			return nil, lastErr
		}
	}
}

// Stream 只在**建立流**失败时重试。
//
// 关键：一旦拿到 StreamReader 就立刻返回，绝不因为"后面读的时候可能出错"而重试 ——
// 那时用户可能已经看到半截回复，重试会造成重复输出。流中途断掉由 P1-2 的中断语义处理。
func (r *retrying) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		sr, err := r.ChatModel.Stream(ctx, input, opts...)
		if err == nil {
			return sr, nil
		}
		lastErr = err
		if !r.shouldRetry(ctx, err, attempt) {
			return nil, err
		}
		if err := r.pause(ctx, attempt+1, err); err != nil {
			return nil, lastErr
		}
	}
}

// shouldRetry 是否还要再试一次。attempt 是"已经重试过的次数"。
func (r *retrying) shouldRetry(ctx context.Context, err error, attempt int) bool {
	// 预算按"本轮已用掉的重试数 + 本次已重试数"算：同一轮里 agent 会调模型很多次
	// （每次工具调用之后都要调一次），预算是整轮的，不是每次调用的 —— 否则
	// 一个坏上游能让你重试 5 次 × N 次模型调用。
	if r.cfg.InitialAttempt+attempt >= r.cfg.Policy.MaxAttempts {
		return false
	}
	// 调用方已经取消 / 超时：再退避下去只是占着资源不放。
	if ctx.Err() != nil {
		return false
	}
	return IsRetryable(err)
}

// pause 退避等待；期间若 ctx 被取消则返回错误。
func (r *retrying) pause(ctx context.Context, attempt int, err error) error {
	delay := r.cfg.Policy.Backoff(attempt - 1)
	if r.cfg.Observer != nil {
		r.cfg.Observer(attempt, Reason(err), delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// statusCodeRe 从 SDK 错误文本里取 HTTP 状态码：SDK 自己的格式是
// "error, status code: 429, status: 429 Too Many Requests, message: ..."。
var statusCodeRe = regexp.MustCompile(`status code: (\d{3})`)

// StatusCode 尽力解析错误里的 HTTP 状态码；解析不出返回 0。
func StatusCode(err error) int {
	if err == nil {
		return 0
	}
	m := statusCodeRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// IsRetryable 判断一个错误值不值得重试。纯函数（除了 net 接口判定），好测。
//
// 判定顺序有讲究：**先排除一定不能重试的**，再认可以重试的。
// 反过来写的话，一条 "400 ... timeout" 这种混着来的消息会被误判成可重试。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// 取消：重试会掩盖"用户不想等了"这个事实。
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true // 上游慢是典型瞬时故障
	}
	// 连接层错误：多半是瞬时抖动。
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	msg := strings.ToLower(err.Error())

	// ① 这些重试一百次也是同样的错，白等还烧钱。
	for _, s := range nonRetryableMarkers {
		if strings.Contains(msg, s) {
			return false
		}
	}

	// ② 有状态码就按状态码判（比字符串靠谱）。
	if code := StatusCode(err); code > 0 {
		return code == 429 || code >= 500
	}

	// ③ 没有状态码（连接层问题），退回关键词。
	for _, s := range retryableMarkers {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// Reason 把错误归类成一个短标签，写进 llm/retry 事件里 ——
// 事后排查时"重试了 3 次"远不如"重试了 3 次，全是 rate_limit"有用。
func Reason(err error) string {
	if err == nil {
		return "unknown"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "transport"
	}
	if code := StatusCode(err); code > 0 {
		switch {
		case code == 429:
			return "rate_limit"
		case code >= 500:
			return "server"
		default:
			return "http_" + strconv.Itoa(code)
		}
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"timeout", "timed out"} {
		if strings.Contains(msg, s) {
			return "timeout"
		}
	}
	for _, s := range []string{"connection", "reset by peer", "broken pipe", "no such host", "eof"} {
		if strings.Contains(msg, s) {
			return "transport"
		}
	}
	return "unknown"
}

// nonRetryableMarkers 命中即不重试（参数/鉴权/内容类，重试没意义）。
var nonRetryableMarkers = []string{
	"invalid_request", "invalid request", "invalid api key", "unauthorized", "forbidden",
	"not found", "context length", "context_length", "maximum context", "too long",
	"insufficient", "quota exceeded", "content filter", "invalid parameter",
	"400 ", "401 ", "403 ", "404 ",
}

// retryableMarkers 没有状态码可依据时的兜底关键词（连接层故障）。
var retryableMarkers = []string{
	"timeout", "timed out", "connection refused", "connection reset", "reset by peer",
	"broken pipe", "no such host", "temporary failure", "server error", "internal error",
	"service unavailable", "overloaded", "rate limit", "too many requests", "eof",
}
