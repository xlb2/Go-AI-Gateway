# 关键词与向量混合检索

`Store.HybridSearch` 将关键词与向量候选按来源融合；`NewReadingToolsWithEmbedder` 提供独立的 `search_sources_hybrid` 工具。现有 `search_sources` 继续保持字面搜索契约。

## 排名与输出

1. `QueryTerms` 提取英文/代码标识符及连续汉字双字词，去重后最多32词。
2. 关键词侧使用 `SearchAllSources` 收齐各词候选，按命中不同词数排名；单词最多10页，超限报错。
3. 向量侧读取有限的分段候选，按首次出现的最好分数去重为来源。
4. 按 `score = Σ 1/(60+rank)` 进行 RRF 融合，同分按来源 ID 稳定排序。

RRF 使用名次，避免直接相加关键词计数与余弦分数。常数60固定，未进行敏感性调优。

每个 `HybridHit` 包含 source_id/version_id、两侧排名（从1开始，0表示未命中）、融合分，以及关键词 offset 或向量 chunk_start。结果不返回正文，须读取固定版本后形成证据。

topK默认8、最多50；query 最多500码点。向量候选预算有限，去重后可能不足 topK 个来源。向量侧未配置或无就绪索引时 `VectorIndexed=false`，返回关键词结果；向量请求本身失败则返回错误。

## 装配与质量边界

`NewReadingTools` 保留原3个资料工具；带 embedder 的构造函数追加混合工具。该工具目前未自动接入 CLI 或 Web chat，评测直接调用 Store。

RRF 可能奖励两路共识并挤掉仅一路找到的正确来源，因此混合不保证优于单路。关键词抽词逐词访问数据库，扫描上限是保护，不是性能结论。召回候选也不等于答案证据。

## 测试与排查

```bash
bash scripts/test-fast.sh -run KnowledgeQueryTerms
bash scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeHybrid
```

排查时先看 VectorIndexed，再看两侧 rank、QueryTerms 与向量索引状态；比较策略时使用同一语料与题集的 [三路评测](../rag/README.md)。假 embedder 的机制回归不代表真实语义质量。
