#!/usr/bin/env bash
# R10 lane 的候选容器（api :18110 + worker，共享私有队列）。
set -euo pipefail
PORT=18110
QUEUE=l15-r10-queue
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
docker build -q -f deployments/docker/api.Dockerfile -t l15-r10-api:cand . >/dev/null
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r10-worker:cand . >/dev/null
docker rm -f l15-r10-api l15-r10-worker >/dev/null 2>&1 || true
common=(-e "APP_ENCRYPTION_KEY=$ENC" -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME="$QUEUE")
docker run -d --name l15-r10-api --network llm_default -p "$PORT:8080" "${common[@]}" l15-r10-api:cand >/dev/null
docker run -d --name l15-r10-worker --network llm_default "${common[@]}" l15-r10-worker:cand >/dev/null
for _ in $(seq 1 40); do
  c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/health" || true)
  [ "$c" != "000" ] && { echo "api ready health=$c"; exit 0; }
  sleep 1
done
echo "api not ready" >&2; exit 1
