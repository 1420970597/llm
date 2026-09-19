#!/usr/bin/env bash
# R4 / issue #9 迁移冒烟：证明 0020 能在「已存在重复活跃记录」的历史库上成功执行且幂等。
#
# 说明：契约 §6.1 为 R4 冻结的测试文件是 test/l15_worker_concurrency.py（已提供，
# 覆盖真实 HTTP 并发路径）。本脚本是**辅助证据脚本**，覆盖契约 §1.2 的另一条硬要求：
# 「迁移必须能在已存在重复 running 记录的历史库上成功执行 —— 先清理孤儿再建唯一索引」。
# 它不替代上面那个冻结文件名。
#
# 用法：bash test/l15_r4_migration_smoke.sh
# 需要 docker。用独立的临时 Postgres（端口 15434），不接触共享开发库。
set -euo pipefail

CONTAINER=l15-r4-mig-smoke
PORT=15434
PSQL=(docker exec -i "$CONTAINER" psql -v ON_ERROR_STOP=1 -q -U llm_factory -d llm_factory)

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

fail() { echo "❌ $*" >&2; exit 1; }

echo "==> 起临时 Postgres ($PORT)"
cleanup
docker run -d --rm --name "$CONTAINER" \
  -e POSTGRES_DB=llm_factory -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev \
  -p "$PORT:5432" postgres:17-alpine >/dev/null
for _ in $(seq 1 40); do
  docker exec "$CONTAINER" pg_isready -q -U llm_factory -d llm_factory >/dev/null 2>&1 && break
  sleep 1
done

echo "==> 应用 0001..0019（模拟历史库）"
for f in $(ls sql/migrations/*.sql | sort); do
  [[ "$f" == *0020* ]] && continue
  "${PSQL[@]}" -f - < "$f" >/dev/null
done

echo "==> 造与线上一致的脏数据：同一 (dataset_id, stage) 多条活跃记录（孤儿）"
"${PSQL[@]}" -c "INSERT INTO datasets (name, root_keyword, status) VALUES ('l15-r4-smoke-fixture','迁移冒烟','draft')" >/dev/null
"${PSQL[@]}" -c "
WITH d AS (SELECT id FROM datasets WHERE name='l15-r4-smoke-fixture')
INSERT INTO generation_runs (dataset_id, stage, status, total_units, done_units, cursor)
SELECT d.id, 'questions', 'running', 5, 2, '{\"a\":1}'::jsonb FROM d" >/dev/null
"${PSQL[@]}" -c "
WITH d AS (SELECT id FROM datasets WHERE name='l15-r4-smoke-fixture')
INSERT INTO generation_runs (dataset_id, stage, status, total_units, done_units, cursor)
SELECT d.id, 'questions', 'running', 5, 3, '{\"a\":1,\"b\":2}'::jsonb FROM d" >/dev/null
"${PSQL[@]}" -c "
WITH d AS (SELECT id FROM datasets WHERE name='l15-r4-smoke-fixture')
INSERT INTO generation_runs (dataset_id, stage, status, total_units)
SELECT d.id, 'questions', 'pending', 5 FROM d" >/dev/null

before=$(docker exec "$CONTAINER" psql -tA -U llm_factory -d llm_factory -c \
  "SELECT count(*) FROM generation_runs WHERE status IN ('pending','running')")
[[ "$before" == "3" ]] || fail "前置脏数据不正确：活跃记录应为 3，实际 $before"
echo "    迁移前活跃记录=$before（其中 2 条 running 是同阶段孤儿）"

echo "==> 应用 0020（第一次）"
"${PSQL[@]}" -f - < sql/migrations/0020_generation_runs_active_unique.sql

after=$(docker exec "$CONTAINER" psql -tA -U llm_factory -d llm_factory -c \
  "SELECT count(*) FROM generation_runs WHERE status IN ('pending','running')")
[[ "$after" == "1" ]] || fail "迁移后活跃记录应被清理为 1，实际 $after"
echo "    迁移后活跃记录=$after ✅"

echo "==> 断言：孤儿被终结但仍可追溯（不删除数据）"
terminated=$(docker exec "$CONTAINER" psql -tA -U llm_factory -d llm_factory -c \
  "SELECT count(*) FROM generation_runs WHERE status='failed' AND error_summary LIKE '并发 StartRun 产生的重复活跃记录%'")
[[ "$terminated" == "2" ]] || fail "应有 2 条记录被标记为并发孤儿，实际 $terminated"

echo "==> 断言：没有数据被删除"
total=$(docker exec "$CONTAINER" psql -tA -U llm_factory -d llm_factory -c \
  "SELECT count(*) FROM generation_runs")
[[ "$total" == "3" ]] || fail "迁移不得删除记录：期望 3，实际 $total"
echo "    总记录数=$total（未删除，仅终结状态）✅"

echo "==> 幂等：重复执行 0020 两次"
"${PSQL[@]}" -f - < sql/migrations/0020_generation_runs_active_unique.sql >/dev/null
"${PSQL[@]}" -f - < sql/migrations/0020_generation_runs_active_unique.sql >/dev/null
echo "    重复执行成功 ✅"

echo "==> 断言：数据库开始拒绝重复活跃记录"
if docker exec "$CONTAINER" psql -U llm_factory -d llm_factory -c \
  "INSERT INTO generation_runs (dataset_id, stage, status, total_units)
   SELECT id, 'questions', 'running', 1 FROM datasets WHERE name='l15-r4-smoke-fixture'" 2>/dev/null; then
  fail "重复活跃记录被接受 —— 唯一索引未生效"
fi
echo "    重复插入被拒绝 ✅"

echo "==> 断言：终态记录不受部分索引限制"
"${PSQL[@]}" -c "
WITH d AS (SELECT id FROM datasets WHERE name='l15-r4-smoke-fixture')
INSERT INTO generation_runs (dataset_id, stage, status, total_units)
SELECT d.id, 'questions', 'completed', 1 FROM d" >/dev/null
echo "    终态可继续追加（历史记录不被挡住）✅"

echo
echo "✅ 迁移冒烟全部通过：0020 在含重复活跃记录的历史库上可执行、幂等、不删数据、且真正建立数据库层约束"
