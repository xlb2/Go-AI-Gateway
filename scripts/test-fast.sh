#!/usr/bin/env sh
# 秒级回归：假模型 + 真 Redis。**不需要真实模型 key，不消耗 token。**
#
#   scripts/test-fast.sh              # 跑全部
#   scripts/test-fast.sh -run Approval -v
#
# 只依赖 docker 里的 im_redis（没起的话用例会自动 skip，不会误报失败）。
set -e
cd "$(dirname "$0")/.."
exec go test ./test/... -count=1 "$@"
