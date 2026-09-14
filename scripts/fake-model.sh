#!/usr/bin/env sh
# 起本地假模型（OpenAI 兼容），默认 127.0.0.1:9099。
#
#   scripts/fake-model.sh                          # 内置场景
#   scripts/fake-model.sh -scenario test/fakemodel/scenarios/default.json
#
# 起来之后把 .env 改成 VOLC_BASE_URL=http://127.0.0.1:9099/api/v3 即可，
# 业务代码一行不用改，且不烧 token、不依赖网络。
#
# 先编译到 bin/ 再执行（不用 go run）：go run 每次都生成新的临时 exe，EDR 会反复报毒。
set -e
. "$(dirname "$0")/_common.sh"

mkdir -p bin
go build -o "bin/fakemodel$EXE" ./cmd/fakemodel
exec "./bin/fakemodel$EXE" "$@"
