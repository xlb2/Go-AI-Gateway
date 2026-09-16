package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
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

func (m *fakeModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	cp := make([]*schema.Message, len(input))
	copy(cp, input)
	m.inputs = append(m.inputs, cp)

	if m.i >= len(m.steps) {
		return nil, errors.New("fakeModel: 没有更多脚本流了")
	}
	f := m.steps[m.i]
	m.i++
	return f(context.Background())
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
