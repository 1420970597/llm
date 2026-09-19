#!/usr/bin/env bash
# R2 lane 候选栈：api + worker（专属端口 18102，专属队列 l15-r2-queue）。
# 只动自己创建的容器，绝不触碰 llm-* 容器。
set -euo pipefail
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
docker build -q -f deployments/docker/api.Dockerfile -t l15-r2-api:cand .
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r2-worker:cand .
docker rm -f l15-r2-api l15-r2-worker >/dev/null 2>&1 || true
common=(-e "APP_ENCRYPTION_KEY=$ENC" -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r2-queue)
docker run -d --name l15-r2-api --network llm_default -p 18102:8080 "${common[@]}" l15-r2-api:cand >/dev/null
docker run -d --name l15-r2-worker --network llm_default "${common[@]}" l15-r2-worker:cand >/dev/null
for _ in $(seq 1 40); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18102/api/v1/health || true)
  [ "$code" != "000" ] && {
    echo "api 就绪 health=$code"
    exit 0
  }
  sleep 1
done
echo "api 40 秒内未就绪" >&2
exit 1
