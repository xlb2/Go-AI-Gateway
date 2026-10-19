# 上下文基线与候选输入实验

以固定多轮任务观察回答约束、工具证据、输入估算和 SDK 用量。采集器复用 Harness/Runtime，不另写 Agent Loop；测试资料为合成内容。

## 场景

| 场景 | 检查内容 |
|---|---|
| `continuity-dev` | 用户偏好、最新决定与未完成事项 |
| `resume-dev` | 重建 Harness 后接续目标、约束与未知日期 |
| `evidence-dev` | 原文引用、事实与推断区分及换话题 |
| `reading-dev` | 长正文、分页标志与中部/尾部补读 |
| `continuity-holdout` / `resume-holdout` | 独立留出场景，须显式允许，不用于调参 |

每场景3轮。`cases.json` 的 expect 仅供评审，不发给模型；只有 `fixtures/` 的两份合成资料复制到临时工具根目录。采集不连接 MCP，使用新的随机评测身份，Redis 日志和身份预留键不会自动清理。

resume 场景重建同一进程内的 Harness 实例，不模拟真实时间间隔、进程冷启动或缓存清空。长文件不等于长期历史。

## 执行

普通回归使用假模型：

```bash
bash scripts/test-fast.sh -run ContextBaseline
```

真实采集在仓库根目录的 WSL/Linux 运行，使用现有模型与 Redis 配置，会产生真实调用用量：

```bash
bash scripts/context-baseline.sh --real continuity-dev
```

一次只运行一个场景，报告写入已忽略的 `results/`。每轮有超时，但没有精确金额上限；失败时停止后续轮并保留部分报告。

## 候选输入对照

默认只记录候选，不改变实际输入。显式设置预算并加 `--apply-shadow` 才在评测路径应用候选：

```bash
CONTEXT_BASELINE_SHADOW_BUDGET=4000 bash scripts/context-baseline.sh --real reading-dev
CONTEXT_BASELINE_SHADOW_BUDGET=4000 bash scripts/context-baseline.sh --real reading-dev --apply-shadow
```

4000是诊断示例，不是生产默认或推荐预算。候选无法安全应用时回退全量，规则见 [上下文候选契约](../context-shadow.md)。

## 报告口径

| 字段 | 含义 |
|---|---|
| `corpus_sha256` / `source_sha256` | 题目/资料与代码/依赖版本指纹 |
| `logical_main_inputs` | 原始逻辑输入的组成估算 |
| `sdk_attempts` | 主/子/摘要 SDK 尝试及已知 usage，不与逻辑步骤重复相加 |
| `tool_events` | 主循环工具名称、参数哈希、范围、分页/裁剪及耗时元数据，不保存原始参数和结果正文 |
| `shadow_input_applications` | 是否应用候选、回退原因、消息数量与原/候选/实际适配器输入估算 |
| `first_text_ms` / `elapsed_ms` | 最终回答首个文本分片与本轮 Harness 调用耗时，不是冷启动性能 |
| `quality` | 默认 `unreviewed`；采集成功不自动改为质量通过 |

未知 usage、缓存与费用保持未知，不填零。尝试统计中的逻辑输入估算仍可能是包装器外的原输入；分析实际应用时需同时核对应用记录及服务商 usage。报告含模型回答，分享前应检查内容。

## 评审原则与已知结论

按固定 expect 逐项核对事实、最新约束、引用和回答长度；无法判断不能算通过。比较前核对语料、源码、模型与配置，报告全部重复运行，不能只选择最有利结果。

已有小样本试验中，8000预算曾出现候选全量保留；4000预算可发生裁剪，但回答长度遵守情况不稳定。不同回答会改变后续历史，单次 provider 总量差异不能直接归因于裁剪。当前不将该策略设为默认，也不宣称已经实现稳定节省。

留出场景只能用于预定最终检查；查看结果后继续改策略的题目应降级为开发集。测试与评测方法见 [测试指南](../README.md)。
