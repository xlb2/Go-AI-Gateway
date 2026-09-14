#!/usr/bin/env bash
# 秒级回归：假模型 + 真 Redis。**不需要真实模型 key，不消耗 token。**
#
#   scripts/test-fast.sh              # 跑全部
#   scripts/test-fast.sh -run Approval -v
#
# 只依赖 docker 里的 im_redis（没起的话用例会自动 skip，不会误报失败）。
# 测试是**进程内**起假模型（httptest），不启动任何独立服务 —— 也就不会惊动端点防护。
#
# 注意这里是 bash 而不是 sh：需要 `set -o pipefail` 来保证管道里 go test 的
# 退出码不被 awk 顶掉。一个"测试挂了但脚本报成功"的回归脚本比没有更糟。
set -euo pipefail
. "$(dirname "$0")/_common.sh"

# 注意是 ./... 而不是 ./test/... ——
# 纯函数单测散在各器官包里（比如 internal/harness/tokenmeter），
# 只跑 ./test/... 会把它们静默漏掉：看着全绿，其实没测。
#
# awk 过滤掉"这个包没有测试文件"的噪声行：一次跑 30 个包会有 28 行
# `?  ... [no test files]`，把真正的 ok/FAIL 淹掉，而看结果的人只需要一眼看到绿还是红。
# 用 awk 而不是 grep -v：被过滤干净时 grep 会返回 1（没匹配），awk 永远返回 0。
go test ./... -count=1 "$@" | awk '!/^\?/'
