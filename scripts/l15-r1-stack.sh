#!/usr/bin/env bash
# R1 lane 候选 API 容器（端口 18101）。前端 lane 本身不新增路由，
# 起候选栈仅为让 test/l15_stage_routes.mjs 打到「本 lane 构建的镜像」而不是 :3210 的 main 镜像。
set -euo pipefail
cd "$(dirname "$0")/.."
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
docker build -q -f deployments/docker/api.Dockerfile -t l15-r1-api:cand .
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r1-worker:cand .
docker rm -f l15-r1-api l15-r1-worker >/dev/null 2>&1 || true
common=(-e "APP_ENCRYPTION_KEY=$ENC" -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r1-queue)
docker run -d --name l15-r1-api --network llm_default -p 18101:8080 "${common[@]}" l15-r1-api:cand >/dev/null
docker run -d --name l15-r1-worker --network llm_default "${common[@]}" l15-r1-worker:cand >/dev/null
for i in $(seq 1 40); do
  c=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18101/api/v1/health || true)
  [ "$c" != "000" ] && {
    echo "health=$c"
    exit 0
  }
  sleep 1
done
echo "api 未就绪" >&2
exit 1
