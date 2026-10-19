# 精确关键词搜索

`search_sources` 用于定位代码名或确切术语，再由 `read_source` 获取原文。它与自然语言 [混合搜索](hybrid.md) 是独立工具。

## 契约

```json
{"query":"RunAgentTurn","after":0}
```

query 去除首尾空白后为1–128个 Unicode 码点，不允许 NUL。after 使用上一页 next_after。owner/library 由服务端绑定，未知参数拒绝。

只搜索所属资料库的当前版本正文，区分大小写，百分号与下划线不是通配符，标题不参与匹配。每页最多10份资料，按 source_id 升序，每份只返回最早命中的元数据与坐标，不返回正文；next_after 为0表示结束。

offset/match_end 为 Unicode 零起点、右侧不含。候选携带固定 version_id，后续读取不会切换到新版本。候选不等于模型已经读过原文，成功读取才进入 [证据投影](evidence.md)。

## 分段与原文回退

查询长度不超过100码点且索引规则有效时使用片段；缺失/过期索引或更长查询回退原文。100对应片段重叠长度，避免完整关键词跨窗口丢失。两路径在同一 SQL 语句内合并，多次命中取最早原文位置，`index_rule` 标识使用的分段规则。

查询采用参数化 SQL，先二进制 LOCATE，再将字节前缀换算为 Unicode 偏移。返回数量有限不代表数据库扫描量固定；当前没有全文倒排索引、同义词或语义相关度排名。

`Store.SearchAllSources` 为融合与评测逐页收齐单词候选，每词最多10页，超出时明确失败，不静默只取第一页。模型的单次 search_sources 仍保留分页接口。

## 测试

```bash
bash scripts/test-fast.sh -tags knowledgeintegration -run 'KnowledgeChunkSearch|KnowledgeSearch'
```

覆盖字面特殊字符、Unicode坐标、版本切换、归属、分页及搜索后读取的工具链。检索质量对照见 [RAG 评测](../rag/README.md)。
