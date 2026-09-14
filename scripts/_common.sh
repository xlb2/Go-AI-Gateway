#!/usr/bin/env sh
# 脚本公共部分：切到仓库根目录 + 探测本机可执行文件后缀。
#
# 为什么要探测后缀：同一套脚本要能在 Windows 的 Git Bash 和 WSL/Linux 下都跑 ——
# 在 WSL 里跑是绕开公司 EDR 编译告警的主要办法（见 README 的「在公司电脑上开发」一节）。
set -e
cd "$(dirname "$0")/.."

case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) EXE=".exe" ;;
    *)                    EXE="" ;;
esac
