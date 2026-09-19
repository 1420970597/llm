# 第二轮缺陷治理 Lane 看板（父代理维护）

契约：`docs/plans/issue-remediation-plan.md`（冻结）。本文件只记录**运行时状态**，不改契约。

## 0. 波次 0（父代理直接执行：基础设施修复，不占子代理编制）

| 项 | 结论 | 证据 |
|---|---|---|
| compose 入口 | `docker-compose.yml`(root, `name: llm`, `include` 子文件) 被本地栈使用 | `docker inspect llm-api-1 --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}'` → `/root/llm/docker-compose.yml` |
| compose 配置插值 | 已修复：删除子文件里覆盖 `env_file` 的 `environment:` 块 | PR #71 |
| CD `COMPOSE_FILE` | 原值 `deployments/compose/docker-compose.yml` 会解析 `DEPLOY_HEALTH_URL` 时读不到根 `.env` → 空串 → `curl ""` 必失败 | PR #71 |

## 1. Lane 看板

| Lane | Issue | 分支 | worktree | 认领文件 | 状态 | PR |
|---|---|---|---|---|---|---|
| R1 | #61 | `lane/r1-stage-routes` | `.worktrees/r1` | `apps/web-user/src/App.tsx` | pending | — |
| R2 | #5 | `lane/r2-worker-batch-status` | `.worktrees/r2` | `apps/worker/main.go`, `internal/store/reasoning_store.go`, `internal/store/reward_store.go` | pending | — |
| R3 | #7 | `lane/r3-placeholder-content` | `.worktrees/r3` | `internal/llm/reasoning_generator.go`, `internal/llm/reward_generator.go`, `internal/llm/question_generator.go` | pending | — |
| R4 | #9 | `lane/r4-generation-run-unique` | `.worktrees/r4` | `sql/migrations/0020_*.sql`, `internal/store/generation_run_store.go` | pending | — |
| R5 | #8 | `lane/r5-route-contract` | `.worktrees/r5` | `apps/api/routes.go`, `apps/api/main.go`, `docs/plans/eval-and-cleaning-plan.md` | pending | — |
| R6 | #63 | `lane/r6-admin-validation` | `.worktrees/r6` | `apps/api/main.go`, `internal/store/admin_store.go` | pending | — |
| R7 | #58 | `lane/r7-dataset-provider-exists` | `.worktrees/r7` | `apps/api/datasets.go` | pending | — |
| R8 | #64 | `lane/r8-silent-guards` | `.worktrees/r8` | `apps/web-user/src/App.tsx` | pending | — |
| R9 | #10/#11 | `lane/r9-difficulty-buckets` | `.worktrees/r9` | `internal/llm/difficulty.go`, `internal/llm/question_generator_v2.go` | pending | — |
| R10 | #65 | `lane/r10-capability-entries` | `.worktrees/r10` | `apps/web-user/src/**` | pending | — |
| R11 | #37–#46, 0.5 | `lane/r11-issue-triage` | `.worktrees/r11` | `test/l15_issue_triage.py` | pending | — |
| R12 | — | `lane/r12-docs` | `.worktrees/r12` | `docs/architecture/**`, `README.md`, `docs/guides/**` | pending | — |

### 串行约束

`App.tsx`：R1 → R8 → R10（R8/R10 的 `blockedBy` 指向前序 lane 的 run key）。

### 合并顺序（父代理）

P0: R1, R2, R3, R4, R5 → P1: R6, R7, R8, R9, R10 → P2: R11, R12。
每条合并前：`git rebase origin/main` → `git push` **成功** → `gh pr merge --squash`。

## 2. 新增测试文件（契约 §6.1 冻结名）

| Lane | 文件 | 反污染前缀 |
|---|---|---|
| R1 | `test/l15_stage_routes.mjs` | 只读（无写库） |
| R2 | `test/l15_worker_batch_status.py` | `l15-r2-<pid>-` |
| R4 | `test/l15_worker_concurrency.py` | `l15-r4-<pid>-` |
| R5 | `test/l15_route_contract.py` | 只读（无写库） |
| R8 | `test/l15_silent_guards.mjs` | 只读（无写库） |
| R10 | `test/l15_capability_entries.mjs` | `l15-r10-<pid>-` |
| R11 | `test/l15_issue_triage.py` | `l15-r11-<pid>-` |

## 3. Issue 处置看板（21 open）

| Issue | 判定 | 依据 | 备注 |
|---|---|---|---|
| #5 | 修（R2） | 静态：`apps/worker/main.go:282/350` 在循环内 `Insert` | — |
| #7 | 修（R3） | 静态：`reasoning_generator.go` 无内容校验 | — |
| #8 | 修（R5） | 静态：`routeDatasetGet` 的 `default:` 静默 200 | — |
| #9 | 修（R4） | 静态：`0016` 索引非唯一 | — |
| #10 | 修（R9） | 静态：`bucket := (index*10)/total` | — |
| #11 | 修（R9） | 同上 | — |
| #37–#46 | 取证（R11） | 自动审查 issue | #45 已关；#58 由 R7 修 |
| #58 | 修（R7） | 静态：`datasets.go` 不校验 providerId | — |
| #61 | 修（R1，PR #70 已开） | 静态：`App.tsx:3647-3651` 自指重定向 | 复核 PR #70 |
| #63 | 修（R6） | 静态：`main.go:189/213/237/261` 零校验 | — |
| #64 | 修（R8） | 静态：`App.tsx:2557/2760` 短路 | — |
| #65 | 修（R10） | 静态：`lib/api.ts` 存在未调用方法 | — |
| #66 | 已修（PR #68） | `writeError` 集中降级 | 待活体复现后关单 |

## 4. PR 处置看板（3 open）

| PR | 判定 | 依据 |
|---|---|---|
| #70 | 合并（首批） | 与 R1 同目标，`mergeable=MERGEABLE`，含 `test/frontend_routes_test.go` 回归守卫 |
| #4 | 关闭 | check out 后核验：与冻结契约冲突 / 无 CI 证据 |
| #1 | 关闭 | 2026-03 陈旧分支、`mergeable=CONFLICTING`、空 PR body、无 issue 关联 |
