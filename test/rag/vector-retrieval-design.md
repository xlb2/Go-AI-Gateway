# K-2/K-3 向量检索与评测：选型与设计（切片 1，未动代码）

状态：2026-10。本文件是“重启 RAG 向量线”的第一切片交付物——**只做选型与设计，不改生产代码**。
读完本文件再决定是否进入切片 2（向量索引与召回）。所有"为什么"都给候选方案与落选理由，
所有未验证项都在第 8 节明说。工程状态以 HARNESS-TODO.md 为准，本文件不替代它。

---

## 0. 目标与硬边界

目标：
1. 给 `internal/knowledge` 增加 embedding + 向量召回。
2. 向量 + 现有关键词的**混合融合**，引用必须能定位到 资料 / 版本 / 片段坐标（沿用现有 evidence 来源片段机制）。
3. 复用 `test/rag` 题集与脚本，产出"关键词基线 vs 向量 vs 混合"的 recall@5 对照报告；14 道 dev 迭代，6 道 holdout 只跑最终验收。

硬边界（用户明确）：
- 不引入**需要管理员权限**的组件；优先进程内或现有 docker 基础设施（im_mysql 宿主 3307 / im_redis）。
- embedding 走远端 API 可以，**key 走环境变量，不碰用户的 `.env`**（只改 `.env.example` 模板）。
- 不碰 CM-0~CM-5 实验线、Web 页面功能扩展、CLI 主线（`internal/cli`、`cmd/agent`）。
- 不主动提交、不 push；不 `go run`；本地只 `go vet`；新测试放 `test/` 下。

---

## 1. 现状资产盘点（先接住，别重造）

| 资产 | 位置 | 现状 |
|---|---|---|
| K-0 设计 | `KNOWLEDGE-K0.md` | 六份真实资料 + 20 题（14 dev / 6 holdout），明确"选型留到 K-2 基线实验" |
| 评测骨架 | `test/rag/{cases.json,manifest.json,evaluator.go}` | 确定性双字/标识符查询、source-level recall@5、SHA256 语料校验、报告 JSON |
| 评测脚本 | `scripts/eval-rag.sh` | 独立 `knowledgeintegration,rageval` 标签，报告独占创建不覆盖 |
| 关键词检索 | `internal/knowledge/search.go` | MySQL 精确扫描（`LOCATE`），当前版本，最多 10 份/页，返回字节→Unicode 偏移 |
| 分块索引 | `internal/knowledge/chunks.go` | `rune-window-800-overlap-100-v1`，事务写入，状态 ready/missing/outdated |
| 资料库/版本 | `internal/knowledge/store.go` | library/source/version，owner 隔离，SHA256 去重 |
| 迁移 | `internal/knowledge/migrate.go` | `GET_LOCK` 串行 + 版本表，当前 schema v4 |
| 来源片段证据 | `test/knowledge/evidence.md` | 来源由**已提交的 tool 调用/结果投影**，不让模型自报引用冒充证据 |
| 基线报告 | `test/rag/results/dev-20260921T092439Z-38323.json` | 关键词基线：12/12 来源召回、recall@5=1.000（6 份资料取前 5，区分度低） |

**结论**：向量线不是从零开始——题集、评测脚本、分块、版本、来源证据都在。只需补"embedding + 向量存储 + 融合"三块。

---

## 2. 决策一：embedding 方案

| 候选 | 说明 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. 远端 OpenAI 兼容 `/embeddings` API** | 复用现有模型服务商（火山方舟等），HTTP 调 embedding 模型 | 无本地依赖、无需管理员、中文能力强、与现有 HTTP 模型栈同构 | 依赖外部服务与 key；产生少量 token 费用；需确认 key 支持 embedding 模型 | **推荐** |
| B. 进程内纯 Go 模型 | 无现成高质量中文 embedding Go 库 | 无网络 | 没有可用实现；自造特征哈希质量差，失去语义意义 | 落选 |
| C. ONNX Runtime + bge-small-zh | CGO 绑 onnxruntime + 下载模型 | 离线、免费 | CGO/运行库在 Windows+WSL 双平台麻烦，可能触发 EDR；模型体积大 | 落选（后续候选） |
| D. Docker `text-embeddings-inference` / Ollama | 独立推理服务 | 离线、不占 key | 新组件（用户要优先进程内/现有 docker）；镜像+模型下载重 | 落选（后续候选） |
| E. Redis Stack 向量集 | 用 `im_redis` | 复用容器 | 现有镜像非 Redis Stack，未必支持 `VADD/VSIM`；引入模块依赖 | 落选 |

**选 A**，但抽一层接口：

```go
// knowledge.Embedder：可替换的 embedding 端口
type Embedder interface {
    ID() string                                   // 模型+维度指纹，用于向量空间锁定
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

- `RemoteEmbedder`：HTTP POST `{model, input:[...]}` → `{data:[{embedding:[...]}], usage?}`，OpenAI Chat-Completions 兼容风格。
- 新增 env（只写进 `.env.example`，**不动用户 .env**）：
  `EMBEDDING_BASE_URL` / `EMBEDDING_API_KEY` / `EMBEDDING_MODEL` / `EMBEDDING_DIM` / `KNOWLEDGE_EMBED_BATCH`。
- **前置闸门已通过（2026-10 实测）**：端点 `https://maas.qianwenaiapi.com/compatible-mode/v1`，模型 **`text-embedding-v3`**，维度 **1024**，`POST /embeddings` 返回 `data[].embedding` 与 `usage.prompt_tokens/total_tokens`。
  - `text-embedding-async-v2` 不存在（404）；`text-embedding-v2` 可用（1536 维）作为备选。
  - 批量：单请求 **≤ 10 条**（10 条 OK；25/30 条返回 400）。默认批量取 10。
  - key 只放环境变量，**不入库、不写入文档、不提交**。

**为什么单独一层接口**：换模型/换本地实现时，只改 `Embedder` 实现和一处装配，callers 不动（对齐本项目"换实现不动主体"）。同时 `ID()` 让"不同模型向量不混查"成为**存储层可强制的规则**，而不是靠自觉。

**未验证**：远端 embedding 端点的可用性、维度、是否返回 usage、限流与批量上限。全部待切片 2 用真实 key 验证。

---

## 3. 决策二：向量存储与检索

| 候选 | 说明 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. MySQL 存向量 + Go 暴力余弦** | 新表存 `float32` BLOB，查询时拉候选暴力算 cosine | 复用 im_mysql；与分块事务同源；无新服务；可解释 | O(N) 扫描，随语料线性增长 | **推荐（K-2 基线）** |
| B. Redis 存量 + 暴力 | 复用 im_redis | 复用容器 | 向量与分块跨存储，一致性更难；收益不明显 | 落选 |
| C. 进程内内存 ANN（HNSW） | 启动时从 MySQL 载入建索引 | 查询快 | 增依赖；多进程不一致；语料小无必要 | 落选（证据后再上） |
| D. 外部向量库（Qdrant/Milvus/pgvector） | 独立服务 | 可扩展 | 新容器/新服务，overhead；与"不预先锁定向量库"冲突 | 落选（后置） |

**选 A**：新增表（schema v5）：

```
knowledge_chunk_embeddings (
  id BIGINT PK, chunk_id BIGINT NOT NULL, version_id BIGINT NOT NULL,
  embedding_rule VARCHAR(100) NOT NULL,     -- model+dim+pooling 指纹
  dim INT NOT NULL, vector BLOB NOT NULL,   -- float32 小端，dim*4 字节
  UNIQUE KEY (chunk_id, embedding_rule),
  KEY (version_id)
)
knowledge_embedding_indexes (
  version_id BIGINT PK, embedding_rule VARCHAR(100), model VARCHAR(100),
  dim INT, chunk_count INT, updated_at DATETIME(3)
)
```

- `Store.EmbedVersion(ctx, owner, library, source)`：对当前版本的 chunks 计算向量并写入，规则/维度与查询侧核对。
- `Store.VectorSearch(ctx, owner, library, queryVec, topK)`：按 owner/library/当前版本/embedding_rule 过滤后暴力 cosine，返回 `source_id/version_id/chunk 坐标/score`。
- **向量空间锁定**：查询用的 `Embedder.ID()` 必须与索引 `embedding_rule` 一致，否则拒绝（对齐 KNOWLEDGE-K0 §6 / PRODUCT-OUTLINE §9.7）。
- **扫描上限**：暴力检索设显式候选上限并在报告/日志标注，不把"返回 topK"当成"扫过全库"。

**为什么先不上 ANN**：语料只有 6 份、几百个 chunk，暴力毫秒级；`KNOWLEDGE-K0 §6` 与 `PRODUCT-OUTLINE §8` 都要求"先比较基线、不预先锁定向量库"。等评测数据证明暴力不够（延迟或规模）再引入 ANN，且那时也换实现不动接口。

**未验证**：暴力检索在真实语料规模下的延迟；BLOB 读写与 GORM 的适配；并发重建与查询。

---

## 4. 决策三：混合融合策略

| 候选 | 说明 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. RRF（倒数排名融合）** | `score=Σ 1/(k+rank)`，取两条链路的排名 | 免调参、与分数量纲无关；正好呼应文档"不直接相加不同尺度分数" | 丢弃原始分数信息 | **推荐** |
| B. 归一化加权求和 | 各自 min-max 后加权 | 可调权重 | 需归一化，阈值/尺度不稳定，易过拟合 dev | 落选（可作为 RRF 的对照） |
| C. 并集 + 最好排名去重 | 合并两列表按最好名次排 | 简单 | 等价于 RRF 的粗粒度版 | 落选（RRF 更规范） |

**选 A（RRF，k 可配，默认 60）**：
- 关键词侧：复用 `Rank` 的 source 排名（已存在，`test/rag/evaluator.go`）。
- 向量侧：`VectorSearch` 的 chunk 结果按 source 取最好名次聚合。
- 融合在 **source 粒度**输出，使评测的 `retrieve() []string` 与现有 `Evaluate` 完全兼容，三路（关键词/向量/混合）可直接同桌对比。
- 引用坐标：混合结果里每个 source 保留"代表坐标"（关键词命中 offset 或向量 chunk start），交给现有 `read_source` 校验，**不让模型自报引用**。

**为什么保持 source 粒度**：现有题集的 `sources` 是 source-level，改 chunk 口径会让基线不可比。chunk 级只作为附加信息（命中位置有效性）报告，符合 `test/rag/README.md "进入分段实现后再固定 chunk-level 口径"` 的约定。

**未验证**：RRF 的 k 取值对本题集的影响（只有 6 份资料、12 可答题，样本太小，k 敏感性大概率测不出显著差异，如实标注）。

---

## 5. 决策四：分块参数要不要动

| 候选 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| **A. 保持 `rune-window-800-overlap-100-v1`** | 与关键词基线、现有 chunk 索引、evidence 机制一致；单变量对比 | 对 embedding 略粗 | **推荐（本轮）** |
| B. 改小到 400~500 | 语义更聚焦 | 需重建索引、破坏关键词基线可比性，混淆"向量 vs 分块"两个变量 | 落选（后续单独 A/B） |
| C. Markdown 结构感知分段 | 更贴语义 | 新规则、工作量与变量更多 | 落选（后置，文档已列为 K-2 之后） |

**选 A**：本轮"只加向量这一条变量"，分块不动。若向量召回差且怀疑分块，另开独立 A/B 并换规则版本（`chunks.md` 已规定改窗口必须换 rule version）。

---

## 6. 索引生命周期与失败

- **embedding 与导入解耦**：导入事务只建文本 chunk 索引（现状不变，保持"关闭模型仍可导入/阅读"）。embedding 是独立的、幂等的 `EmbedVersion` 步骤。
- **默认不联网导入**：新增配置 `KNOWLEDGE_EMBED_ON_IMPORT`（默认 **off**）。开时在导入提交后 best-effort 触发；关时由显式步骤/评测触发。避免把网络调用塞进导入事务（事务里做网络是坏味道）。
- **状态机复用**：与 chunk 索引并行，记录 `missing/ready/outdated`（`outdated` = embedding_rule 与当前代码不同）。
- **幂等**：`(chunk_id, embedding_rule)` 唯一；重建同规则覆盖旧向量；换模型写入新规则记录，旧规则向量保留但**不参与查询**，需显式重建/切换。
- **失败处理**：embedding 失败不标记 ready（对齐 `PRODUCT-OUTLINE §9.3`）；失败可见、可重试；查询侧遇到 missing 明确降级为关键词，不假装混合成功。

---

## 7. 评测升级与诚实口径

- 新增 `rageval` 对照测试（如 `TestRAGVectorComparison`），一次跑**关键词 / 向量 / 混合**三路，输出**一份**对照报告；保留现有 `TestRAGDevelopmentEvaluation` 关键词基线不动。
- 报告新增字段：embedding 模型 id / 维度 / 规则指纹、嵌入 chunk 数、embedding usage（缺则 **null**，不填零）、逐题三路候选与缺失、融合贡献、chunk 命中位置有效性。沿用 corpus/cases/implementation 的 SHA256 与配置记录。
- **口径变更必须写清**：现有 README 写"不调用真实模型、不消耗模型 token"——**加入向量后将不再成立**，需改为"关键词基线不调用模型；向量/混合对照会调用远端 embedding API，产生少量 token 费用，记录或标未知"。
- holdout 6 题**只跑一次最终验收**，不用于调参；报告明确 `PASS ≠ 质量达标`，6 份语料取前 5 的区分度局限照旧声明。

---

## 8. 明确未验证 / 待确认（不夸大）

1. ~~远端 embedding 端点是否可用、维度、usage、批量上限~~ **已于 2026-10 实测**：`text-embedding-v3` / 1024 维 / 返回 usage / 批 ≤10 条。仍待验证：真实语料规模下的限流、长文本（>8192 token）截断行为、以及多文本批量在 WSL 网络下的稳定性。
2. 暴力检索在真实规模下的延迟——本轮语料太小，**给不出生产结论**。
3. RRF 的 k 敏感性——样本太小，**大概率不可测**，如实标注。
4. 向量召回质量——**在跑完对照报告前，不宣称比关键词强**；关键词基线 recall@5=1.000 意味着本题集上向量"提升空间"可能被天花板掩盖。
5. 换 embedding 模型/维度的迁移与重建成本——只设计规则，未实测。
6. 全部新测试**待用户 WSL 运行**，本地只 `go vet`。

---

## 9. 切片计划（每片等确认）

| 切片 | 内容 | 是否动代码 | 交付 |
|---|---|---|---|
| **1（本片）** | 选型与设计 | 否 | 本文件 + 状态文档更新 |
| 2 | `Embedder` 接口 + `RemoteEmbedder` + MySQL 向量表(v5) + `EmbedVersion` + `VectorSearch` | 是 | 实现 + `go vet` 三标签 + 测试清单（待 WSL）+ 决策问答 |
| 3 | RRF 混合融合 + 新检索工具 + 引用坐标 | 是 | 同上 |
| 4 | 三路对照评测报告 + README 口径更新 + holdout 最终验收 | 是 | 报告 + 诚实口径 + TODO/§5 更新 |

每片完成：`go vet -tags 'knowledgeintegration rageval contexteval' ./...` + 测试清单标"待 WSL" + 更新 K 系列与 AGENTS.md §5。

---

## 10. 需要你拍板的决策点

1. ~~embedding 用哪家/哪个模型~~ **已定**：`text-embedding-v3`（1024 维，端点见第 2 节）。请确认沿用 v3（推荐）还是换 v2（1536 维）。
2. **接受评测调用远端 embedding**（打破现有"零模型 token"口径）吗？
3. **向量存储**：同意"MySQL BLOB + Go 暴力余弦"作为 K-2 基线吗（先不上 ANN/外部库）？
4. **检索工具**：新增独立工具（如 `search_sources_hybrid`），**不改**现有 `search_sources` 契约——可以吗？
5. **导入触发**：`KNOWLEDGE_EMBED_ON_IMPORT` 默认 off（保持离线可导入），评测/重建显式嵌入——可以吗？
6. **分块保持 800/100** 不动，作为单变量——可以吗？
7. `.env.example` 增加 `EMBEDDING_*` 示例（不动你的 `.env`）——可以吗？
