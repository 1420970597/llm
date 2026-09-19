#!/usr/bin/env bash
# R4 lane 本地候选栈：构建 api/worker 镜像并起独立容器（18104），共享 llm_default 与 llm-postgres-1。
set -euo pipefail
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
docker build -q -f deployments/docker/api.Dockerfile -t l15-r4-api:cand . >/dev/null
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r4-worker:cand . >/dev/null
docker rm -f l15-r4-api l15-r4-worker >/dev/null 2>&1 || true
docker run -d --name l15-r4-api --network llm_default -p 18104:8080 \
  -e APP_ENCRYPTION_KEY="$ENC" \
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r4-queue \
  l15-r4-api:cand >/dev/null
docker run -d --name l15-r4-worker --network llm_default \
  -e APP_ENCRYPTION_KEY="$ENC" \
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r4-queue \
  l15-r4-worker:cand >/dev/null
for i in $(seq 1 40); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18104/api/v1/health || true)
  [ "$code" != "000" ] && {
    echo "api health=$code 就绪"
    exit 0
  }
  sleep 1
done
echo "api 未就绪" >&2
exit 1
