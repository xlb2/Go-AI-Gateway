# 混合召回（K-2 切片3）

状态：2026-10。本切片实现**关键词 + 向量**的 RRF 融合（`Store.HybridSearch`）与独立工具 `search_sources_hybrid`。选型见 [rag/vector-retrieval-design.md](../rag/vector-retrieval-design.md) 第 4 节；向量侧契约见 [vector.md](vector.md)。

## 契约

| 接口 | 行为 |
|---|---|
| `knowledge.QueryTerms(query)` | 关键词侧查询规划：英文/代码标识符 + 连续汉字双字词，去重，上限 32。与 `test/rag/evaluator.go` 同口径 |
| `(*Store).HybridSearch(ctx, owner, library, embedder, query, topK)` | 融合两路召回到 **source 粒度**，返回 `HybridHit` 列表 |
| `knowledge.NewReadingToolsWithEmbedder(store, owner, library, embedder)` | 追加第 4 个工具 `search_sources_hybrid`；不传 embedder 时 `NewReadingTools` 仍是原 3 个 |

`HybridHit` 每个候选带两侧 1-based 名次（`keyword_rank` / `vector_rank`，0=该侧未命中）与 RRF 分，并保留 `offset`（关键词精确命中）与 `chunk_start`（向量分段起点）供 `read_source` 定位，**不返回正文**。

降级语义：向量侧没有就绪索引时 `VectorIndexed=false`，结果退化为**仅关键词**；调用方据此知道融合没生效，不能当成"两路都查了"。

## 决策问答（为什么）

**Q1 为什么用 RRF，不用加权求和？**
关键词侧分是"命中词数"，向量侧分是余弦，**量纲不可加**（`PRODUCT-OUTLINE.md §8` 明确反对直接相加）。RRF 只用两侧的**名次**：`score = Σ 1/(60+rank)`，免调参、与分数量纲无关，也不会因为某一侧分数量级大就压倒另一侧。落选方案（min-max 归一化加权）需要归一化、权重易过拟合 dev 小样本。

**Q2 为什么融合停在 source 粒度？**
K-0 题集按 source 标注标准来源，source 粒度让"关键词基线 / 向量 / 混合"三路可直接同桌对比；chunk 级只作为**附加坐标**带上供定位。若改成 chunk 粒度，基线不可比（`test/rag/README.md` 已约定"进入分段后再固定 chunk 口径"）。

**Q3 为什么新增独立工具，不改 `search_sources`？**
`search_sources` 是精确关键词查询，被现有回归和 evidence 机制依赖；改它的入参/语义会牵动一批既有行为。新增 `search_sources_hybrid` 让"精确查代码名"和"自然语言混合查"各司其职，回归风险为零。工具顺序上它排在第 4 位，既有测试用的 `[0]/[1]/[2]` 不受影响。

**Q4 关键词侧为什么不用整句？**
自然语言整句几乎不会字面命中。关键词侧的价值在精确术语/代码名，所以用 `QueryTerms` 抽词后逐词查、按"命中不同词数"排名（与评测基线同口径）。

**Q5 为什么 RRF 常数取 60？**
业界常用默认值，且本题集只有 6 份资料、样本太小，k 的敏感性大概率测不出差异——**如实标注**，不宣称调优过。

## 召回错了怎么排查

1. 看 `VectorIndexed`：false 说明向量侧没生效（没索引/模型不符），此时结果等于关键词单路。
2. 看每个候选的 `keyword_rank` / `vector_rank`：都命中却排序不对 → 查 RRF 与两侧排名；只有一侧命中 → 另一侧为何漏（关键词：用词不同；向量：索引/维度/语义）。
3. keyword 侧漏 → 检查 `QueryTerms` 是否抽出了关键术语；vector 侧漏 → 按 [vector.md](vector.md) 的排查顺序。
4. 真实质量只能靠 `rageval` 三路对照报告判定，不靠单次观感。

## 实测发现（2026-10-09，小样本，不可外推）

K-0 六份语料、12(dev)/5(holdout) 道可答题，`text-embedding-v3`。dev：关键词=混合 recall@1 0.833 > 向量 0.750；"holdout"（已消费降级）：**向量 1.000/0.800 反超，混合与关键词完全相同（0.900/0.400）**。详见 [rag/README.md](../rag/README.md)。

**RRF 奖励共识（agreement），可能把"只有一路命中、且在该路排名靠后"的正确答案挤出去。** 逐题机制（Q17，标准来源 `[S05, S06]`）：

- 向量侧把 S06 排第 5（`[S04,S05,S03,S01,S06]`）；关键词侧完全没有 S06（`[S01,S02,S03,S04,S05]`）。
- RRF：S01/S03/S04/S05 是**两路都命中**的共识项，占据前四；第 5 名额由"只在关键词排第 2 的 S02"（1/(60+2)=0.01613）与"只在向量排第 5 的 S06"（1/(60+5)=0.01538）竞争，S02 微弱胜出 → **S06 掉出前五**。
- 结果：纯向量 recall@5=1.0，而"向量+关键词"的混合反而漏了 S06（recall@5=0.5）。

结论（限定小样本）：**两路都命中时 RRF 很好；当正确答案只被一路找到且排名靠后时，RRF 可能不如让那一路单独出结果。** 这不是实现错误，是排名融合对"单路低召回候选"不友好的固有性质。是否调 k、去重重排或对单路候选加权，**等加了干扰文档、样本有区分度后再定**，不凭 6 份语料调参。

## 验证

```bash
# 纯单测（QueryTerms，无 DB）
scripts/test-fast.sh -run KnowledgeQueryTerms
# 集成（假 embedder，不联网；需 im_mysql:3307）
scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeHybrid
```

新增：`test/knowledge/hybrid_test.go`（1 项，无标签）、`test/knowledge/hybrid_mysql_test.go`（2 项，`knowledgeintegration`）。本地 `go vet -tags 'knowledgeintegration rageval contexteval' ./...` 通过；**测试待用户 WSL 运行**。

## 未做 / 边界（如实）

- **混合工具未接入 Web chat 路径**（该路径暂停扩展，且需要装配 embedder）；本切片提供 `Store.HybridSearch` 与 `NewReadingToolsWithEmbedder`，评测路径直接调用前者。
- **关键词侧逐词查询并翻页收齐候选**（`SearchAllSources`，每词最多 10 页 × 10 份）：词多则多次 DB 往返；超过页数上限明确失败，不静默丢候选。
- 假 embedder 只验证融合机制，**不代表语义质量**；`PASS ≠ 质量达标`。
- RRF 的 k 敏感性未验证（样本太小）。
