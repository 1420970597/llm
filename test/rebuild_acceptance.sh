#!/usr/bin/env bash
# 重建验收用的 api/worker 容器（1808N 端口 + llm_default 网络 + 从 llm-api-1 取加密密钥）。
#
# 为什么是这个脚本：验收必须打自己构建的镜像，不能打 :3210（那是 main 镜像，
# 新路由必然 404/405）。跑完全量 CI 后重建，再跑 test_acceptance_7requirements.py。
#
# 用法：bash test/rebuild_acceptance.sh [api|worker|both]
set -euo pipefail

TARGET="${1:-both}"
cd "$(dirname "$0")/.."
ROOT="$PWD"

# 加密密钥必须与已有数据一致，否则解密 provider key 会失败 → LLM 调用全 401。
ENC_KEY=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
PG_HOST=llm-postgres-1
REDIS_HOST=llm-redis-1

build_and_run() {
  local name="$1" dockerfile="$2" image="$3" port="$4" queue="$5"
  echo "==> building $name ($image)"
  docker build -q -f "deployments/docker/$dockerfile" -t "$image" . >/dev/null
  docker rm -f "$name" >/dev/null 2>&1 || true
  local port_args=()
  [ -n "$port" ] && port_args=(-p "$port:8080")
  docker run -d --name "$name" \
    --network llm_default \
    "${port_args[@]}" \
    -e APP_ENCRYPTION_KEY="$ENC_KEY" \
    -e POSTGRES_HOST="$PG_HOST" -e POSTGRES_PORT=5432 \
    -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev \
    -e POSTGRES_DB=llm_factory \
    -e REDIS_HOST="$REDIS_HOST" -e REDIS_PORT=6379 \
    -e WORKER_QUEUE_NAME="$queue" \
    "$image" >/dev/null
  echo "    $name up"
}

case "$TARGET" in
  api)    build_and_run accept-api    api.Dockerfile    accept-api:main    18100 accept-queue ;;
  worker) build_and_run accept-worker worker.Dockerfile accept-worker:main ""    accept-queue ;;
  both)
    build_and_run accept-api    api.Dockerfile    accept-api:main    18100 accept-queue
    build_and_run accept-worker worker.Dockerfile accept-worker:main ""    accept-queue
    ;;
  *) echo "usage: $0 [api|worker|both]" >&2; exit 2 ;;
esac

echo "==> 等待健康检查"
for _ in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18100/api/v1/health || true)
  [ "$code" != "000" ] && { echo "    health=$code (就绪)"; exit 0; }
  sleep 1
done
echo "    ❌ 30 秒内未就绪" >&2
exit 1
