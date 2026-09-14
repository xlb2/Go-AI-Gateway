#!/usr/bin/env sh
# 一次性把所有命令行工具编译到 bin/，之后复用同一批文件。
#
# **为什么不直接 `go run`**：`go run` 每次都会生成一个**全新的临时 exe**
# （路径在 %TEMP%\go-buildXXX\...）。对端点防护（EDR）来说那是一个没见过的、
# 未签名的新样本 —— 所以每跑一次就报一次毒。编译到固定路径后，
# 同一个文件只会被扫一次，后续复用的是同一个哈希。
#
#   scripts/build.sh
set -e
. "$(dirname "$0")/_common.sh"

mkdir -p bin
go build -o "bin/gwapi$EXE"     ./cmd/api
go build -o "bin/gwverify$EXE"  ./cmd/verify
go build -o "bin/gwprobe$EXE"   ./cmd/probe
go build -o "bin/fakemodel$EXE" ./cmd/fakemodel
go build -o "bin/gwradar$EXE"   ./cmd/radar

echo "已编译到 bin/："
ls -1 bin/*"$EXE" 2>/dev/null | sed 's/^/  /'
