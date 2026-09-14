package metrics

import (
	"strings"
	"testing"
	"time"
)

// 桶是**累计**语义：le="0.1" 表示"观测值 ≤ 0.1 的次数"，
// 不是"落在 (0.05, 0.1] 这一档里的次数"。写成分档的话 P99 会算错，
// 而且是那种完全看不出来的错（数字都在，就是含义反了）。
func TestHistogramBucketsAreCumulative(t *testing.T) {
	values := []float64{0.005, 0.02, 0.03, 0.4, 2, 100}
	h := &Histogram{}
	for _, v := range values {
		h.Observe(v)
	}

	// 用**最朴素**的方式独立算期望：对每个桶数一遍"观测值 ≤ 这个上界"的个数。
	//
	// 为什么不写死数字：我第一版就是手算的，把 le=1 当成了"包含 2"（2 > 1，不该计入），
	// 测试失败 —— 错的是**期望**，不是实现。而"累计"与"分档"这两种语义的区别，
	// 恰恰是这里最容易搞混的地方，所以让测试按语义自己算一遍，比人肉对表可靠。
	// 如果实现用的是分档语义（值只归进最近的那一档），下面的逐桶比较就会不相等。
	for i, ub := range h.Buckets {
		var want int64
		for _, v := range values {
			if v <= ub {
				want++
			}
		}
		if h.Counts[i] != want {
			t.Errorf("le=%v 的累计计数 = %d，期望 %d（le 表示「≤」，不是「落在这档里」）",
				ub, h.Counts[i], want)
		}
	}

	if h.Count != int64(len(values)) {
		t.Errorf("总次数 = %d，期望 %d", h.Count, len(values))
	}
	if last := h.Counts[len(h.Counts)-1]; last > h.Count {
		t.Errorf("最大有限桶的累计（%d）不该超过总数（%d）", last, h.Count)
	}

	var wantSum float64
	for _, v := range values {
		wantSum += v
	}
	if h.Sum != wantSum {
		t.Errorf("总和 = %v，期望 %v", h.Sum, wantSum)
	}
}

func TestRenderHistogramFormat(t *testing.T) {
	r := New()
	r.ObserveHistogram("turn_latency_seconds", 0.3)
	r.ObserveHistogram("turn_latency_seconds", 2)
	r.ObserveDuration("tool_duration_seconds", 1500*time.Millisecond)

	out := r.Render()

	// Prometheus 直方图的三件套：_bucket{le=…} / _sum / _count
	for _, want := range []string{
		"# TYPE turn_latency_seconds histogram",
		`turn_latency_seconds_bucket{le="0.1"} 0`,
		`turn_latency_seconds_bucket{le="0.5"} 1`,
		`turn_latency_seconds_bucket{le="+Inf"} 2`,
		"turn_latency_seconds_count 2",
		"turn_latency_seconds_sum 2.3",
		"# TYPE tool_duration_seconds histogram",
		`tool_duration_seconds_bucket{le="5"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("导出里缺 %q\n实际输出：\n%s", want, out)
		}
	}
}

// 加直方图不能改坏计数器/求和/仪表的老行为（别人已经在读这些指标）。
func TestRenderKeepsLegacyKinds(t *testing.T) {
	r := New()
	r.Inc("turns_total")
	r.Add("prompt_tokens_total", 42)
	r.SetGauge("last_turn_latency_seconds", 0.3)

	out := r.Render()
	for _, want := range []string{
		"# TYPE turns_total counter\nturns_total 1",
		"# TYPE prompt_tokens_total counter\nprompt_tokens_total 42",
		"# TYPE last_turn_latency_seconds gauge\nlast_turn_latency_seconds_gauge 0.3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("导出里缺 %q\n实际输出：\n%s", want, out)
		}
	}
}

// 桶上界要渲染成短数字：1e-02 虽然合法，但人查指标时要多想一下，
// 而且和文档里写的 0.01 对不上。
func TestBucketBoundFormatting(t *testing.T) {
	r := New()
	r.ObserveHistogram("x_seconds", 0.001)

	out := r.Render()
	if strings.Contains(out, "e-02") || strings.Contains(out, "E-") {
		t.Errorf("桶上界出现了科学计数法：\n%s", out)
	}
	if !strings.Contains(out, `le="0.01"`) {
		t.Errorf("应有 le=\"0.01\" 这一行：\n%s", out)
	}
}
