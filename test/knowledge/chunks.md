# 版本化文本分段

分段是不可变原文版本的派生数据，保存在 `knowledge_chunks` 与 `knowledge_chunk_indexes` 中。

## 坐标与规则

每个片段绑定 version_id，保存 ordinal、start/end 与 content。位置按 Unicode 码点、零起点、右侧不含；BOM、CRLF、Markdown 与代码保持原样。

当前规则 `rune-window-800-overlap-100-v1`：每片最多800码点，相邻重叠100。修改窗口或算法需更新规则版本。这是字符窗口，不是 Markdown 语义解析，段落与代码块可能跨片段。

## 构建与重建

导入在同一事务内保存来源、版本、分段及索引记录，任一步失败均回滚。相同正文去重不自动重建已有索引。

显式重建校验 owner/library/source 并锁定当前版本，事务替换旧片段与规则记录；失败保留旧完整索引。原文和 current_version_id 不变。片段自增 ID 可能变化，外部引用应使用版本与字符范围。

同步构建没有后台作业表或持久化 building 状态。旧资料不在启动迁移时自动扫描，需显式补建；大规模并行构建与性能未作保证。

## API

以下路径在 `/api/v1` 下，沿用 JWT 与资源归属校验：

| 方法与路径 | 行为 |
|---|---|
| GET `/libraries/:library_id/sources/:source_id/index` | 当前版本索引状态、规则与数量 |
| POST `/libraries/:library_id/sources/:source_id/index/rebuild` | 同步重建 |
| GET `/libraries/:library_id/sources/:source_id/versions/:version_id/chunks?after=0` | ordinal升序，每页50条，以最后ordinal续页 |

状态为 ready、missing 或 outdated。重建不承诺跨规则分页快照；当前 UI 没有独立索引管理按钮。

## 测试与关联

```bash
bash scripts/test-fast.sh -tags knowledgeintegration -run KnowledgeChunks
```

覆盖 Unicode/CRLF 切片、并发重建、写入回滚、旧数据补建、分页与权限。关键词路径见 [search.md](search.md)，向量派生数据见 [vector.md](vector.md)。
