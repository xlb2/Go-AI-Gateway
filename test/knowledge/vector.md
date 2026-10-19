# 分段向量索引与召回

向量是原文分段的可重建派生数据。远端 embedding 通过可替换 `Embedder` 接口提供，MySQL 保存向量，查询使用线性余弦扫描。技术取舍见 [检索设计](../rag/vector-retrieval-design.md)。

## 数据与生命周期

schema v5 新增：

- `knowledge_chunk_embeddings`：chunk/version、embedding_rule、维度与归一化 float32 小端 BLOB。
- `knowledge_embedding_indexes`：每版本的模型、规则、维度、片段数量与构建时间。

`EmbedVersion` 读取当前版本分段，在事务外调用 embedding；写入时重新校验归属及当前版本，事务替换该版本旧向量并更新索引记录。每版本同一时刻保留一套有效规则，不混合新旧模型向量。失败不修改原文；该接口可重复构建，但远端调用本身不提供去重保证。

导入不自动调用 embedding，显式构建前需已有文本分段。

## 接口与配置

| 接口 | 行为 |
|---|---|
| `EmbedderFromEnv()` | 必要配置全空表示未启用，半配置报错 |
| `Store.EmbedVersion` | 为来源当前版本构建向量 |
| `Store.EmbeddingStatus` | missing/ready/outdated；模型变化判 outdated |
| `Store.VectorSearch` | owner/library/当前版本/模型过滤后的分段级召回 |

必要环境变量为 `EMBEDDING_BASE_URL`、`EMBEDDING_API_KEY`、`EMBEDDING_MODEL`；可选 `EMBEDDING_DIM` 与 `KNOWLEDGE_EMBED_BATCH`，见 [配置模板](../../.env.example)。默认每批10条，端点能力由实际服务决定。

索引规则记录模型与维度，但查找先按模型，避免新进程维度尚未学习时错误降级；查询时逐行校验向量维度。同模型改维度不会单独标 outdated，可能在查询校验时报错。

没有该模型的就绪索引时返回 `Indexed=false`，不是“没有相关资料”。查询最多扫描20000候选，超出报错；topK默认8、最多50。候选包含版本、分段坐标与分数，不含正文，原文读取仍需 `read_source`。

usage 优先 prompt_tokens，缺失时使用 total_tokens；未知为-1，不能当零。归一化与点积只提供相似度计算，不代表答案支持度。

## 测试与限制

```bash
bash scripts/test-fast.sh -run 'Embedder'
bash scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeVector
```

测试使用假端点/假 embedder，覆盖批量归位、坏响应、用量、归属、索引规则与新进程查询。真实语义质量由 [三路评测](../rag/README.md) 单独观察。

当前没有 ANN、导入自动嵌入、后台索引队列或真实规模性能保证；限流策略与长文本端点限制需按服务配置另行处理。
