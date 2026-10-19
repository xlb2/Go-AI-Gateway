# 分段索引第一批

## 数据契约

v4新增knowledge_chunks与knowledge_chunk_indexes，不改原文。每片段绑定不可变version_id，含ordinal、start/end和content；位置为Unicode码点，零起点、右侧不含。保留BOM、CRLF、Markdown及代码原样，不做规范化。规则`rune-window-800-overlap-100-v1`：最多800字符，相邻重叠100字符。规则版本保存在版本索引记录中，未来修改窗口或算法必须换规则版本。

这不是Markdown语义分段；代码块或段落可能跨窗口，结构解析需后续比较评测再加入。当前片段是原文切片，可按坐标逐字验证；没有向量、全文倒排或质量评分。

## 构建与失败

新导入：锁库 → 保存source/version → 分段 → 批量写片段 → 写规则/数量记录 → 提交。任何一步失败，原文和索引都回滚，HTTP返回失败，不产生“资料已导入但索引不完整”的成功回执。对已存在的相同正文仍返回去重结果，不自动重建旧资料。

重建：校验owner/library/source，按库与source行锁串行化，固定当前version，事务删除该版本旧片段、写入新片段及规则记录。提交前读者仍看到旧索引；失败回滚，旧ID/片段/规则记录保留。重建不改原文及current_version_id。不同版本的索引分开保存。

同步构建采用这一事务边界，未引入后台任务、重试队列或failed作业表。失败由请求返回；进程在未提交时退出由数据库回滚，不存在持久化building状态。不能据此宣称所有索引生命周期已完成，也不模拟一个不会更新的失败状态字段。大库并行索引和事务时长需后续另行设计。

## API

均在/api/v1下，沿用JWT鉴权：

| 方法与路径 | 行为 |
|---|---|
| GET /libraries/:library_id/sources/:source_id/index | 当前版本的ready/missing/outdated、版本ID、规则版本、片段数 |
| POST /libraries/:library_id/sources/:source_id/index/rebuild | 同步重建当前版本，成功返回ready |
| GET /libraries/:library_id/sources/:source_id/versions/:version_id/chunks?after=0 | 显式版本的片段，ordinal升序，每页50条；下一页after取最后ordinal |

资源逐层检查所属关系。missing表示该版本没有构建记录；outdated表示规则版本与当前代码不同。旧资料不在启动迁移时自动扫描，使用显式重建补齐。片段ID在重建后可能改变，外部定位应使用版本与字符范围，不把自增ID当不可变引用。当前规则固定，未来规则切换时仍需定义分页/索引代契约，不承诺跨规则重建分页快照。

UI尚未添加索引管理按钮；现有上传自然调用新导入路径。后续增量已将search_sources接入片段与原文回退，运行待验，详见[search.md](search.md)。不宣称搜索已加速，K-0开发集评测仍待建设。

## 验证

新增1项纯分段与3项MySQL集成测试，放在test/knowledge/chunks_test.go、chunks_mysql_test.go；包含Unicode/CRLF无损定位、并发重建、写片段/索引记录失败时回滚、旧数据重建、分页与权限。重复迁移检查确认1/2/3/4各一次。

```bash
scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeChunks
scripts/test-fast.sh -tags knowledgeintegration
```

用户WSL定向4、全量163已通过，0跳过；不覆盖后续分段检索增量。未运行真实生产迁移、导入性能测试或RAG质量评测。
