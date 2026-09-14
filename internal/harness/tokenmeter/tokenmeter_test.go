package tokenmeter

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestEstimateByCharClass(t *testing.T) {
	if got := Estimate(""); got != 0 {
		t.Fatalf("空串应为 0，实际 %d", got)
	}

	// 英文：约 4 字符 1 token
	en := Estimate("this is an english sentence with about forty chars")
	if en < 8 || en > 14 {
		t.Fatalf("英文估算离谱: %d", en)
	}

	// 中文比英文"贵"：同样字符数，token 更多
	zh := Estimate(strings.Repeat("中文", 20)) // 40 个汉字
	if zh <= 0 || zh < 20 {
		t.Fatalf("中文估算偏低: %d", zh)
	}
	if zh <= Estimate(strings.Repeat("ab", 20)) {
		t.Fatal("同样字符数下，中文应比英文更贵（token 更多）")
	}
}

func TestEstimateCountsToolArguments(t *testing.T) {
	// 工具调用的参数和正文一样会发给模型，而且往往更大 —— 不算它就是系统性低估
	plain := []*schema.Message{schema.UserMessage("查一下")}
	withTool := []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{
		ID: "c1", Type: "function",
		Function: schema.FunctionCall{Name: "search", Arguments: strings.Repeat(`{"q":"很长的参数"}`, 30)},
	}})}

	if EstimateMessages(withTool) <= EstimateMessages(plain) {
		t.Fatal("工具调用参数没被计入估算")
	}
}

func TestBudgetSanitizesSelfContradiction(t *testing.T) {
	// 保留比例 ≥ 触发比例 = 压完立刻又该压（每轮都压）。必须被修正。
	b := Budget{ContextWindow: 1000, ThresholdRatio: 0.5, RetainRatio: 0.9}.Sanitized()
	if b.RetainRatio >= b.ThresholdRatio {
		t.Fatalf("保留比例(%v) 必须明显小于触发比例(%v)", b.RetainRatio, b.ThresholdRatio)
	}
	if b.TriggerTokens() <= b.RetainTokens() {
		t.Fatalf("触发线(%d) 必须大于保留线(%d)", b.TriggerTokens(), b.RetainTokens())
	}

	// 非法值回落到默认
	d := Budget{ContextWindow: -1, ThresholdRatio: 5, RetainRatio: 0}.Sanitized()
	if d.ContextWindow != defaultContextWindow || d.ThresholdRatio != defaultThresholdRatio || d.RetainRatio != defaultRetainRatio {
		t.Fatalf("非法值没回落到默认: %+v", d)
	}
}

func TestDefaultBudgetRatios(t *testing.T) {
	b := DefaultBudget()
	if b.TriggerTokens() != 102400 {
		t.Fatalf("128k × 0.8 应为 102400，实际 %d", b.TriggerTokens())
	}
	if b.RetainTokens() != 20480 {
		t.Fatalf("128k × 0.16 应为 20480，实际 %d", b.RetainTokens())
	}
}

func TestCalibrationMovesAndClamps(t *testing.T) {
	ResetCalibration()
	defer ResetCalibration()

	if r, n := Calibration(); r != 1.0 || n != 0 {
		t.Fatalf("初始应为 1.0/0，实际 %v/%d", r, n)
	}

	// 实际是估算的 2 倍 → 系数往上走
	for i := 0; i < 10; i++ {
		Observe(100, 200)
	}
	r, n := Calibration()
	if r <= 1.2 {
		t.Fatalf("持续低估后系数应上升，实际 %v", r)
	}
	if n != 10 {
		t.Fatalf("样本数应为 10，实际 %d", n)
	}

	// 荒谬的样本（实际是估算的 1 万倍）必须被夹住，不能把后续判断全带偏
	for i := 0; i < 20; i++ {
		Observe(1, 10000)
	}
	if r, _ := Calibration(); r > calMaxRatio {
		t.Fatalf("系数应被夹在 %.1f 以内，实际 %v", calMaxRatio, r)
	}

	// 非法输入直接忽略
	before, nBefore := Calibration()
	Observe(0, 100)
	Observe(100, 0)
	after, nAfter := Calibration()
	if before != after || nBefore != nAfter {
		t.Fatal("非法样本不该影响校准")
	}
}

func TestTrimToBudgetKeepsToolPairing(t *testing.T) {
	msgs := []*schema.Message{
		schema.UserMessage(strings.Repeat("很长的历史", 100)),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "c1", Type: "function",
			Function: schema.FunctionCall{Name: "t", Arguments: "{}"}}}),
		schema.ToolMessage("工具结果", "c1"),
		schema.UserMessage("最新一问"),
	}

	// 预算小到必须从头丢
	got := TrimToBudget(msgs, 20)
	if len(got) == 0 {
		t.Fatal("不该裁成空")
	}
	// 裁剪后的第一条不能是 Tool 消息：它必须紧跟在带 tool_calls 的 assistant 后面
	if got[0].Role == schema.Tool {
		t.Fatal("裁剪后以 Tool 消息开头，模型侧协议会直接报错（配对被从中间切断）")
	}

	// 预算足够大时不该动它
	same := TrimToBudget(msgs, 100000)
	if len(same) != len(msgs) {
		t.Fatalf("预算充足时不该裁剪：%d -> %d", len(msgs), len(same))
	}
}
