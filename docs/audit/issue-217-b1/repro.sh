#!/usr/bin/env bash
# issue #217 B1 复现/验证：`db-migrate-smoke` 是否真的应用**全部**迁移。
#
# 缺陷形态（实测）：旧 Makefile target 只执行 sql/migrations/0001_phase1_foundation.sql
# （3 张表），而 AGENTS.md 把它描述为「验证 SQL 迁移脚本」—— 0022–0039 零覆盖，
# 却永远绿灯。判定口径（机器事实）：迁移后核心表是否存在
# （samples / sample_versions / batches / review_projections / legacy_imports）。
#
# 用法：
#   bash docs/audit/issue-217-b1/repro.sh before   # 复刻旧 target 的行为
#   bash docs/audit/issue-217-b1/repro.sh after    # 运行新 target
# 产物：docs/audit/issue-217-b1/<phase>.json
#
# 防覆盖：已存在的证据默认不覆盖（与 issue-200/205/212/213 的同一约定）。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

PHASE="${1:?用法: repro.sh before|after}"
OUT_DIR="docs/audit/issue-217-b1"
OUT_FILE="$OUT_DIR/${PHASE}.json"

if [ -e "$OUT_FILE" ] && [ "${FORCE:-0}" != "1" ]; then
  echo "[issue-217-b1] 拒绝覆盖已存在的证据：$OUT_FILE" >&2
  echo "  要重采请设 FORCE=1（注意：在已修复的仓库上采 before 会得到修复后的读数）。" >&2
  exit 2
fi
mkdir -p "$OUT_DIR"

PORT="${DB_MIGRATE_SMOKE_PORT:-15435}"
CONTAINER="llm-217b1-repro"
IMAGE="${POSTGRES_IMAGE:-postgres:17-alpine}"
DB_NAME=llm_factory
DB_USER=llm_factory
DB_PASS=llm_factory_dev

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "[issue-217-b1] phase=$PHASE 起临时 Postgres（端口 $PORT）"
cleanup
docker run -d --rm --name "$CONTAINER" \
  -e "POSTGRES_DB=$DB_NAME" -e "POSTGRES_USER=$DB_USER" -e "POSTGRES_PASSWORD=$DB_PASS" \
  -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null

ready=0
for _ in $(seq 1 60); do
  ready_lines=$(docker logs "$CONTAINER" 2>&1 | grep -c 'database system is ready to accept connections' || true)
  if [ "$ready_lines" -ge 2 ] \
     && docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -tAc 'SELECT 1' >/dev/null 2>&1; then
    sleep 1
    if docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -tAc 'SELECT 1' >/dev/null 2>&1; then
      ready=1; break
    fi
  fi
  sleep 1
done
[ "$ready" = "1" ] || { echo "[issue-217-b1] Postgres 未就绪" >&2; exit 1; }

if [ "$PHASE" = "before" ]; then
  # 复刻旧 target 的行为：只应用第一个迁移。
  APPLIED=(sql/migrations/0001_phase1_foundation.sql)
  echo "[issue-217-b1] before：复刻旧 target（只应用 0001）"
else
  # 新 target 的行为：按文件名顺序应用全部迁移。
  APPLIED=(sql/migrations/*.sql)
  echo "[issue-217-b1] after：应用全部迁移（${#APPLIED[@]} 份）"
fi

for file in "${APPLIED[@]}"; do
  docker exec -i "$CONTAINER" psql -h 127.0.0.1 -v ON_ERROR_STOP=1 -q \
    -U "$DB_USER" -d "$DB_NAME" <"$file" >/dev/null 2>&1
done

table_count=$(docker exec "$CONTAINER" psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")

python3 - "$PHASE" "$table_count" "$CONTAINER" "$OUT_FILE" "${APPLIED[@]}" <<'PY'
import json, subprocess, sys
phase, table_count, container, out_file = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
applied = sys.argv[5:]
core = ['datasets', 'samples', 'sample_versions', 'batches', 'review_projections', 'legacy_imports']
presence = {}
for table in core:
    out = subprocess.run(
        ['docker', 'exec', container, 'psql', '-h', '127.0.0.1', '-U', 'llm_factory', '-d', 'llm_factory', '-tAc',
         f"SELECT to_regclass('public.{table}') IS NOT NULL"],
        capture_output=True, text=True, check=True).stdout.strip()
    presence[table] = out == 't'
missing = [t for t in core if not presence[t]]
report = {
    'issue': 217,
    'item': 'B1',
    'phase': phase,
    'appliedMigrations': len(applied),
    'appliedFirst': applied[0],
    'publicTableCount': table_count,
    'coreTables': presence,
    'missingCoreTables': missing,
    'allCoreTablesPresent': len(missing) == 0,
    'ok': len(missing) == 0,
}
open(out_file, 'w').write(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(f"[issue-217-b1] phase={phase} 应用迁移={len(applied)} 表数={table_count} 缺失核心表={missing or '无'}")
print(f"[issue-217-b1] 判定: {'FIXED' if report['ok'] else 'STILL_BROKEN'}")
sys.exit(0 if report['ok'] else 1)
PY
