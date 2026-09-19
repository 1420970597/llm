#!/usr/bin/env bash
# 带版本信息重建并启动前端（issue #88）。
#
# 为什么需要这个包装：compose 的变量插值**不做命令执行**，所以
# `docker compose up -d --build` 无法自动把 `git rev-parse HEAD` 传进 build args。
# 与其让用户手打两条易错的环境变量，不如给一条固定命令。
#
# 用法：
#   ./scripts/build-with-version.sh            # 重建 web-user
#   ./scripts/build-with-version.sh --all      # 重建全部服务（api/worker 也带上版本标识）
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

GIT_SHA="$(git rev-parse HEAD)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "==> 注入版本：GIT_SHA=${GIT_SHA:0:7}  BUILD_TIME=${BUILD_TIME}"

if [ "${1:-}" = "--all" ]; then
  GIT_SHA="$GIT_SHA" BUILD_TIME="$BUILD_TIME" docker compose up -d --build
else
  GIT_SHA="$GIT_SHA" BUILD_TIME="$BUILD_TIME" docker compose up -d --build web-user
fi

echo "==> 校验部署版本"
# 等前端就绪：nginx 起来很快，但要给容器一点时间。
for _ in $(seq 1 30); do
  if curl -fsS --max-time 3 "http://localhost:${WEB_USER_PORT:-3210}/version.json" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

exec ./scripts/check-deployed-version.sh "http://localhost:${WEB_USER_PORT:-3210}"
