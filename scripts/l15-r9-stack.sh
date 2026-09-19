#!/usr/bin/env bash
# R9 lane 候选栈：l15-r9-api(:18109) + l15-r9-worker，复用 llm_default 网络与 llm-api-1 的加密密钥。
# 用法：bash scripts/l15-r9-stack.sh up | down
set -euo pipefail
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
QUEUE=l15-r9-queue
COMMON=(
  -e APP_ENCRYPTION_KEY="$ENC"
  -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432
  -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory
  -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379
  -e WORKER_QUEUE_NAME="$QUEUE"
)
case "${1:-up}" in
  up)
    docker rm -f l15-r9-api l15-r9-worker >/dev/null 2>&1 || true
    docker run -d --name l15-r9-api --network llm_default -p 18109:8080 "${COMMON[@]}" l15-r9-api:cand >/dev/null
    docker run -d --name l15-r9-worker --network llm_default "${COMMON[@]}" l15-r9-worker:cand >/dev/null
    for _ in $(seq 1 40); do
      code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18109/api/v1/health || true)
      [ "$code" != "000" ] && { echo "api ready health=$code"; exit 0; }
      sleep 1
    done
    echo "api not ready" >&2; exit 1 ;;
  down) docker rm -f l15-r9-api l15-r9-worker >/dev/null 2>&1 || true; echo "removed" ;;
  *) echo "usage: $0 [up|down]" >&2; exit 2 ;;
esac
