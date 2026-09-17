package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/session"
)

// 这些用例只测**循环本体**：注入假模型 + 计数工具，不碰 Redis、不碰真模型。
// 之所以能这么测：ownLoop 的 model/tools 是字段，同包测试可以直接构造。

// ---------- 测试替身：脚本化模型 ----------

type streamStep func(ctx context.Context) (*schema.StreamReader[*schema.Message], error)

type fakeModel struct {
	steps  []streamStep
	inputs [][]*schema.Message // 每次 Stream 收到的 input（用来验"工具结果有没有回灌"）
	i      int
}

func (m *fakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("fakeModel: Generate 不该被调用")
}

func (m *fakeModel) BindTools([]*schema.ToolInfo) error { return nil }

func (m *fakeModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	cp := make([]*schema.Message, len(input))
	copy(cp, input)
	m.inputs = append(m.inputs, cp)

	if m.i >= len(m.steps) {
		return nil, errors.New("fakeModel: 没有更多脚本流了")
	}
	f := m.steps[m.i]
	m.i++
	return f(ctx)
}

// streamOf 用 schema.Pipe 造一条"发完就正常结束"的流。
func streamOf(msgs ...*schema.Message) *schema.StreamReader[*schema.Message] {
	sr, sw := schema.Pipe[*schema.Message](len(msgs) + 1)
	go func() {
		defer sw.Close()
		for _, m := range msgs {
			sw.Send(m, nil)
		}
	}()
	return sr
}

func streamText(text string) streamStep {
	return func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		return streamOf(&schema.Message{Role: schema.Assistant, Content: text}), nil
	}
}

// ---------- 测试替身：计数工具 ----------

type countingTool struct {
	name  string
	out   string
	calls []string
}

func (t *countingTool) Name() string { return t.name }

func (t *countingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *countingTool) InvokableRun(_ context.Context, args string, _ ...tool.Option) (string, error) {
	t.calls = append(t.calls, args)
	return t.out, nil
}

func newTestLoop(m model.ChatModel, tools ...tool.InvokableTool) *ownLoop {
	byName := make(map[string]tool.InvokableTool, len(tools))
	for _, tl := range tools {
		if info, err := tl.Info(context.Background()); err == nil && info != nil {
			byName[info.Name] = tl
		}
	}
	return &ownLoop{model: m, tools: tools, byName: byName, maxSteps: 5}
}

// drainAll 读干输出流，返回拼接文本与结束错误（io.EOF 归一成 nil）。
func drainAll(t *testing.T, sr *schema.StreamReader[*schema.Message]) (string, error) {
	t.Helper()
	defer sr.Close()
	var sb strings.Builder
	for {
		msg, err := sr.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return sb.String(), nil
			}
			return sb.String(), err
		}
		sb.WriteString(msg.Content)
	}
}

func sawToolResult(msgs []*schema.Message, callID, want string) bool {
	for _, m := range msgs {
		if m.Role == schema.Tool && m.ToolCallID == callID && strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

// ---------- 用例 ----------

func TestOwnLoop_CancelSilentUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	m := &fakeModel{steps: []streamStep{func(ctx context.Context) (*schema.StreamReader[*schema.Message], error) {
		r, w := schema.Pipe[*schema.Message](0)
		go func() { defer close(stopped); defer w.Close(); <-ctx.Done() }()
		return r, nil
	}}}
	r, err := newTestLoop(m).Stream(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	finished := make(chan error, 1)
	go func() { _, err := r.Recv(); finished <- err }()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("silent read did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream did not stop")
	}
}

func TestOwnLoop_CancelFullOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	filled, stopped := make(chan struct{}), make(chan struct{})
	m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		r, w := schema.Pipe[*schema.Message](0)
		go func() {
			defer close(stopped)
			defer w.Close()
			for i := 0; ; i++ {
				if i == 9 {
					close(filled)
				}
				if w.Send(schema.AssistantMessage("x", nil), nil) {
					return
				}
			}
		}()
		return r, nil
	}}}
	r, err := newTestLoop(m).Stream(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	select {
	case <-filled:
	case <-time.After(time.Second):
		t.Fatal("output did not fill")
	}
	cancel()
	// 此时仍不读取输出；生产者必须自己退出，而不是靠测试排空缓冲。
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("producer blocked after cancellation")
	}
	finished := make(chan error, 1)
	go func() {
		for {
			_, err := r.Recv()
			if err != nil {
				finished <- err
				return
			}
		}
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation not delivered")
	}
}

func TestOwnLoop_TruncationIsError(t *testing.T) {
	m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		return streamOf(&schema.Message{Role: schema.Assistant, Content: "partial", ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}}), nil
	}}}
	sr, err := newTestLoop(m).Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	text, err := drainAll(t, sr)
	if text != "partial" || !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestOwnLoop_MaxStepsDoesNotRequestNextModel(t *testing.T) {
	call := schema.ToolCall{ID: "limit", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`}}
	m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		return streamOf(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}), nil
	}, streamText("extra")}}
	l := newTestLoop(m, &countingTool{name: "echo"})
	l.maxSteps = 1
	sr, err := l.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainAll(t, sr)
	if !errors.Is(err, ErrMaxSteps) || m.i != 1 {
		t.Fatalf("model calls=%d err=%v", m.i, err)
	}
}

func TestToolPersistenceBarrier(t *testing.T) {
	for _, failKind := range []string{session.EventToolCall, session.EventToolResult, ""} {
		t.Run("fail="+failKind, func(t *testing.T) {
			counter := &countingTool{name: "echo", out: "ok"}
			l := newTestLoop(nil, counter)
			boom := errors.New("storage unavailable")
			writes := 0
			l.writeEvents = func(_ context.Context, _ uint, events []session.MemoryDTO) error {
				writes++
				if events[0].Type == session.EventToolCall && len(counter.calls) != 0 {
					t.Fatal("tool ran before call was recorded")
				}
				if events[0].Type == failKind {
					return boom
				}
				return nil
			}
			calls := []schema.ToolCall{
				{ID: "a", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`}},
				{ID: "b", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`}},
			}
			out, err := l.executeTools(context.Background(), 7, calls)
			wantCalls := 2
			if failKind == session.EventToolCall {
				wantCalls = 0
			}
			if failKind == session.EventToolResult {
				wantCalls = 1
			}
			if len(counter.calls) != wantCalls {
				t.Fatalf("executed %d tools, want %d", len(counter.calls), wantCalls)
			}
			if failKind != "" {
				if !errors.Is(err, boom) || len(out) != 0 {
					t.Fatalf("failure not propagated: out=%v err=%v", out, err)
				}
			} else if err != nil || len(out) != 2 || writes != 3 {
				t.Fatalf("successful path: writes=%d out=%v err=%v", writes, out, err)
			}
		})
	}
}

func TestToolPersistenceFailureStopsModelHandoff(t *testing.T) {
	call := schema.ToolCall{ID: "barrier", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`}}
	m := &fakeModel{steps: []streamStep{func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		return streamOf(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}), nil
	}, streamText("must not run")}}
	counter := &countingTool{name: "echo"}
	l := newTestLoop(m, counter)
	boom := errors.New("write failed")
	l.writeEvents = func(context.Context, uint, []session.MemoryDTO) error { return boom }
	ctx := context.WithValue(context.Background(), "user_id", uint(7))
	sr, err := l.Stream(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainAll(t, sr)
	if !errors.Is(err, boom) || m.i != 1 || len(counter.calls) != 0 {
		t.Fatalf("barrier failed: err=%v model_calls=%d tool_calls=%d", err, m.i, len(counter.calls))
	}
}

// 契约：模型不再要工具 → 一步收尾，文本原样流出。
func TestOwnLoop_TextOnly_StreamsAndCompletes(t *testing.T) {
	m := &fakeModel{steps: []streamStep{streamText("你好呀")}}
	l := newTestLoop(m, &countingTool{name: "echo", out: "x"})

	out, err := l.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("建流不该失败: %v", err)
	}
	text, rerr := drainAll(t, out)
	if rerr != nil {
		t.Fatalf("正常结束应得到 io.EOF（归一为 nil），实际 %v", rerr)
	}
	if text != "你好呀" {
		t.Fatalf("文本被改写：%q", text)
	}
	if m.i != 1 {
		t.Fatalf("只该调模型 1 次，实际 %d", m.i)
	}
}

// 契约：有工具调用 → 交棒（执行工具、把结果喂回）→ 再调模型直到说完。
func TestOwnLoop_ToolCall_HandsOffAndFeedsBack(t *testing.T) {
	call := schema.ToolCall{ID: "c1", Function: schema.FunctionCall{Name: "echo", Arguments: "ping"}}
	m := &fakeModel{steps: []streamStep{
		func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			return streamOf(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}), nil
		},
		streamText("done"),
	}}
	et := &countingTool{name: "echo", out: "echoed: ping"}
	l := newTestLoop(m, et)

	out, err := l.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatalf("建流不该失败: %v", err)
	}
	text, rerr := drainAll(t, out)
	if rerr != nil {
		t.Fatalf("正常结束应得到 io.EOF，实际 %v", rerr)
	}
	if text != "done" {
		t.Fatalf("最终文本应为 done，实际 %q", text)
	}
	if len(et.calls) != 1 || et.calls[0] != "ping" {
		t.Fatalf("工具应恰好执行一次且参数为 ping，实际 %v", et.calls)
	}
	// 关键：第二次调模型必须能看到工具结果，否则模型永远接不上、会重复调工具。
	if len(m.inputs) != 2 {
		t.Fatalf("应调模型 2 次（工具前 + 工具后），实际 %d", len(m.inputs))
	}
	if !sawToolResult(m.inputs[1], "c1", "echoed: ping") {
		t.Fatalf("第二次输入没带工具结果：%+v", m.inputs[1])
	}
}

// 契约：**建流失败必须同步返回 error**（不是 Recv 时才抛）—— TestNonRetryableErrorFailsFast 靠它。
func TestOwnLoop_FirstStreamError_IsSynchronous(t *testing.T) {
	m := &fakeModel{steps: []streamStep{
		func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("400 bad request")
		},
	}}
	l := newTestLoop(m, &countingTool{name: "echo"})

	if _, err := l.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")}); err == nil {
		t.Fatal("建流失败必须从 Stream 同步返回 error")
	}
}

// 契约：调用方取消上下文后，输出流要以 ctx 的错误收尾 —— 不再往 pipe 里写（否则可能阻塞、泄漏 goroutine）。
func TestOwnLoop_ContextCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := &fakeModel{steps: []streamStep{
		func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				time.Sleep(30 * time.Millisecond) // 让 cancel 先发生
				sw.Send(&schema.Message{Role: schema.Assistant, Content: "x"}, nil)
				sw.Close()
			}()
			return sr, nil
		},
	}}
	l := newTestLoop(m, &countingTool{name: "echo"})

	out, err := l.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("建流不该失败: %v", err)
	}
	cancel()

	if _, rerr := drainAll(t, out); !errors.Is(rerr, context.Canceled) {
		t.Fatalf("取消后应以 context.Canceled 收尾，实际 %v", rerr)
	}
}

// 契约：中途断流 → 已投递文本保留，非 EOF 错误**透传**（中断标记靠它）。
func TestOwnLoop_MidStreamError_Propagates(t *testing.T) {
	boom := errors.New("stream broken")
	m := &fakeModel{steps: []streamStep{
		func(context.Context) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](2)
			go func() {
				sw.Send(&schema.Message{Role: schema.Assistant, Content: "半截"}, nil)
				sw.Send(nil, boom)
				sw.Close()
			}()
			return sr, nil
		},
	}}
	l := newTestLoop(m, &countingTool{name: "echo"})

	out, err := l.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("建流本身应成功: %v", err)
	}
	text, rerr := drainAll(t, out)
	if text != "半截" {
		t.Fatalf("已投递的文本应保留，实际 %q", text)
	}
	if !errors.Is(rerr, boom) {
		t.Fatalf("中途断流应把非 EOF 错误透传出来，实际 %v", rerr)
	}
}
