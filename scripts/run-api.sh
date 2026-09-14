#!/usr/bin/env sh
# 起网关服务。先编译到 bin/ 再执行，**不用 `go run`**（见 scripts/build.sh 里的说明：
# go run 每次都生成新临时 exe，公司 EDR 会反复报毒）。
#
#   scripts/run-api.sh
#
# 想让它跑在本地假模型上（不烧 token）：先改 .env 的 VOLC_BASE_URL 指向假模型，
# 或者直接：VOLC_BASE_URL=http://127.0.0.1:9099/api/v3 scripts/run-api.sh
set -e
. "$(dirname "$0")/_common.sh"

mkdir -p bin
go build -o "bin/gwapi$EXE" ./cmd/api
exec "./bin/gwapi$EXE" "$@"
