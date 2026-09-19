#!/usr/bin/env bash
# 构建并启动本 lane 的候选 api/worker 容器（打自己构建的镜像，不是 :3210 的 main 镜像）。
#
# 与 /root/llm/test/rebuild_acceptance.sh 同一套路，但用本 lane 专属的容器名与端口，
# 避免并发 lane 抢名字：l15-r7-api:18107、l15-r7-worker（共享 WORKER_QUEUE_NAME）。
#
# 用完执行：docker rm -f l15-r7-api l15-r7-worker
set -euo pipefail

cd "$(dirname "$0")/.."

PORT="${L15_R7_PORT:-18107}"
QUEUE="l15-r7-queue"
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)

docker build -q -f deployments/docker/api.Dockerfile -t l15-r7-api:cand .
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r7-worker:cand .

docker rm -f l15-r7-api l15-r7-worker >/dev/null 2>&1 || true

common=(
  --network llm_default
  -e APP_ENCRYPTION_KEY="$ENC"
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379
  -e WORKER_QUEUE_NAME="$QUEUE"
)

docker run -d --name l15-r7-api -p "$PORT:8080" "${common[@]}" l15-r7-api:cand >/dev/null
docker run -d --name l15-r7-worker "${common[@]}" l15-r7-worker:cand >/dev/null

for _ in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/health" || true)
  [ "$code" != "000" ] && { echo "l15-r7 stack ready on :$PORT (health=$code)"; exit 0; }
  sleep 1
done
echo "❌ 30 秒内未就绪" >&2
exit 1
