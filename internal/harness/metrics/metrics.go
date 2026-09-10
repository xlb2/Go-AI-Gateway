// Package metrics 可观测性器官：轻量指标注册表（计数器/求和/仪表）+ Prometheus 文本导出。
//
// 定位：harness 解剖图里的"可观测性"（HARNESS-STUDY M11 / Roadmap ⑦）。
// 零第三方依赖：指标就三类——计数器(Inc)、求和(Add，用于累加耗时)、仪表(SetGauge)；
// /metrics 端点直接吐 Prometheus 文本格式，抓取方（Prometheus/Grafana）无需改动。
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry 线程安全的指标注册表。
type Registry struct {
	mu       sync.RWMutex
	counters map[string]int64
	sums     map[string]float64
	gauges   map[string]float64
}

// New 新建一个空注册表。
func New() *Registry {
	return &Registry{
		counters: map[string]int64{},
		sums:     map[string]float64{},
		gauges:   map[string]float64{},
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

// Add 求和累加（如 turn 耗时的秒数总和）。
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

// Render 渲染成 Prometheus 文本格式（Gauge 带 _gauge 后缀区分；计数/求和直接输出）。
// 例：
//
//	# TYPE turns_total counter
//	turns_total 42
//	# TYPE turn_latency_seconds_total counter
//	turn_latency_seconds_total 12.5
//	# TYPE last_turn_latency_seconds gauge
//	last_turn_latency_seconds 0.3
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
	return b.String()
}
