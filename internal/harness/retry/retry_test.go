package retry

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// 退避要"指数增长 → 夹上限"，否则上游挂了的时候退避会涨到没法看。
func TestBackoffGrowsThenCaps(t *testing.T) {
	p := Policy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 400 * time.Millisecond}
	want := []time.Duration{100, 200, 400, 400, 400}
	for i, w := range want {
		if got := p.Backoff(i); got != w*time.Millisecond {
			t.Fatalf("第 %d 次退避 = %s，期望 %s", i, got, w*time.Millisecond)
		}
	}
}

// 抖动必须留在 ±jitter 之内：抖出去太多会让"最坏等待时间"不可预测。
func TestBackoffJitterStaysWithinRange(t *testing.T) {
	p := Policy{BaseDelay: 1000 * time.Millisecond, MaxDelay: time.Minute, Jitter: 0.1}
	for i := 0; i < 500; i++ {
		d := p.Backoff(0)
		if d < 900*time.Millisecond || d > 1100*time.Millisecond {
			t.Fatalf("抖动后 %s 超出 ±10%% 范围（第 %d 次）", d, i)
		}
	}
	// 不配 BaseDelay 不能变成负数或 panic
	if got := (Policy{}).Backoff(3); got != 0 {
		t.Fatalf("BaseDelay 为 0 时应返回 0，实际 %s", got)
	}
}

// 只重试"再试一次可能好"的错误。参数错/鉴权错重试一百次还是同样的错 —— 白等还烧钱。
func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"429 限流", errors.New("error, status code: 429, status: 429 Too Many Requests, message: slow down"), true},
		{"503 不可用", errors.New("error, status code: 503, status: 503 Service Unavailable, message: upstream"), true},
		{"500 服务端错", errors.New("error, status code: 500, status: 500, message: boom"), true},
		{"400 参数错", errors.New("error, status code: 400, status: 400, message: invalid_request"), false},
		{"401 鉴权错", errors.New("error, status code: 401, status: 401, message: unauthorized"), false},
		{"上下文超长", errors.New("error, status code: 400, message: maximum context length exceeded"), false},
		{"连接被拒", errors.New("dial tcp 127.0.0.1:9099: connect: connection refused"), true},
		{"连接被重置", errors.New("read tcp: connection reset by peer"), true},
		{"超时", context.DeadlineExceeded, true},
		{"用户取消", context.Canceled, false},
		{"流被截断", io.ErrUnexpectedEOF, true},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsRetryable(c.err); got != c.want {
			t.Errorf("%s: IsRetryable = %v，期望 %v（%v）", c.name, got, c.want, c.err)
		}
	}
}

// 混合消息要以"不重试"优先：一条 "400 ... timeout" 不能被 timeout 骗去重试。
func TestIsRetryablePrefersNonRetryable(t *testing.T) {
	err := errors.New("error, status code: 400, message: invalid_request: request timeout is not supported")
	if IsRetryable(err) {
		t.Fatal("400 参数错不该因为文本里出现 timeout 就被重试")
	}
}

// 状态码解析：SDK 会把它格式化进错误文本，别去依赖 SDK 的具体类型。
func TestStatusCode(t *testing.T) {
	if got := StatusCode(errors.New("error, status code: 429, status: 429, message: x")); got != 429 {
		t.Fatalf("StatusCode = %d，期望 429", got)
	}
	if got := StatusCode(errors.New("connection refused")); got != 0 {
		t.Fatalf("没有状态码时应返回 0，实际 %d", got)
	}
	if got := StatusCode(nil); got != 0 {
		t.Fatalf("nil 应返回 0，实际 %d", got)
	}
}

// Reason 是写进日志的短标签：事后"重试 3 次"远不如"重试 3 次全是 rate_limit"有用。
func TestReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("error, status code: 429, message: slow down"), "rate_limit"},
		{errors.New("error, status code: 503, message: unavailable"), "server"},
		{errors.New("error, status code: 400, message: bad"), "http_400"},
		{context.DeadlineExceeded, "timeout"},
		{io.EOF, "transport"},
		{errors.New("dial tcp: connection refused"), "transport"},
		{nil, "unknown"},
	}
	for _, c := range cases {
		if got := Reason(c.err); got != c.want {
			t.Errorf("Reason(%v) = %s，期望 %s", c.err, got, c.want)
		}
	}
}
