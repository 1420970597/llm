#!/usr/bin/env sh
# 部署前的 **schema 兼容性检查**（Issue #160 T33）。
#
# 用法（在仓库根，目标是待部署的数据库）：
#   scripts/check-schema-compat.sh "postgres://user:pass@host:5432/db?sslmode=disable"
#
# 判定（两个方向都要看，因为两种不匹配的后果不同）：
#
#   1. **库里有本二进制不认识的迁移** → 失败。
#      这表示库比代码新（有人回滚了版本但没回滚数据库，或部署了旧镜像）。
#      此时二进制会按旧 schema 假设写数据，而新列/新约束可能让写入语义出错。
#
#   2. **仓库里有库里没应用的迁移** → 只警告，不失败。
#      加性迁移会在进程启动时由 migrate.Run 自动应用（这是本仓库的既有约定：
#      「DB 保留加法迁移，不 down-migrate 删表」）。警告是为了让运维知道
#      「这次部署会顺带执行 N 个迁移」，而不是让它静默发生。
#
# 为什么必须**先部署能读懂新作业的 worker，再放开写入口**（T33）：
# 作业是做在 DB 里的（jobs/outbox），worker 认不出新 job_kind 会把消息
# 送进死信或反复失败。顺序反了会造成「用户看到已排队、其实没人能执行」。
# 本脚本只能检查 schema；作业种类兼容性由「worker 版本 ≥ 迁移版本」保证，
# 因此部署顺序是：迁移 → worker → API 写入口。
set -eu

if [ "$#" -lt 1 ] || [ -z "$1" ]; then
  echo "用法: $0 <postgres-dsn>" >&2
  exit 2
fi
DSN="$1"

if ! command -v psql >/dev/null 2>&1; then
  # 宿主机常常没有 psql（本仓库的 Go 门禁也走容器），此时给出一条可执行替代。
  echo "缺少 psql：请用容器执行，例如" >&2
  echo "  docker run --rm --network host -v \"\$PWD:/w\" -w /w \\" >&2
  echo "    -e DSN=\"\$DSN\" postgres:17-alpine sh /w/scripts/check-schema-compat.sh \"\$DSN\"" >&2
  exit 2
fi

psql_cmd() {
  psql -tA -v ON_ERROR_STOP=1 "$DSN" -c "$1"
}

# schema_migrations 不存在（全新库）时视为「空库」，两个方向都不匹配：
# 那是首次部署，迁移会在启动时全部应用。
has_table=$(psql_cmd "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'schema_migrations')" | tr -d '[:space:]')

applied_file=$(mktemp)
repo_file=$(mktemp)
trap 'rm -f "$applied_file" "$repo_file"' EXIT

if [ "$has_table" = "t" ]; then
  psql_cmd "SELECT filename FROM schema_migrations ORDER BY filename" >"$applied_file"
else
  : >"$applied_file"
  echo "[schema-compat] schema_migrations 不存在（全新库）：迁移将在启动时全部应用"
fi
# 用 ls+basename 而不是 find -printf：alpine 的 busybox find 不支持 -printf，
# 而本脚本要在 postgres:17-alpine 容器里直接跑（宿主机常常没有 psql）。
for file in sql/migrations/*.sql; do basename "$file"; done | sort >"$repo_file"

unknown=$(comm -13 "$repo_file" "$applied_file" || true)
missing=$(comm -23 "$repo_file" "$applied_file" || true)

applied_count=$(wc -l <"$applied_file" | tr -d ' ')
repo_count=$(wc -l <"$repo_file" | tr -d ' ')
echo "[schema-compat] 已应用 $applied_count 个，仓库有 $repo_count 个"

if [ -n "$unknown" ]; then
  echo "::error::数据库里有本代码不认识的迁移（库比代码新）："
  echo "$unknown" | sed 's/^/  /'
  echo "请部署与数据库匹配的版本，或先确认这些迁移的来源。"
  exit 1
fi

if [ -n "$missing" ]; then
  echo "[schema-compat] 警告：以下迁移尚未应用，启动时会被自动执行（必须都是加性迁移）："
  echo "$missing" | sed 's/^/  /'
fi

echo "[schema-compat] OK：库中没有比代码更新的迁移"
