# 分段向量索引与召回（K-2 切片2）

状态：2026-10。本切片只做**向量索引 + 向量召回**（embedding + 暴力余弦）；关键词/向量**混合融合**是切片 3，尚未接入。选型理由见 [rag/vector-retrieval-design.md](../rag/vector-retrieval-design.md)。

## 数据契约（schema v5）

新增两表，不改原文与分段：

- `knowledge_chunk_embeddings`：`chunk_id`、`version_id`、`embedding_rule`、`dim`、`vector`。`vector` 是**归一化后的 float32 小端字节**（cosine == 点积）。唯一键 `(chunk_id, embedding_rule)`。
- `knowledge_embedding_indexes`：`version_id`(PK)、`embedding_rule`、`model`、`dim`、`chunk_count`、`updated_at`。

不变量：

- **一个版本同一时刻只保留一条规则的有效向量**。重建（`EmbedVersion`）先按 `version_id` 清旧规则、再插入新规则、再 upsert 索引记录，避免不同向量空间混存。
- **向量空间由 `embedding_rule` 锁定**。`rule = 模型:维度`（如 `text-embedding-v3:1024`）。查询用的规则必须与索引一致，否则 `indexed=false`，明确降级为关键词，绝不跨规则硬查。
- 位置沿用分段坐标：命中返回 `source_id/version_id/chunk_start/chunk_end`，**不返回正文**。要用原文必须走 `read_source`，遵循既有 evidence 机制——召回候选不等于证据。

## 接口

| 方法 | 行为 |
|---|---|
| `EmbedderFromEnv()` | 读 `EMBEDDING_BASE_URL/API_KEY/MODEL/DIM/KNOWLEDGE_EMBED_BATCH`。三个必要项全空 → `(nil,nil)` 未启用；半配置 → 报错 |
| `(*Store).EmbedVersion(ctx, owner, library, source, embedder)` | 为当前版本 chunks 建向量，幂等；失败不改原文 |
| `(*Store).EmbeddingStatus(...)` | `missing / ready / outdated`（outdated = 规则与传入口径不同） |
| `(*Store).VectorSearch(ctx, owner, library, embedder, query, topK)` | 暴力余弦召回，owner/library/当前版本/规则四重过滤 |

失败语义：`indexed=false` 表示**本库没有该模型（该规则）的就绪索引**，调用方必须降级，**不能**把它当成"资料库里没有相关内容"。向量候选超过 `maxVectorScan`（20000）直接报错，要求评测后再决定是否上 ANN，不静默拖慢。

**索引查找按"模型"而不是"模型:维度"**（见 `activeEmbeddingRules`）。原因：`ID()` 含维度，而维度可能尚未从响应学到（新进程为 `model:0`）；若用它查索引，新进程首次直接 `VectorSearch`/`EmbeddingStatus` 会匹配不到已建索引，**静默降级成关键词（假降级）**。按模型查则与维度学习时序无关。同模型改维度这种少见情况，由查询向量的逐行维度校验报错兜住（不会返回空腹结果）。

**用量口径**：embedding usage 优先取 `prompt_tokens`，缺失则回退 `total_tokens`（`text-embedding-v3` 实测可能只回 total），两者都缺才记未知（`-1`），不把"没返回"误记为 0。

## 决策问答（为什么）

**Q1 为什么远端 embedding API，而不是本地模型？**
候选：远端 API / 进程内纯 Go / ONNX+bge / Docker 推理 / Redis Stack。选远端：无本地依赖、无管理员权限需求、中文强、返回 usage 可计量。落选理由见设计文档第 2 节。代价是依赖外部服务与 key，故抽 `Embedder` 接口便于替换。

**Q2 为什么 MySQL 存 BLOB + Go 暴力余弦，而不是向量库/ANN？**
语料只有 K-0 的六份、几百个 chunk，暴力毫秒级；`KNOWLEDGE-K0.md §6` 与 `PRODUCT-OUTLINE.md §8` 都要求"先比较基线、不预先锁定向量库"。用现有 `im_mysql`，向量与分块同源、可事务、可解释。`maxVectorScan` 是诚实的天花板，规模上来再换实现，接口不动。

**Q3 为什么 embedding 不在导入事务里做？**
事务里做网络调用是坏味道（长事务、失败不可回滚外部调用），且会破坏"关闭模型仍可导入/阅读"这条产品前提。故 `EmbedVersion` 独立幂等，`KNOWLEDGE_EMBED_ON_IMPORT` 默认 off（本切片未接导入触发）。

**Q4 为什么一个版本只留一条规则向量？**
允许新旧向量共存会让"当前检索用哪套向量"变得不确定，也让分块重建/版本切换的失效判断复杂化。设计上换模型 = 重建 = 覆盖；旧规则的向量直接清掉，向量空间不混。

**Q5 为什么存归一化向量？**
cosine 退化成点积，查询路径少一次开方/逐元素除法；存储层统一口径，避免调用方各自归一化出错。

## 召回错了怎么排查

按顺序看：

1. `EmbeddingStatus` 是否 `ready` 且规则与查询一致？（`outdated`/`missing` 会直接 `indexed=false`）
2. `VectorSearch` 的 `Indexed` 与 `Chunks`（扫了多少分段）——`Chunks=0` 是"没索引"不是"没内容"。
3. 逐 `hit` 看 `Score` 与 `chunk_start/end`：分数普遍接近说明 embedding 区分度不足或分块过粗；分数高但答案错说明该分段不含答案（分块问题）。
4. 换查询措辞复现：若换个同义说法就命中，说明是 embedding 语义问题而非索引问题。
5. 真实质量只能靠 `rageval` 三路对照报告判定，不靠单次观感。

## 验证

```bash
# 纯单测（不联网、无需 DB）
scripts/test-fast.sh -run 'Embedder'
# MySQL 集成（假 embedder，不联网）
scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeVector
```

本切片新增：`test/knowledge/embedding_test.go`（4 项，无标签：配置边界、批量/归位/学维度、坏响应、usage 兜底）、`test/knowledge/vector_mysql_test.go`（2 项，`knowledgeintegration`：索引/召回/隔离/规则、**新进程直接查已建索引不假降级**）；同步 `mysql_test.go` 迁移版本断言 4 → 5。本地 `go vet -tags 'knowledgeintegration rageval contexteval' ./...` 通过；**测试待用户 WSL 运行**，未运行前不标通过。

## 未做 / 边界（如实）

- **混合融合（RRF）与检索工具未接**——切片 3。
- **未接导入自动嵌入**，评测/重建需显式调用 `EmbedVersion`。
- 真实 embedding 的**限流、长文本截断（>8192 token）、WSL 网络稳定性未验证**。
- 暴力检索在**真实规模下的延迟未测**；`maxVectorScan` 是保护不是性能结论。
- 假 embedder 只验证机制，**不代表语义质量**；`PASS ≠ 质量达标`。
- `EmbeddingStatus` 只按模型判过期；同模型改维度的"过期"由查询时的维度校验报错兜住，不单独建状态。
- 复审修复点：① 索引查找改为按模型（消除新进程 `model:0` 假降级，补 `TestKnowledgeVectorSearchFreshProcess`）；② usage 增加 `total_tokens` 兜底（补 `TestRemoteEmbedderUsageTotalFallback`）。
