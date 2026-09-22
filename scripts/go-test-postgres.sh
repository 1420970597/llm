#!/usr/bin/env bash
# 起一个临时 Postgres、按文件名顺序应用全部迁移，然后在 golang 容器里执行命令
#（默认 `go test ./...`）。用于本地与 CI 的**真实 DB** 门禁（Issue #160 T32）。
#
# 为什么需要它：仓库里大量集成测试要求 LLM_TEST_POSTGRES_DSN，未设置时
# `t.Skip`。而「go test 成功但集成测试全部 Skip」不满足 T32 的验收项 ——
# 那种绿色只证明编译通过。这个脚本把「跳过」变成「真的跑过」。
#
# 用法：
#   scripts/go-test-postgres.sh                       # go test ./...
#   scripts/go-test-postgres.sh go test ./internal/store/...
#   POSTGRES_IMAGE=postgres:17-alpine scripts/go-test-postgres.sh
#
# 设计要点：
#   * postgres 发布到宿主 127.0.0.1 的一个高位端口，golang 容器用
#     --network host 访问它。这样两个容器不需要共享自定义网络，
#     脚本不需要把网络名透给调用方。
#   * 迁移逐个 `psql -v ON_ERROR_STOP=1` 应用：一个坏迁移必须让整条命令
#     立刻失败，而不是留下「部分应用」的库让测试去猜。
#   * trap 保证容器被删掉，即使中途失败。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PORT="${TEST_POSTGRES_PORT:-55432}"
CONTAINER="llm-test-pg-$$"
IMAGE="${POSTGRES_IMAGE:-postgres:17-alpine}"
DB_NAME="${POSTGRES_DB:-llm_factory}"
DB_USER="${POSTGRES_USER:-llm_factory}"
DB_PASS="${POSTGRES_PASSWORD:-llm_factory_dev}"
GO_IMAGE="${GO_IMAGE:-golang:1.24-alpine}"

if [ "$#" -eq 0 ]; then
  set -- go test ./...
fi

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "[go-test-postgres] 启动临时 Postgres（$IMAGE，宿主端口 $PORT）"
docker run -d --rm --name "$CONTAINER" \
  -e "POSTGRES_DB=$DB_NAME" -e "POSTGRES_USER=$DB_USER" -e "POSTGRES_PASSWORD=$DB_PASS" \
  -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null

# 就绪判定必须**确定**，不能只看 pg_isready。
#
# 实测（CI 上必现、本地不一定复现）：postgres 官方镜像在初始化阶段会先起一个
# **临时服务器**（只监听 unix socket）跑 initdb 脚本，然后把它关掉、再起真正
# 对外服务的那个。`pg_isready` 在临时服务器阶段就会返回「接受连接」，于是
# 紧接着的 psql 会撞上 `FATAL: the database system is shutting down` ——
# 一条与迁移内容毫无关系的失败，只在启动时序落到某个窗口时出现。
#
# 因此改用两条**与真实可用性等价**的判据，且都要求「确认后仍成立」：
#   1. 日志里出现两次 "database system is ready to accept connections"
#      （第一次是临时服务器，第二次才是真正对外服务的实例）；
#   2. 用 TCP 真实执行 `SELECT 1` 成功，隔 1 秒再成功一次
#      （临时实例不监听 TCP，且它正在关闭时会拒绝第二次）。
ready=0
for _ in $(seq 1 60); do
  ready_lines=$(docker logs "$CONTAINER" 2>&1 | grep -c 'database system is ready to accept connections' || true)
  if [ "$ready_lines" -ge 2 ] \
     && docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -tAc 'SELECT 1' >/dev/null 2>&1; then
    sleep 1
    if docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -tAc 'SELECT 1' >/dev/null 2>&1; then
      ready=1
      break
    fi
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "[go-test-postgres] Postgres 未在 60 秒内就绪" >&2
  docker logs --tail 50 "$CONTAINER" >&2 || true
  exit 1
fi

echo "[go-test-postgres] 应用迁移"
for file in sql/migrations/*.sql; do
  # 用 TCP（-h 127.0.0.1）而不是 unix socket：只有真正对外服务的实例才监听 TCP，
  # 这本身就是避开「临时服务器」的第二道保证。
  #
  # 失败时打印**具体是哪个迁移文件**：只报 psql 的 SQLSTATE 无法定位，
  # 而 CI 日志里翻找是哪一份迁移挂了很费时间。
  if ! docker exec -i "$CONTAINER" psql -h 127.0.0.1 -v ON_ERROR_STOP=1 -q \
    -U "$DB_USER" -d "$DB_NAME" <"$file"; then
    echo "[go-test-postgres] 迁移失败：$file" >&2
    exit 1
  fi
done
echo "[go-test-postgres] 迁移完成：$(find sql/migrations -name '*.sql' | wc -l) 个文件"

export LLM_TEST_POSTGRES_DSN="postgres://$DB_USER:$DB_PASS@127.0.0.1:$PORT/$DB_NAME?sslmode=disable"

echo "[go-test-postgres] 执行：$*"
# 不要用 exec：exec 会替换掉当前 shell，trap 于是不会触发，
# 临时 Postgres 容器会一直留在后台占着端口（实测踩过一次）。
docker run --rm --network host -v "$REPO_ROOT:/w" -w /w \
  -e "LLM_TEST_POSTGRES_DSN=$LLM_TEST_POSTGRES_DSN" \
  "$GO_IMAGE" "$@"
