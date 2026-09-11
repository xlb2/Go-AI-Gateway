#!/usr/bin/env sh
# 重置本地验证状态，让下一轮验证从干净状态开始。
#
#   scripts/reset-state.sh        # 清所有 agent:* 键 + audit 产物
#   scripts/reset-state.sh 3      # 只清 UserID 3
#
# 只会动两类东西，都在本项目范围内：
#   1) Redis 里 agent:* 开头的键（记忆日志 / 折叠指针 / 待审批 / 溢出内容）
#   2) audit/ 目录下审批真实执行留下的日志（.gitignore 已忽略）
# 不碰 MySQL 里的业务数据，也不碰别的 Redis 键。
set -e
cd "$(dirname "$0")/.."

if command -v redis-cli >/dev/null 2>&1; then
    RC="redis-cli"
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^im_redis$'; then
    RC="docker exec im_redis redis-cli"
else
    echo "找不到 redis-cli，也没发现运行中的 im_redis 容器。先 docker start im_redis。" >&2
    exit 1
fi

if [ -n "$1" ]; then
    uid="$1"
    keys="agent:V2:history:$uid agent:V2:fold:$uid agent:pending:$uid"
    echo "将删除 UserID $uid 的键：$keys"
    # shellcheck disable=SC2086
    $RC DEL $keys >/dev/null
else
    keys=$($RC --scan --pattern 'agent:*' 2>/dev/null || true)
    if [ -z "$keys" ]; then
        echo "没有 agent:* 键需要清理。"
    else
        echo "将删除以下键："
        echo "$keys" | sed 's/^/  /'
        echo "$keys" | while read -r k; do [ -n "$k" ] && $RC DEL "$k" >/dev/null; done
    fi
fi

if [ -f audit/defense.log ]; then
    rm -f audit/defense.log
    echo "已删除 audit/defense.log"
fi

echo "完成。"
