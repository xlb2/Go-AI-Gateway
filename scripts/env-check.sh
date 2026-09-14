#!/usr/bin/env sh
# 环境体检：一条命令告诉你"这台机器能不能跑这个项目"、缺什么、怎么补。
#
#   scripts/env-check.sh
#
# 在 Windows Git Bash 和 WSL/Linux 下都能跑（会自动提示当前在哪一侧）。
# 起因：公司电脑的端点防护（EDR）会把 Go 编译产物反复当成新样本报毒，
# 在 WSL 里跑是最省事的绕法 —— 但前提是 WSL 里真的有能用的 Go 和能连上的存储。
# 这个脚本就是用来一次性确认这几件事，免得你来回试。
set -u

PROBLEM=0
ok()   { printf '  \033[32m✅\033[0m %s\n' "$1"; }
bad()  { printf '  \033[31m❌\033[0m %s\n' "$1"; PROBLEM=$((PROBLEM+1)); }
warn() { printf '  \033[33m⚠️\033[0m  %s\n' "$1"; }

cd "$(dirname "$0")/.."

echo "=== 1. 当前在哪一侧 ==="
UNAME=$(uname -s)
case "$UNAME" in
    Linux)
        if grep -qi microsoft /proc/version 2>/dev/null; then
            ok "WSL（Linux）—— 端点防护管不到这里，跑测试不会报警"
        else
            ok "原生 Linux"
        fi
        ;;
    MINGW*|MSYS*|CYGWIN*)
        warn "Windows（Git Bash）—— 编译产物会被端点防护扫到；能跑，但可能反复弹告警"
        ;;
    *)
        warn "未知平台：$UNAME"
        ;;
esac

echo
echo "=== 2. Go 版本够不够 ==="
NEED=$(grep -E '^go [0-9]' go.mod 2>/dev/null | awk '{print $2}')
echo "  go.mod 要求：${NEED:-未知}"
if command -v go >/dev/null 2>&1; then
    HAVE=$(go version | awk '{print $3}' | sed 's/^go//')
    echo "  本机版本：$HAVE"
    # 逐段比大小（1.10 > 1.9，不能按字符串比）
    newer=$(printf '%s\n%s\n' "$NEED" "$HAVE" | sort -V | tail -1)
    if [ "$newer" = "$HAVE" ]; then
        ok "Go 版本满足"
    else
        bad "Go 太旧。Ubuntu 22.04 自带的 golang-go 是 1.18，不够用；装官方新版："
        echo "       wget -q https://go.dev/dl/go${NEED}.linux-amd64.tar.gz"
        echo "       sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go${NEED}.linux-amd64.tar.gz"
        echo "       echo 'export PATH=\$PATH:/usr/local/go/bin' >> ~/.bashrc && . ~/.bashrc"
    fi
else
    bad "没有 go。同上面的命令装官方版（别用 apt 的 golang-go，版本太老）"
fi

echo
echo "=== 3. 存储能不能连上 ==="
# 不用 nc（不一定装了），优先 bash 的 /dev/tcp，回退到 python3
probe() {
    host=$1; port=$2
    if command -v python3 >/dev/null 2>&1; then
        python3 -c "import socket,sys; s=socket.socket(); s.settimeout(2)
try: s.connect(('$host', $port)); sys.exit(0)
except Exception: sys.exit(1)" && return 0 || return 1
    fi
    if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then exec 3<&- 2>/dev/null; return 0; fi
    return 1
}

if probe 127.0.0.1 6379; then
    ok "Redis 6379 通（秒级回归只需要这个）"
else
    bad "Redis 6379 不通。检查：① docker 容器 im_redis 在跑；② Docker Desktop → Settings → Resources → WSL Integration 打开当前发行版；③ 之后 wsl --shutdown 重进"
fi

if probe 127.0.0.1 3307; then
    ok "MySQL 3307 通（只有起完整网关才需要）"
else
    warn "MySQL 3307 不通（跑测试不影响；起网关会失败）"
fi

echo
echo "=== 4. 脚本换行符 ==="
if command -v file >/dev/null 2>&1 && file scripts/test-fast.sh 2>/dev/null | grep -q CRLF; then
    bad "scripts/*.sh 是 CRLF —— Linux 下会报 bad interpreter: /bin/sh^M"
    echo "       修：sed -i 's/\r$//' scripts/*.sh"
else
    ok "脚本是 LF（.gitattributes 已锁住，别人 clone 也不会变 CRLF）"
fi

echo
echo "=== 5. 依赖缓存 ==="
CACHE=$(go env GOMODCACHE 2>/dev/null || echo "")
if [ -n "$CACHE" ] && [ -d "$CACHE/github.com" ]; then
    ok "模块缓存已存在（$CACHE）"
else
    warn "模块缓存是空的 —— 第一次 go test 会联网下载全部依赖（约一两百 MB），耐心等一次就好"
fi

echo
if [ "$PROBLEM" -eq 0 ]; then
    echo "结论：可以跑了 →  scripts/test-fast.sh -v"
else
    echo "结论：还有 $PROBLEM 个问题要先解决（见上面 ❌）。"
fi
