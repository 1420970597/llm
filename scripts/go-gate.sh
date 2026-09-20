#!/usr/bin/env sh
# Go 门禁（CI 等价），**按输出判定而不是按 exit code**。
#
# 为什么需要这个脚本：`gofmt -l` 在**列出未格式化文件时仍然返回 exit 0**。
# 因此 `gofmt -l ... && go test ./...` 这种写法会在有格式问题时继续往下跑，
# 看起来「本地全绿」，而 CI 的 workflow 是显式判断输出并 exit 1 —— 只有 CI 能发现。
# 本仓库已因此漏过两次（#94 与 PR #151）。
#
# 用法（在仓库根）：
#   docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh /w/scripts/go-gate.sh
set -eu

fail=0

# 1) gofmt：必须看输出
fmt_out=$(gofmt -l apps internal test 2>&1 || true)
if [ -n "$fmt_out" ]; then
  echo "❌ gofmt 发现未格式化的文件："
  echo "$fmt_out"
  fail=1
else
  echo "✓ gofmt clean"
fi

# 2) vet
if go vet ./... 2>&1; then echo "✓ go vet clean"; else echo "❌ go vet 失败"; fail=1; fi

# 3) build
if go build ./... 2>&1; then echo "✓ go build ok"; else echo "❌ go build 失败"; fail=1; fi

# 4) test
if go test ./... 2>&1; then echo "✓ go test ok"; else echo "❌ go test 失败"; fail=1; fi

if [ "$fail" != "0" ]; then
  echo "GO GATE FAILED"
  exit 1
fi
echo "GO GATE OK"
