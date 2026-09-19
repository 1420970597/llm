#!/usr/bin/env bash
# 本 lane 的验收栈：构建并启动 l15-r5-api + l15-r5-worker，复用 llm_default 网络。
#
# 为什么需要它：宿主机 :3210 打的是 main 镜像，新路由在那里必然 404/405。
# 验收必须打自己构建的镜像。容器名/端口/队列名都带 lane 前缀，避免与其他
# 并行 lane 抢资源；队列独立还能保证本 lane 的 worker 不抢主栈任务。
set -euo pipefail

cd "$(dirname "$0")/.."

ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)

echo "==> 构建镜像"
docker build -q -f deployments/docker/api.Dockerfile -t l15-r5-api:cand . >/dev/null
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r5-worker:cand . >/dev/null

docker rm -f l15-r5-api l15-r5-worker >/dev/null 2>&1 || true

echo "==> 启动 api (18105->8080) 与 worker"
docker run -d --name l15-r5-api --network llm_default -p 18105:8080 \
  -e APP_ENCRYPTION_KEY="$ENC" \
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r5-queue \
  l15-r5-api:cand >/dev/null

docker run -d --name l15-r5-worker --network llm_default \
  -e APP_ENCRYPTION_KEY="$ENC" \
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r5-queue \
  l15-r5-worker:cand >/dev/null

echo "==> 等待健康检查"
for _ in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18105/api/v1/health || true)
  if [ "$code" != "000" ]; then
    echo "    health=$code (就绪)"
    exit 0
  fi
  sleep 1
done
echo "    ❌ 30 秒内未就绪" >&2
exit 1
