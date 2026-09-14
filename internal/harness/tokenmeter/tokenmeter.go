// Package tokenmeter token 计量：估算 + 用真实用量校准 + 上下文预算。
//
// 定位：harness 解剖图里的"上下文预算"（HARNESS-STUDY M7 的前置）。
// 为什么必须有它：窗口按"消息条数"算是错的 —— 20 条"收到"和 20 条千字长文差两个数量级，
// 但条数一样。按条数判定的结果是：要么该压的时候不压（撑爆上下文窗口），
// 要么不该压的时候把长内容压掉（丢信息）。
//
// 两层设计，刻意分开：
//
//	Estimate / EstimateMessages  纯函数，只按字符类型估算，无全局状态（便于测试）
//	Used                         在纯估算之上乘校准系数 —— 这才是喂给预算判断的值
//
// 校准系数来自真实返回的 usage：估算永远只是估算，模型自己报的数才是真的。
// 对齐 dsh 的 `token-meter`（回放式计价：优先复用真实 usage，否则启发式估算）。
package tokenmeter

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// 估算系数（按字符类型分档，比"总字符数 / 4"准得多）。
const (
	// asciiTokensPerChar 英文/数字/半角符号：约 4 个字符 1 个 token
	asciiTokensPerChar = 1.0 / 4.0
	// wideTokensPerChar 中日韩字符：约 1.4 个字符 1 个 token
	wideTokensPerChar = 0.7
	// otherTokensPerChar 其它（emoji、全角符号等）：保守按 1 字符 1 token
	otherTokensPerChar = 1.0
	// perMessageOverhead 每条消息的结构开销（role 标记、分隔符等）
	perMessageOverhead = 4
)

// isWide 判断是否 CJK 宽字符（中日韩文字与标点、全角符号）。
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11FF: // 谚文字母
		return true
	case r >= 0x2E80 && r <= 0x303F: // 中日韩部首 + 中文标点
		return true
	case r >= 0x3040 && r <= 0x30FF: // 平假名 / 片假名
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // 基本汉字
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // 谚文音节
		return true
	case r >= 0xF900 && r <= 0xFAFF: // 兼容汉字
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // 全角 ASCII / 半角片假名
		return true
	case r >= 0x20000 && r <= 0x2FA1F: // 扩展 B 及以后
		return true
	}
	return false
}

// Estimate 估算一段文本的 token 数（纯函数，不含校准）。
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	var ascii, wide, other int
	for _, r := range text {
		switch {
		case r < 0x80:
			ascii++
		case isWide(r):
			wide++
		default:
			other++
		}
	}
	tokens := float64(ascii)*asciiTokensPerChar +
		float64(wide)*wideTokensPerChar +
		float64(other)*otherTokensPerChar
	return int(math.Ceil(tokens))
}

// EstimateMessages 估算一组消息的 token 数（纯函数，不含校准）。
// 工具调用的**参数**也要算 —— 它们和正文一样会发给模型，而且往往更大。
func EstimateMessages(msgs []*schema.Message) int {
	total := 0
	for _, m := range msgs {
		if m == nil {
			continue
		}
		total += perMessageOverhead
		total += Estimate(m.Content)
		total += Estimate(m.Name) + Estimate(m.ToolName) + Estimate(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			total += Estimate(tc.Function.Name) + Estimate(tc.Function.Arguments)
		}
	}
	return total
}

// ---- 校准：估算永远只是估算，模型自己报的数才是真的 ----

var (
	calMu      sync.RWMutex
	calRatio   = 1.0
	calSamples int
)

// 系数夹在合理区间内：一次异常 usage 不该把后续所有判断带偏。
const (
	calMinRatio = 0.4
	calMaxRatio = 2.5
)

// Observe 用一次真实的 usage 校准估算系数。
// estimated 是发请求前的估算值，actual 是模型返回的 prompt_tokens。
// 采用指数滑动平均（EWMA）：单次样本不推翻历史，但持续偏差会稳步纠正。
func Observe(estimated, actual int) {
	if estimated <= 0 || actual <= 0 {
		return
	}
	r := float64(actual) / float64(estimated)
	calMu.Lock()
	defer calMu.Unlock()
	if calSamples == 0 {
		calRatio = r
	} else {
		calRatio = calRatio*0.7 + r*0.3
	}
	calSamples++
	if calRatio < calMinRatio {
		calRatio = calMinRatio
	}
	if calRatio > calMaxRatio {
		calRatio = calMaxRatio
	}
}

// Calibration 返回当前校准系数与样本数（排查用）。
func Calibration() (ratio float64, samples int) {
	calMu.RLock()
	defer calMu.RUnlock()
	return calRatio, calSamples
}

// ResetCalibration 复位校准（测试用；生产不需要调用）。
func ResetCalibration() {
	calMu.Lock()
	defer calMu.Unlock()
	calRatio = 1.0
	calSamples = 0
}

// Used 估算一组消息**实际会占多少 token**：纯估算 × 校准系数。
// 预算判断一律用它，不要直接用 EstimateMessages —— 那个只是"未经校对的第一版"。
func Used(msgs []*schema.Message) int {
	calMu.RLock()
	ratio := calRatio
	calMu.RUnlock()
	return int(math.Ceil(float64(EstimateMessages(msgs)) * ratio))
}

// ---- 预算 ----

// Budget 上下文预算：什么时候该压缩、压缩后保留多少。
//
// 对齐 dsh `compaction-basic/src/config.ts` 的两个比例：
// thresholdRatio 决定"用到窗口多少就该压了"，retainRatio 决定"压完留多长的尾巴"。
type Budget struct {
	// ContextWindow 模型的上下文窗口（token）
	ContextWindow int
	// ThresholdRatio 用量占窗口多少时触发压缩（默认 0.8）
	ThresholdRatio float64
	// RetainRatio 压缩后保留尾部多少（默认 0.16）
	RetainRatio float64
}

// 默认值：128k 窗口 / 用到 80% 触发 / 压完保留 16%（约 20k）。
const (
	defaultContextWindow  = 128000
	defaultThresholdRatio = 0.8
	defaultRetainRatio    = 0.16
)

// DefaultBudget 返回默认预算。
func DefaultBudget() Budget {
	return Budget{
		ContextWindow:  defaultContextWindow,
		ThresholdRatio: defaultThresholdRatio,
		RetainRatio:    defaultRetainRatio,
	}
}

// BudgetFromEnv 从环境变量读预算（不合法/没配就用默认值）。
// 环境变量：MODEL_CONTEXT_WINDOW / COMPACT_THRESHOLD_RATIO / COMPACT_RETAIN_RATIO
func BudgetFromEnv() Budget {
	b := DefaultBudget()
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MODEL_CONTEXT_WINDOW"))); err == nil && v > 0 {
		b.ContextWindow = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("COMPACT_THRESHOLD_RATIO")), 64); err == nil && v > 0 && v <= 1 {
		b.ThresholdRatio = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("COMPACT_RETAIN_RATIO")), 64); err == nil && v > 0 && v <= 1 {
		b.RetainRatio = v
	}
	return b.Sanitized()
}

// Sanitized 修掉非法值，保证比例关系合理（否则可能出现"保留得比触发线还多"这种自相矛盾）。
func (b Budget) Sanitized() Budget {
	if b.ContextWindow <= 0 {
		b.ContextWindow = defaultContextWindow
	}
	if b.ThresholdRatio <= 0 || b.ThresholdRatio > 1 {
		b.ThresholdRatio = defaultThresholdRatio
	}
	if b.RetainRatio <= 0 || b.RetainRatio > 1 {
		b.RetainRatio = defaultRetainRatio
	}
	if b.RetainRatio >= b.ThresholdRatio {
		// 保留尾巴必须明显小于触发线，否则压缩完立刻又该压了 —— 变成每轮都压
		b.RetainRatio = b.ThresholdRatio / 4
	}
	return b
}

// TriggerTokens 用量超过它就触发压缩。
func (b Budget) TriggerTokens() int {
	return int(float64(b.Sanitized().ContextWindow) * b.Sanitized().ThresholdRatio)
}

// RetainTokens 压缩后尾巴保留的 token 预算。
func (b Budget) RetainTokens() int {
	return int(float64(b.Sanitized().ContextWindow) * b.Sanitized().RetainRatio)
}

// String 便于日志里一眼看出当前预算。
func (b Budget) String() string {
	return "window=" + strconv.Itoa(b.ContextWindow) +
		" trigger=" + strconv.Itoa(b.TriggerTokens()) +
		" retain=" + strconv.Itoa(b.RetainTokens())
}

// TrimToBudget 从**头部**丢消息，直到估算值落进 maxTokens 之内。
//
// 硬保护：压缩是 best-effort（摘要失败就跳过），所以投影之后还要有一道兜底，
// 否则一次压缩失败就可能把超长上下文整个塞给模型。
// 保持工具消息配对：裁剪后的第一条不能是 Tool 消息 ——
// 它必须紧跟在带 tool_calls 的 assistant 后面，否则模型侧协议直接报错。
func TrimToBudget(msgs []*schema.Message, maxTokens int) []*schema.Message {
	if maxTokens <= 0 || len(msgs) == 0 {
		return msgs
	}
	start := 0
	for start < len(msgs) && Used(msgs[start:]) > maxTokens {
		start++
	}
	// 丢到某条 Tool 消息开头了 → 继续丢，直到不是 Tool（配对不能从中间切）
	for start < len(msgs) && msgs[start].Role == schema.Tool {
		start++
	}
	if start == 0 {
		return msgs
	}
	out := make([]*schema.Message, len(msgs)-start)
	copy(out, msgs[start:])
	return out
}
