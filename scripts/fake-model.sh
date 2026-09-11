#!/usr/bin/env sh
# 起本地假模型（OpenAI 兼容），默认 127.0.0.1:9099。
#
#   scripts/fake-model.sh                          # 内置场景
#   scripts/fake-model.sh -addr :9099 -scenario test/fakemodel/scenarios/default.json
#
# 起来之后把 .env 改成 VOLC_BASE_URL=http://127.0.0.1:9099/api/v3 即可，
# 业务代码一行不用改，且不烧 token、不依赖网络。
set -e
cd "$(dirname "$0")/.."
exec go run ./cmd/fakemodel "$@"
