#!/usr/bin/env bash
# T32/T33 isolated failure drills: real Redis stop/restart, real MinIO signed
# requests, Postgres leases/outbox and schema guard. Existing stacks are never
# targeted. All named resources belong to this invocation and are removed.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"
redis_container="llm-studio-fault-redis-$$"
minio_container="llm-studio-fault-minio-$$"
control_dir="$(mktemp -d /tmp/llm-studio-fault-control.XXXXXX)"
controller_pid=''
cleanup() {
  if [ -n "$controller_pid" ]; then kill "$controller_pid" >/dev/null 2>&1 || true; wait "$controller_pid" 2>/dev/null || true; fi
  docker rm -fv "$redis_container" "$minio_container" >/dev/null 2>&1 || true
  rm -f "$control_dir/redis-stop" "$control_dir/redis-start" "$control_dir/redis-stopped" "$control_dir/redis-started"
  rmdir "$control_dir" 2>/dev/null || true
}
trap cleanup EXIT
redis_host_port="${TEST_REDIS_PORT:-56389}"
docker run -d --name "$redis_container" -p "127.0.0.1:$redis_host_port:6379" redis:7-alpine redis-server --save '' --appendonly no >/dev/null
docker run -d --name "$minio_container" --user 0:0 -p '127.0.0.1::9000' \
  -e MINIO_ROOT_USER=studio-fault-admin -e MINIO_ROOT_PASSWORD=studio-fault-secret-123 \
  docker.io/bitnamilegacy/minio@sha256:451fe6858cb770cc9d0e77ba811ce287420f781c7c1b806a386f6896471a349c \
  server /bitnami/minio/data >/dev/null
redis_port=$(docker port "$redis_container" 6379/tcp | sed 's/.*://')
minio_port=$(docker port "$minio_container" 9000/tcp | sed 's/.*://')
export LLM_TEST_REDIS_ADDR="127.0.0.1:$redis_port"
export LLM_TEST_S3_ENDPOINT="http://127.0.0.1:$minio_port"
export LLM_TEST_S3_ACCESS_KEY=studio-fault-admin
export LLM_TEST_S3_SECRET_KEY=studio-fault-secret-123
export LLM_TEST_FAULT_CONTROL_DIR="$control_dir"
export TEST_POSTGRES_PORT="${TEST_POSTGRES_PORT:-55549}"
export TEST_SCHEMA_FAULTS=1
ready=0
for _ in $(seq 1 60); do
  if docker exec "$redis_container" redis-cli ping >/dev/null 2>&1 && curl -fsS "$LLM_TEST_S3_ENDPOINT/minio/health/live" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [ "$ready" != 1 ]; then echo '::error::fault infrastructure not ready' >&2; exit 1; fi

# Go requests an actual service interruption via files in an isolated temporary
# directory. The container is kept stopped until Go has observed the transport
# error and the preserved Postgres outbox, then restarted on explicit request.
(
  for _ in $(seq 1 900); do
    if [ -f "$control_dir/redis-stop" ] && [ ! -f "$control_dir/redis-stopped" ]; then
      docker stop --time 1 "$redis_container" >/dev/null
      touch "$control_dir/redis-stopped"
    fi
    if [ -f "$control_dir/redis-start" ] && [ ! -f "$control_dir/redis-started" ]; then
      docker start "$redis_container" >/dev/null
      touch "$control_dir/redis-started"
    fi
    sleep 1
  done
) &
controller_pid=$!
scripts/go-test-postgres.sh sh -c 'gofmt -s -w apps internal && go vet ./... && go build ./... && sh scripts/check-studio-failure-tests.sh'
