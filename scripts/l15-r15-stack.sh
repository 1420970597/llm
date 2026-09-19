#!/usr/bin/env bash
# R15 lane 的候选容器（api :18191 + worker，共享私有队列）。
# 用于 --with-api 的真实端到端。用完 docker rm -f 清掉自己的容器。
set -euo pipefail
PORT=18191
QUEUE=l15-r15-queue
# 取加密密钥：优先从已运行的 api 容器读；读不到时回落到仓库 .env；
# 再读不到就用 internal/config 的内置默认值（三者必须一致，否则解不开库里已有的密文）。
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY 2>/dev/null || true)
if [ -z "$ENC" ] && [ -f .env ]; then
  ENC=$(grep -E '^APP_ENCRYPTION_KEY=' .env | head -1 | cut -d= -f2-)
fi
if [ -z "$ENC" ]; then
  ENC="phase1-dev-only-32-byte-secret!!!"   # internal/config 的内置默认值
fi
echo "==> 使用加密密钥来源已解析（长度 ${#ENC}）"
docker build -q -f deployments/docker/api.Dockerfile -t l15-r15-api:cand . >/dev/null
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r15-worker:cand . >/dev/null
docker rm -f l15-r15-api l15-r15-worker >/dev/null 2>&1 || true
common=(-e "APP_ENCRYPTION_KEY=$ENC" -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME="$QUEUE")
docker run -d --name l15-r15-api --network llm_default -p "$PORT:8080" "${common[@]}" l15-r15-api:cand >/dev/null
docker run -d --name l15-r15-worker --network llm_default "${common[@]}" l15-r15-worker:cand >/dev/null
for _ in $(seq 1 40); do
  c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/health" || true)
  [ "$c" != "000" ] && { echo "api ready health=$c"; exit 0; }
  sleep 1
done
echo "api not ready" >&2; exit 1
