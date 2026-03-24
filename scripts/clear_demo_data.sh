#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U llm_factory -d llm_factory \
  -c "TRUNCATE TABLE artifacts, reward_records, reasoning_records, questions, domain_edges, domains, dataset_runs, datasets, audit_logs, prompt_templates, generation_strategies, storage_profiles, model_providers RESTART IDENTITY CASCADE;"

echo "已清空预置/验收产生的业务假数据，用户账号保留。"
