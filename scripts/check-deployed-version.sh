#!/usr/bin/env bash
# 部署版本自检（issue #88）：5 秒内回答「当前跑的前端是不是本地 HEAD 构建的」。
#
# 为什么需要这个脚本：本地部署曾停在旧镜像上（compose 工程名从 compose 改成 llm 之后
# 留下 compose-web-user:latest 与 llm-web-user:latest 两个名字），而页面上、镜像里
# 都没有任何版本信息，只能靠比对「镜像构建时间 vs 提交时间」间接推断 —— 实测就是这么
# 漏掉了一次：五个阶段路由被旧构建里的重定向逻辑全部吃掉，却看起来像产品缺陷。
#
# 用法：
#   ./scripts/check-deployed-version.sh                      # 默认 http://localhost:3210
#   ./scripts/check-deployed-version.sh http://host:3210
#
# 退出码：0 = 版本一致；1 = 不一致（应重建）；2 = 无法判定（缺信息）
set -uo pipefail

BASE_URL="${1:-http://localhost:3210}"
BASE_URL="${BASE_URL%/}"

cd "$(dirname "${BASH_SOURCE[0]}")/.."

LOCAL_SHA="$(git rev-parse HEAD 2>/dev/null || echo '')"
if [ -z "$LOCAL_SHA" ]; then
  echo "❌ 无法读取本地 HEAD（不在 git 仓库里？）" >&2
  exit 2
fi

echo "本地源码 HEAD : ${LOCAL_SHA:0:7}  ($(git log -1 --format='%s' HEAD 2>/dev/null | cut -c1-60))"

# 读部署中的版本。version.json 是构建期产物（见 apps/web-user/vite.config.ts），
# 因此它反映的是「页面上这份 JS 是哪一版」，而不是「后端是哪一版」。
VERSION_JSON="$(curl -fsS --max-time 5 "${BASE_URL}/version.json" 2>/dev/null || echo '')"

if [ -z "$VERSION_JSON" ]; then
  echo "❌ ${BASE_URL}/version.json 取不到。" >&2
  echo "   可能原因：前端未重建（旧镜像不含该文件）、容器未起、或端口不对。" >&2
  echo "   处理：docker compose up -d --build web-user" >&2
  exit 2
fi

DEPLOYED_SHA="$(printf '%s' "$VERSION_JSON" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
BUILD_TIME="$(printf '%s' "$VERSION_JSON" | sed -n 's/.*"buildTime"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"

echo "部署中前端版本: ${DEPLOYED_SHA:0:7}  (buildTime=${BUILD_TIME:-未知})"

if [ "$DEPLOYED_SHA" = "unknown" ] || [ -z "$DEPLOYED_SHA" ]; then
  echo ""
  echo "⚠️  部署中的前端**未注入版本号**，无法自证与源码的对应关系。" >&2
  echo "   这不等于「一定落后」，但你不能假定它等于本地 HEAD。" >&2
  echo "   处理：GIT_SHA=$(git rev-parse HEAD) BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) \\" >&2
  echo "         docker compose up -d --build web-user" >&2
  echo "   （或用 ./scripts/build-with-version.sh）" >&2
  exit 2
fi

if [ "$DEPLOYED_SHA" = "$LOCAL_SHA" ]; then
  echo "✅ 一致：部署中的前端就是本地 HEAD 构建的，可以用它做验收。"
  exit 0
fi

echo ""
echo "❌ 不一致：部署中的前端不是本地 HEAD 构建的。" >&2
echo "   本地 HEAD      : $LOCAL_SHA" >&2
echo "   部署中前端版本 : $DEPLOYED_SHA" >&2
echo "" >&2
echo "   **不要用这个部署做验收** —— 你看到的行为可能来自旧代码（issue #88 就是这样误判成产品缺陷的）。" >&2
echo "   处理：GIT_SHA=$LOCAL_SHA BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) \\" >&2
echo "         docker compose up -d --build web-user" >&2
exit 1
