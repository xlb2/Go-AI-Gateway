// Package metrics 可观测性器官：轻量指标注册表（计数器/求和/仪表/直方图）+ Prometheus 文本导出。
//
// 定位：harness 解剖图里的"可观测性"（HARNESS-STUDY M11 / Roadmap ⑦）。
// 零第三方依赖：/metrics 端点直接吐 Prometheus 文本格式，抓取方（Prometheus/Grafana）无需改动。
package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBuckets 默认的耗时桶（秒），升序。
//
// 覆盖"一次工具调用 / 一轮对话"的常见量级：10ms → 30s，再往上就是"出问题了"那一档，
// 由 +Inf 收尾。桶是**累计**语义（Prometheus 的 le = less-or-equal）：
// Counts[i] 表示"观测值 ≤ Buckets[i]"的总次数，不是落在这一档里的次数。
var DefaultBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 30}

// Histogram 固定桶直方图。
//
// 为什么必须有它：只有"求和"的话能算出平均耗时，但**看不到分布** ——
// 平均 200ms 到底是"每次 200ms"还是"99 次 10ms + 1 次 19 秒"？
// 前者没事，后者是事故。P50 / P99 只能从分布里来。
type Histogram struct {
	Buckets []float64 // 上界（升序，不含 +Inf）
	Counts  []int64   // 累计计数：Counts[i] = 观测值 ≤ Buckets[i] 的次数
	Sum     float64
	Count   int64 // 观测总次数（也就是 +Inf 桶的值）
}

// Observe 记录一个观测值。
func (h *Histogram) Observe(v float64) {
	if h.Buckets == nil {
		h.Buckets = append([]float64(nil), DefaultBuckets...)
	}
	if h.Counts == nil {
		h.Counts = make([]int64, len(h.Buckets))
	}
	h.Sum += v
	h.Count++
	for i, ub := range h.Buckets {
		if v <= ub {
			h.Counts[i]++
		}
	}
}

// Registry 线程安全的指标注册表。
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]int64
	sums       map[string]float64
	gauges     map[string]float64
	histograms map[string]*Histogram
}

// New 新建一个空注册表。
func New() *Registry {
	return &Registry{
		counters:   map[string]int64{},
		sums:       map[string]float64{},
		gauges:     map[string]float64{},
		histograms: map[string]*Histogram{},
	}
}

// Default 全局默认指标注册表（main 启动后即可用，/metrics 端点读它）。
var Default = New()

// Inc 计数器 +1（如 turns_total）。
func (r *Registry) Inc(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name]++
}

// Add 求和累加（如 token 总数）。
func (r *Registry) Add(name string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sums[name] += delta
}

// SetGauge 设置仪表值（当前瞬时值，如最近一次 turn 耗时）。
func (r *Registry) SetGauge(name string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[name] = v
}

// ObserveHistogram 把一个观测值记进直方图（耗时请传**秒**）。
// 名字第一次出现时用 DefaultBuckets 建桶。
func (r *Registry) ObserveHistogram(name string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.histograms[name]
	if h == nil {
		h = &Histogram{Buckets: append([]float64(nil), DefaultBuckets...)}
		r.histograms[name] = h
	}
	h.Observe(v)
}

// ObserveDuration 同 ObserveHistogram，直接收 time.Duration。
// 有它就不用每个调用点都写 .Seconds() —— 少一处手误的机会。
func (r *Registry) ObserveDuration(name string, d time.Duration) {
	r.ObserveHistogram(name, d.Seconds())
}

// Render 渲染成 Prometheus 文本格式。
//
// 三类指标的输出约定：
//   - 计数器 → `# TYPE <name> counter` + 值
//   - 求和   → 同上（我们的"求和"本身就是单调递增的计数器）
//   - 仪表   → 名字带 `_gauge` 后缀，导出时去掉后缀并标 gauge
//   - 直方图 → `_bucket{le="..."}` / `_sum` / `_count` 三件套（Prometheus 约定）
func (r *Registry) Render() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder

	names := make([]string, 0, len(r.counters)+len(r.sums)+len(r.gauges))
	for k := range r.counters {
		names = append(names, k)
	}
	for k := range r.sums {
		names = append(names, k)
	}
	for k := range r.gauges {
		names = append(names, k+"_gauge")
	}
	sort.Strings(names)
	for _, n := range names {
		base := n
		isGauge := strings.HasSuffix(n, "_gauge")
		if isGauge {
			base = strings.TrimSuffix(n, "_gauge")
			fmt.Fprintf(&b, "# TYPE %s gauge\n%s %v\n", base, n, r.gauges[base])
		} else if v, ok := r.counters[base]; ok {
			fmt.Fprintf(&b, "# TYPE %s counter\n%s %d\n", base, n, v)
		} else {
			fmt.Fprintf(&b, "# TYPE %s counter\n%s %v\n", base, n, r.sums[base])
		}
	}

	// 直方图单独一段：每个桶一行 + sum + count。
	hnames := make([]string, 0, len(r.histograms))
	for k := range r.histograms {
		hnames = append(hnames, k)
	}
	sort.Strings(hnames)
	for _, n := range hnames {
		h := r.histograms[n]
		fmt.Fprintf(&b, "# TYPE %s histogram\n", n)
		for i, ub := range h.Buckets {
			fmt.Fprintf(&b, "%s_bucket{le=%q} %d\n", n, formatBound(ub), h.Counts[i])
		}
		fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n", n, h.Count)
		fmt.Fprintf(&b, "%s_sum %v\n", n, h.Sum)
		fmt.Fprintf(&b, "%s_count %d\n", n, h.Count)
	}
	return b.String()
}

// formatBound 把桶上界渲染成 Prometheus 里那样的短数字（0.01 / 0.5 / 1 / 30，而不是 1e-02）。
func formatBound(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
