# CM-1a 历史来源存储基础

SourceAt(owner, seq, offset, limit)从原始主日志建立来源；ReadSource(owner, id, offset, limit)回读。owner来自可信入口，不从模型参数获得。ID格式history:v1:owner:随机代次:原始seq，对消费者视为不透明。当前日志按user划分，没有独立项目空间；不声称具备项目隔离。子日志不在本接口范围。

每次单条/批量追加通过Lua同时管理generation键与RPUSH；日志为空时更换代次，非空时兼容补建元数据。读取以Lua原子核对代次并LINDEX。旧日志只创建旁路键、不重写事件；代次键丢失时旧ID失效，重新发现后获得新ID，不尝试猜测旧身份。正常重启保留Redis键则ID不变，但未执行真实进程重启验收。任意外部LSET/LTRIM、绕过新版写入口重建日志、回滚Redis整套快照均不在保证范围，历史索引仍依赖只追加契约。Redis Cluster多键槽支持未提供（与当前单Redis部署一致）。

正文按Unicode码点从0分页，limit=1..2000；offset等于末尾返回空页，超过末尾报错。返回事件type/role/interrupted，不能把中断回复当完成回复。只读取持久Content，不展开spill定位符，不包含ToolCalls参数；空正文不代表事件没有其它字段。返回大小有界，但当前仍取完整单条JSON并解码，非存储端流式大对象分页。

| 回归 | 边界 |
|---|---|
| TestHistorySourcePagesAndIdentity | 中文/emoji完整拼接、追加摘要后原seq仍可读、跨用户拒绝、页大小限制、单条及批量写入口、重建日志旧ID失效 |
| TestHistorySourceLegacyAndUnavailable | 旧事件原文不变、中断标志保留、代次丢失拒绝旧ID、坏JSON/未来版本/缺失事件报错 |

本地三标签go vet通过；2026-09-23用户WSL定向2、全量201通过，均0跳过。复验命令：

```bash
bash scripts/test-fast.sh -run HistorySource
bash scripts/test-fast.sh -tags knowledgeintegration
```

上述运行结果为用户提供的WSL证据，本轮未重跑。尚待CM-1后续：相邻事件/消息回读、外存完整内容与归属检查、来源发现和模型工具接入；本批不改变模型上下文选择，不增加模型调用。不把CM-1a当CM-1全部完成。
