# K-0 RAG 样本基线

状态：题集已有 27 条问题，开发集与三路对照已运行（2026-10-09，用户 WSL，真实 embedding `text-embedding-v3`/1024）。**语料已升级 k0-corpus-v2（18 份，见下），原 holdout（Q15-Q20）因口径修复被消费、并入 dev；新 holdout-2（Q21-Q27）已盲写冻结，未运行。** 所有既有数字结论均为小样本、不可外推；早期 6 份语料的 recall@5 接近天花板，后续加入干扰文档后才拉开差距。详见下方「三路对照实测结果」。来源召回不代表答案正确率或真实 Agent 质量。

资料来自已有讲义及框架验收说明；18 份固定副本随仓库保存在 `test/rag/corpus/`。评测只读副本，导入随机独立测试库，结束后清理测试库。不操作业务库、不读取.env、不抓取网站、不修改参考仓库。

## 语料清单 v1

清单路径保留原工作区的相对路径，用于记录来源；实际读取位置为仓库内 `test/rag/corpus/<清单路径>`，不依赖某台电脑的目录。SHA256 是原文件字节的记录；2026-10-19 冻结副本时全部 18 份均与既有清单匹配，题集与清单未因迁移修改。`.gitattributes` 固定副本使用 LF，避免跨平台换行转换导致哈希漂移。副本仅作为评测材料，其中的历史状态和链接不代表当前工程状态。语料变更需另建版本，不能更新哈希掩盖漂移。

| ID | 路径 | SHA256 |
|---|---|---|
| S01 | lessons/01-agent-loop.md | 740F489C501990E067C8DE95C8C2FF05D9EE759ED02F5FB28913425DDB15F592 |
| S02 | lessons/02-compaction-preservation.md | F8A246DDA0DFAFF70AACFA19044B935D2894ABBE7D0DFE34EE274510ED4FD345 |
| S03 | lessons/03-framework-map.md | 7FB2D473F308E930BA45EBB64A7F8911F781C239F8929B54EE3B8B7E0D81068B |
| S04 | lessons/04-tool-approval.md | 5C74FCCE46355991CC3E91C4ADF8564C73F7F5D629438F7F343E239456D9A09A |
| S05 | lessons/05-run-ownership.md | 2224490FF05E606904B1D668DA1707001DB0899401E1D78E64F84662545FE62E |
| S06 | Go-AI-Gateway/test/framework.md | 74C551092423CB028EA2E5472654E3FC4B85AC4EA786BB60E236BC9E8161466A |

S01/S03 等含历史措辞，不自动代表当前代码状态。S03 记载子日志未实现，S05 描述独立子日志，刻意保留这个冲突用于检查时间与来源处理。不能靠文件修改时间认定内容更权威。允许回答“旧讲义与后续记录不同，需按阶段区分”，不能猜测无法证实的运行验收。

## 语料清单 v2（k0-corpus-v2，2026-10-09）

v2 = v1 六份标准语料（S01-S06，内容与哈希不变）+ 十二份干扰文档（D01-D12）。目的：6 份语料取前 5 的 recall@5 已饱和（三路全 1.000），加同领域干扰文档把检索难度拉回可区分区间。干扰文档收录标准：**同领域、不含任何题目的标准答案、不含题目本身**——一份文档若能让"找到它但没找到标准来源"的检索实际上也算答对，就不能进语料（标准来源不再唯一，ground truth 被污染）。

| ID | 路径 | 选入理由 |
|---|---|---|
| D01 | lessons/06-harness-reference-and-integration.md | 同主题（turn/step/会话），与 S01 话题最接近，是最强干扰 |
| D02 | Go-AI-Gateway/README.md | 项目能力自述，术语密度高 |
| D03 | Go-AI-Gateway/docs/cli.md | CLI 使用文档，同领域 |
| D04 | HARNESS-LOOP-REVIEW.md | 循环替换复盘，与 S01 话题近 |
| D05 | HARNESS-PRODUCTION.md | 与 dsh/codex 差距证据，同领域 |
| D06-D09 | test/knowledge/{search,chunks,evidence,conversations}.md | 知识库契约文档，同领域 |
| D10-D12 | test/{context-shadow,history-source,model-evidence}.md | 上下文/来源/证据契约文档，同领域 |

**已排除（含答案或题目，进语料即污染）**：`KNOWLEDGE-K0.md`（直接含 20 题与标准来源）、`AGENT-ENGINEER-PLAN.md`（第六节复述 lesson 01 三问答案）、`AGENTS.md`/`AGENTS1.md`、`HARNESS-STUDY.md`、`HARNESS-TODO.md`、`test/rag/` 全部（评测设施自身）。**已排除（干扰太弱）**：deepseek-harness/codex 的 README——中英文与文风差异太大，靠表面特征即可区分，增加不了检索难度。完整哈希清单见 `manifest.json`。

已知残留风险：D01 第 32-33 行简述了 codex StepContext 的动机，与 Q25 的话题相邻但不含其两层伤害的完整答案；若干扰文档与某题答案的边界日后变得模糊，应将该文档换出并另建语料版本。

## 使用方式

2026-10-19 换机复现验证：用户在 WSL 运行 `scripts/eval-rag.sh`，成功从仓库内固定副本导入并完成关键词开发集评测。dev 共20题、17题可答，source recall@5=0.765，完整召回12/17，无答案题有候选3/3；Q02/Q11/Q12/Q15/Q17 仍漏来源。数值与既有 v2 关键词记录一致，仅确认本次执行结果，不代表向量/混合对照已重跑或答案生成质量通过。报告为 `results/dev-20261019T073356Z-1678.json`（本地忽略）；holdout-2 未运行。

问题与判据见 cases.json。`sources` 表示本题需要覆盖的来源，不是所有等价正确片段的穷举；`evidence` 提供人工定位词；`must_not` 是禁止作出的无依据结论。空 sources 表示预期无答案。

开发集用于分段与检索调试，holdout 保留集不要用于参数选择。这里的划分是初始约定，不代表独立盲测；后续用户可以补充实现者未见的新问题。

初期使用 source-level recall@5、引用位置有效性、人工核对结论支撑、无答案处理、耗时与成本。进入分段实现后，再标注等价相关 chunk 集合并固定 chunk-level 口径。问题少，不能用百分比宣传普遍质量。

更新删除、跨用户过滤、索引重试和旧任务覆盖新版本是独立工程场景，不用静态问答样本代替这些集成测试。

## 开发集执行器

```bash
# 在克隆后的仓库根目录执行
scripts/test-fast.sh -run RAGEvaluation
scripts/test-fast.sh -tags knowledgeintegration
bash scripts/eval-rag.sh            # 关键词基线，无模型调用
bash scripts/eval-rag-compare.sh    # 三路对照，调用真实 embedding（消耗少量 token）
```

前两项预计2/167；后两项各自独立运行1个实际评测测试，需要im_mysql及测试建库权限，沿用KNOWLEDGE_TEST_DSN配置。都不启动业务API。脚本生成唯一JSON文件到test/rag/results/，该目录已忽略提交；报告保存成功才显示报告路径，旧报告不覆盖。额外rageval标签防止普通回归意外重复评测或读取工作区语料。

**token 口径（2026-10 更正）：** `eval-rag.sh` 的关键词基线**不调用任何模型**；`eval-rag-compare.sh` 的向量/混合对照**会调用远端 embedding API**（6 份语料嵌入 + 每题一次查询嵌入），产生少量 token 费用，报告记录语料嵌入用量、查询用量（缺则 null）。不再笼统声称"评测零 token"。

### 三路对照（K-3）

`eval-rag-compare.sh` 在同一份题集、同一语料、同一分段规则下，分别评测 **关键词基线 / 向量 / 混合(RRF)** 三路的 source-level recall@5，输出到一份 `compare-<split>-*.json`。报告含语料/题集/实现文件的 SHA256、embedding 模型与规则（模型:维度）、语料与查询 embedding 用量。用 `KNOWLEDGE_RAG_SPLIT=holdout` 只跑保留集；默认 `dev`。执行 PASS 只表示跑完，**分数低也如实记录，不代表质量达标**。

### 三路对照实测结果（2026-10-09，小样本，不可外推）

环境：embedding `text-embedding-v3`（1024 维），语料嵌入 11030 prompt token/次；查询用量 dev 201/201、holdout 87/87（向量/混合）。语料 6 份、dev 12 可答、holdout 5 可答——**样本过小，任何数字都不可外推**。

**第一次运行（口径不公允，已作废）：** 向量适配器只取 top-5 chunk 去重成 source、可能不足 5 个来源，**系统性打压向量**。数字：dev 关键词 1.000 / 向量 0.917 / 混合 1.000；holdout 三路均 0.900。此结果不用于任何结论。

**第二次运行（口径修正 + 增加 recall@1）：**

| split | 指标 | 关键词 | 向量 | 混合(RRF) |
|---|---|---|---|---|
| dev | recall@5 | 1.000 | 1.000 | 1.000 |
| dev | recall@1 | 0.833 | 0.750 | 0.833 |
| holdout（已消费） | recall@5 | 0.900 | **1.000** | 0.900 |
| holdout（已消费） | recall@1 | 0.400 | **0.800** | 0.400 |

- dev 上 recall@5 三路全饱和（1.000），只有 recall@1 能区分：关键词=混合 > 向量。
- "holdout" 上**向量两指标都最好，混合与关键词完全相同**（RRF 没带来增益，反而继承了关键词的漏检 S06）。
- 无答案处理：三路都把所有无答案题的候选照常返回（dev 2/2、holdout 1/1），**没有拒答机制**；recall 指标不衡量这一点。

**第三次运行（2026-10-09，k0-corpus-v2 = 6 标准 + 12 干扰，dev 17 可答题）：** 干扰文档生效，区分度拉开：

| split | 指标 | 关键词 | 向量 | 混合(RRF) |
|---|---|---|---|---|
| dev (v2) | recall@5 | 0.765 | 0.824 | **0.912** |
| dev (v2) | recall@1 | 0.412 | 0.588 | **0.706** |

- 混合两指标均最优，且与单路差距超出单题噪声（>0.1）——三路互补性首次被实测证实：如 Q02 关键词整题漏 S02、向量救回；Q12 关键词漏 S05、向量漏 S03、混合双全。
- 干扰文档按设计起干扰作用：D01/D04/D05 多次出现在 top-1（topically-close false positive）。
- **Q17 的 S06 三路仍全漏**（与 v1 时代一致）——三路同挂指向数据/覆盖问题而非算法问题，留作失败样本分析入口。
- 仍为小样本（17 可答题）、dev 已用于迭代、来源召回 ≠ 答案正确率、无拒答机制；结论不可外推。
- 附带工程修复：v2 语料触发"关键词候选超过单页"保护，已加 `Store.SearchAllSources` 翻页收齐（每词上限 10 页），`HybridSearch` 与两个评测入口改用它，回归 `TestKnowledgeHybridKeywordMultiPage`。

**holdout-2（Q21-Q27）仍冻结，未运行。**

**holdout 降级声明：** 第一次运行后我们用 holdout 结果去发现并修复了口径 bug——**这一步已经消费了保留集**。因此原 holdout（Q15-Q20）不再算独立盲测，**已并入 dev（cases k0-v2），禁止再用它迭代或宣布结论**。

**holdout-2（Q21-Q27，冻结中）：** 2026-10-09 新建 7 道保留题，全部盲写——只依据原文内容（6 道覆盖此前零题目的 S01，含 1 道 conflict 时间语义题；1 道 unanswerable），未参考任何检索报告或漏检记录。冻结规则：在下一次最终验收之前**不得运行** `KNOWLEDGE_RAG_SPLIT=holdout`，不得根据 dev 失败样本修改这批题。

**下一步：** 基于已完成的 v2 dev 对照分析失败样本，再决定查询策略、分段或向量权重；holdout-2 继续冻结。所有数字继续标注"小样本、不可外推"。

执行顺序：读取manifest.json与cases.json → 校验所有原文SHA256 → 创建随机测试库 → 按manifest顺序导入原文并建片段 → 只对dev检索 → 检查命中位置确实对应关键词 → 计算得分 → 保存报告 → 清理测试库。语料变化、数据库失败或定位错误直接失败，不更新哈希来掩盖漂移。问题、evidence、expected、must_not字段均不导入数据库。

固定查询策略 `han-bigram-identifiers-v1-max32`：只读取question，先提取英文/代码标识符，再提取连续汉字的相邻双字词，去重后最多32词；每词调用 SearchAllSources 翻页收齐候选，最多10页，超出预算明确报错。每来源按不同命中词数排序，同分按S01等语料ID排序，取前5。它是无模型基线，不是生产Agent的查询规划、BM25或语义检索，也没有根据标准答案挑关键词。

报告包括语料/题集版本及SHA256、检索与分段代码SHA256、策略/规则版本、逐题查询词和实际来源、缺失来源、原文命中位置。临时数据库ID通过source_ids映射到稳定S01等ID；库会清理，报告不可当业务资源定位器。耗时是本机本轮检索和评分时间，不含导入，不是生产P99。

## 分数解释

- `macro_source_recall_at_5`：可回答题逐题计算找到的标准来源数/所需来源数，再平均。多来源题只找到一个会部分得分。
- `macro_source_recall_at_1`：只看排名第一的来源是否命中；语料小、recall@5 饱和时用它区分排序质量。
- `all_expected_sources_found`：全部标准来源都出现在前5的可回答题数量。
- `unanswerable_with_candidates`：无答案题仍返回候选的数量，只提示需要后续拒答机制，不等于模型已经给出错误答案。
- 来源召回不证明引用片段支持结论；本批没有生成答案，不计算答案准确率或拒答准确率。

6份资料取前5本身较宽松，必须结合逐题漏检和候选观察，不把高召回宣传成通用能力。**原 holdout 已因口径修复被消费并并入 dev（见上）；holdout-2（Q21-Q27）已冻结，未运行前才是独立保留集。**

`PASS`只表示执行与数据完整性检查通过，不设置为了绿灯而调整的召回阈值；分数再低也如实写报告。三路结果见上，**一律标注"小样本、不可外推"**；下一步加干扰文档提升区分度，再决定查询策略/分段/向量权重。
