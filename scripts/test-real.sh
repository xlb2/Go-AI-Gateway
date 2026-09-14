#!/usr/bin/env sh
# 真模型端到端冒烟：跑 cmd/verify 的 6 段（子智能体并行 / spill / 防御审批 / auth:approve /
# 逼出自动 spill / 逼出压缩）。
#
#   scripts/test-real.sh          # 全部 6 段（慢，几分钟，要真 key）
#   scripts/test-real.sh pump     # 只跑第 6 段（灌对话逼压缩），改压缩逻辑后的快速回归
#
# 前置：
#   1) docker 容器 im_redis / im_rabbitmq / im_mysql 在跑
#   2) 另开一个终端起服务：scripts/run-api.sh
#   3) .env 里有真实的 VOLC_ACCESS_KEY / VOLC_ENDPOINT_ID
#
# 只想快速回归、不想烧 token 的话，用 scripts/test-fast.sh。
set -e
. "$(dirname "$0")/_common.sh"

code=$(curl -s -m 3 -o /dev/null -w '%{http_code}' http://localhost:8080/api/v1/metrics || true)
if [ "$code" != "200" ]; then
    echo "服务没在跑（/api/v1/metrics 返回 $code）。先另开终端执行：scripts/run-api.sh" >&2
    exit 1
fi

# 先编译再执行，不用 `go run`：go run 生成的临时 exe 会被端点防护反复当成新样本报毒。
mkdir -p bin
go build -o "bin/gwverify$EXE" ./cmd/verify
exec "./bin/gwverify$EXE" "$@"
