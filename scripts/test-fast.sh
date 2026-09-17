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
# 显式 -v 保留排障日志；默认捕获详细输出，以便统计跳过且失败时不丢证据。
for arg in "$@"; do
    case "$arg" in
        -v|-v=true|-test.v|-test.v=true|-json|-json=true)
            go test ./... -count=1 "$@"
            exit 0
            ;;
    esac
done

log=$(mktemp)
trap 'rm -f "$log"' EXIT
if go test ./... -count=1 "$@" -v >"$log" 2>&1; then
    awk '
        /^--- PASS:/ { passed++ }
        /^--- SKIP:/ { skipped++ }
        /^[[:space:]]*--- SKIP:/ { print }
        END {
            printf "PASS: %d top-level tests passed, %d skipped\n", passed, skipped
            if (passed == 0) print "WARNING: no top-level tests passed; check the filter."
        }
    ' "$log"
else
    status=$?
    cat "$log"
    exit "$status"
fi
