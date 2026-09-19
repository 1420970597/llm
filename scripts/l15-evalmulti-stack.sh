#!/usr/bin/env bash
# l15-evalmulti lane 的候选容器（api :18170 + worker，共享私有队列）。
# 为什么要自己的队列：主栈 llm-worker-1 会抢走任务，导致本 lane 的评估永远不执行。
set -euo pipefail
ID=evalmulti
PORT=18170
QUEUE=l15-evalmulti-queue
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)

docker build -q -f deployments/docker/api.Dockerfile -t l15-$ID-api:cand . >/dev/null
docker build -q -f deployments/docker/worker.Dockerfile -t l15-$ID-worker:cand . >/dev/null
docker rm -f l15-$ID-api l15-$ID-worker >/dev/null 2>&1 || true

common=(-e "APP_ENCRYPTION_KEY=$ENC" \
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=$QUEUE)

docker run -d --name l15-$ID-api --network llm_default -p $PORT:8080 "${common[@]}" l15-$ID-api:cand >/dev/null
docker run -d --name l15-$ID-worker --network llm_default "${common[@]}" l15-$ID-worker:cand >/dev/null

for _ in $(seq 1 40); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$PORT/api/v1/health || true)
  [ "$code" != "000" ] && { echo "api ready health=$code"; exit 0; }
  sleep 1
done
echo "api not ready in 40s" >&2
exit 1
