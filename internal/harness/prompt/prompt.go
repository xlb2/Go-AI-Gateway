// Package prompt 上下文/Prompt 组装器官（对应 HARNESS-STUDY M2）。
//
// 核心认知：**系统提示不是一坨写死的字符串**，而是"各片段各带一个 order 数字、
// 最后由组装器排序拼接"出来的。好处有三个：
//
//  1. 各写各的、互不覆盖——加一段工具指引不用去改人设那一段；
//  2. 顺序显式——谁在前谁在后是数据（order），不是字符串里的位置；
//  3. 可编程——工具清单这类"随时间变化"的片段能在渲染时注入，不用改代码。
//
// 对齐 dsh `packages/core/system-prompt/src/index.ts`：
// PromptSection{name, order, text} + 按 order 排序 + `{{var}}` 插值。
// dsh 的序号约定照抄：-100 身份 / 0 人设 / 100-199 工具指引 / 200+ 动态上下文。
package prompt

import (
	"sort"
	"strings"
)

// 序号约定（对齐 dsh：越小越靠前）。
const (
	// OrderIdentity 身份（最先说"你是谁"）
	OrderIdentity = -100
	// OrderPersona 人设（性格/语气/原则）
	OrderPersona = 0
	// OrderTools 工具指引（100-199 段留给工具）
	OrderTools = 100
	// OrderContext 动态上下文（时间、环境等运行时注入的）
	OrderContext = 200
)

// Section 系统提示里的一个片段。
type Section struct {
	Name  string
	Order int
	Text  string
}

// Builder 片段组装器。零值不可用，用 New() 构造。
type Builder struct {
	sections map[string]Section
}

// New 构造一个空的组装器。
func New() *Builder {
	return &Builder{sections: map[string]Section{}}
}

// Set 写入/覆盖一个片段；同名会整体替换（工具重新注册后可以直接顶掉旧指引）。
// 返回自身，方便链式调用。
func (b *Builder) Set(name string, order int, text string) *Builder {
	b.sections[name] = Section{Name: name, Order: order, Text: text}
	return b
}

// Remove 摘掉一个片段（撤销注册的场景）。
func (b *Builder) Remove(name string) *Builder {
	delete(b.sections, name)
	return b
}

// Has 该片段是否存在。
func (b *Builder) Has(name string) bool {
	_, ok := b.sections[name]
	return ok
}

// Sections 返回按 order 升序排好的片段（order 相同按 name 排，保证渲染结果稳定可复现——
// 不稳定的话每次重启系统提示都在变，等于给模型埋了个随机变量）。
func (b *Builder) Sections() []Section {
	out := make([]Section, 0, len(b.sections))
	for _, s := range b.sections {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Render 渲染成最终的系统提示：排序 → 插值 → 丢掉空片段 → 用空行拼接。
func (b *Builder) Render(vars map[string]string) string {
	parts := make([]string, 0, len(b.sections))
	for _, s := range b.Sections() {
		text := interpolate(s.Text, vars)
		text = strings.TrimSpace(text)
		if text == "" {
			continue // 空片段直接不参与拼接，不留多余空行
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}

// interpolate 把 {{key}} 替换成 vars[key]；没有对应键时原样保留
// （保留是为了让"忘了传变量"这件事在提示词里肉眼可见，而不是悄悄变成一个空串）。
func interpolate(text string, vars map[string]string) string {
	if len(vars) == 0 || !strings.Contains(text, "{{") {
		return text
	}
	for k, v := range vars {
		text = strings.ReplaceAll(text, "{{"+k+"}}", v)
	}
	return text
}
