#!/usr/bin/env bash
# l15-r11 取证用的候选 api/worker 容器（宿主端口 18106），复用 llm_default 网络与加密密钥。
set -euo pipefail
cd "$(dirname "$0")/.."
ENC=$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)
docker build -q -f deployments/docker/api.Dockerfile -t l15-r11-api:cand . >/dev/null
docker build -q -f deployments/docker/worker.Dockerfile -t l15-r11-worker:cand . >/dev/null
docker rm -f l15-r11-api l15-r11-worker >/dev/null 2>&1 || true
run_container() {
  local name="$1" image="$2" portmap="$3"
  local args=()
  [ -n "$portmap" ] && args=(-p "$portmap")
  docker run -d --name "$name" --network llm_default "${args[@]}" \
    -e APP_ENCRYPTION_KEY="$ENC" \
    -e POSTGRES_HOST=llm-postgres-1 -e POSTGRES_PORT=5432 \
    -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -e POSTGRES_DB=llm_factory \
    -e REDIS_HOST=llm-redis-1 -e REDIS_PORT=6379 -e WORKER_QUEUE_NAME=l15-r11-queue \
    "$image" >/dev/null
}
run_container l15-r11-api l15-r11-api:cand "18106:8080"
run_container l15-r11-worker l15-r11-worker:cand ""
echo "==> 等待健康检查"
for _ in $(seq 1 40); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18106/api/v1/health || true)
  [ "$code" != "000" ] && {
    echo "    health=$code (就绪)"
    exit 0
  }
  sleep 1
done
echo "    ❌ 40 秒内未就绪" >&2
exit 1
