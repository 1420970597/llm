#!/usr/bin/env bash
# 迁移冒烟：起一个临时 Postgres，**按文件名顺序应用全部**迁移，再断言新迁移真的生效。
#
# 为什么需要这个脚本（而不是把逻辑写在 Makefile 里）：issue #217 的 B1 指出，
# `db-migrate-smoke` 只执行 `sql/migrations/0001_phase1_foundation.sql`，
# 而 AGENTS.md §1 明确把它描述为「验证 SQL 迁移脚本」（复数）。实测：
# 走一遍旧 target 后库里只有 3 张表，`samples` / `sample_versions` / `batches` /
# `review_projections` / `legacy_imports` 全部不存在 —— 0022–0039 一条都没被验证。
# 也就是说这条「迁移验证」命令**永远绿灯**，却对 0022 之后的 18 份迁移零覆盖。
#
# 设计要点（与 `scripts/go-test-postgres.sh` 保持一致）：
#   * postgres 官方镜像初始化阶段会先起一个**只监听 unix socket 的临时服务器**，
#     `pg_isready` 在那一阶段就会返回「接受连接」。因此就绪判定要求
#     「日志里出现两次 ready」+「TCP 上连续两次 SELECT 1 成功」，避免撞上关停窗口。
#   * 逐个迁移 `psql -v ON_ERROR_STOP=1` 应用：坏迁移必须让命令立刻失败，
#     并**打印是哪一个文件**（只报 SQLSTATE 在 CI 日志里定位很费时间）。
#   * 应用完成后断言核心表存在：这挡住「退回只跑 0001」的静默回归 ——
#     否则 target 再次变成永远绿灯的空转。
#   * trap 保证临时容器被删掉，即使中途失败。
#
# 用法（在仓库根）：
#   bash scripts/db-migrate-smoke.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PORT="${DB_MIGRATE_SMOKE_PORT:-15433}"
CONTAINER="llm-postgres-migrate-smoke"
IMAGE="${POSTGRES_IMAGE:-postgres:17-alpine}"
DB_NAME="${POSTGRES_DB:-llm_factory}"
DB_USER="${POSTGRES_USER:-llm_factory}"
DB_PASS="${POSTGRES_PASSWORD:-llm_factory_dev}"

# 迁移目录必须存在且非空：空目录会让「全部应用」变成「什么都没做」还报成功。
MIGRATIONS=(sql/migrations/*.sql)
if [ ! -e "${MIGRATIONS[0]}" ]; then
  echo "[db-migrate-smoke] 找不到迁移文件（sql/migrations/*.sql）" >&2
  exit 1
fi

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "[db-migrate-smoke] 启动临时 Postgres（$IMAGE，宿主端口 $PORT）"
cleanup
docker run -d --rm --name "$CONTAINER" \
  -e "POSTGRES_DB=$DB_NAME" -e "POSTGRES_USER=$DB_USER" -e "POSTGRES_PASSWORD=$DB_PASS" \
  -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null

echo "[db-migrate-smoke] 等待就绪（要求：日志两次 ready + TCP SELECT 1 连续两次成功）"
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
  echo "[db-migrate-smoke] Postgres 未在 60 秒内就绪" >&2
  docker logs --tail 50 "$CONTAINER" >&2 || true
  exit 1
fi

echo "[db-migrate-smoke] 按文件名顺序应用全部迁移（共 ${#MIGRATIONS[@]} 个）"
for file in "${MIGRATIONS[@]}"; do
  if ! docker exec -i "$CONTAINER" psql -h 127.0.0.1 -v ON_ERROR_STOP=1 -q \
    -U "$DB_USER" -d "$DB_NAME" <"$file"; then
    echo "[db-migrate-smoke] 迁移失败：$file" >&2
    exit 1
  fi
done
echo "[db-migrate-smoke] 迁移完成：${#MIGRATIONS[@]} 个文件"

# 断言核心表真的建出来了。这一条是防「静默退回只跑 0001」的关键：
# 只跑 0001 时下面每张表都不存在，而命令此前会照样返回 0。
# 注意：`schema_migrations` 不在本表 —— 它由 Go 侧 `internal/migrate` 在启动时
# 创建（记录已应用文件名），不属于 SQL 迁移产物，因此断言它会误报。
REQUIRED_TABLES=(datasets samples sample_versions batches review_projections legacy_imports)
missing=()
for table in "${REQUIRED_TABLES[@]}"; do
  exists=$(docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -tAc \
    "SELECT to_regclass('public.$table') IS NOT NULL")
  if [ "$exists" != "t" ]; then
    missing+=("$table")
  fi
done
if [ "${#missing[@]}" -gt 0 ]; then
  echo "[db-migrate-smoke] 迁移未真正生效，缺少表：${missing[*]}" >&2
  echo "  只应用 0001 时就会得到这个结果 —— 说明 target 可能退回了「只跑第一个迁移」。" >&2
  exit 1
fi
echo "[db-migrate-smoke] 核心表已建：${REQUIRED_TABLES[*]}"

docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -c "\dt" | head -20
echo "[db-migrate-smoke] OK：全部迁移在干净库上应用成功且核心表存在"
