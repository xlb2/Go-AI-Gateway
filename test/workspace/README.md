# 只读工作区工具测试

验证文件工具返回的真实内容、定位信息及访问边界。参数与限制见 [CLI 指南](../../docs/cli.md#只读工作区工具)。

| 测试 | 覆盖 |
|---|---|
| `TestWorkspaceReadAndSearch` | 目录、UTF-8文本、真实行号、读取分页与字面搜索 |
| `TestWorkspaceSearchContext` | 上下文窗口、相邻窗口去重、命中标记与非法范围 |
| `TestWorkspaceResultLimitsDistinct` | 正常分页、长行裁剪、输出预算与扫描上限分类 |
| `TestWorkspaceEmptySearchFields` | 空结果与 skipped 计数 |
| `TestWorkspaceBoundaries` | 绝对路径、穿越、Windows流路径、敏感文件与符号链接拒绝 |
| `TestWorkspaceLimitsAndCancellation` | 大文件、二进制、取消与截断 |
| `TestWorkspaceHarnessRead`（e2e） | 真 Harness 调用文件工具、SDK 内容与 Redis 事件对应 |

```bash
bash scripts/test-fast.sh -run Workspace
```

基础用例使用临时目录；Harness 集成另需 Redis。测试不模拟恶意本地进程并发替换文件、挂载点或硬链接。工具不修改文件、不运行命令，文件范围限制也不是进程沙箱。
