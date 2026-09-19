# 第二轮缺陷治理 Lane 看板（父代理维护）

契约：`docs/plans/issue-remediation-plan.md`（冻结）。本文件只记录**运行时状态**，不改契约。

## 0. 波次 0（父代理直接执行：基础设施修复，不占子代理编制）

| 项 | 结论 | 证据 |
|---|---|---|
| compose 入口 | `docker-compose.yml`(root, `name: llm`, `include` 子文件) 被本地栈使用 | `docker inspect llm-api-1 --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}'` → `/root/llm/docker-compose.yml` |
| compose 配置插值 | 已修复：删除子文件里覆盖 `env_file` 的 `environment:` 块 | PR #71 |
| CD `COMPOSE_FILE` | 原值 `deployments/compose/docker-compose.yml` 会解析 `DEPLOY_HEALTH_URL` 时读不到根 `.env` → 空串 → `curl ""` 必失败 | PR #71 |

## 1. Lane 看板

隔离路径（父代理预建，一 lane 一 worktree）：`/root/worktrees/llm-round2/r{N}`。
根因：本仓库 worktree 里没有 `node_modules`，且宿主机无 Go，因此不走托管 worktree 分配，
由父代理 `git worktree add -b <branch> <dir> origin/main` 预建并把 `node_modules` 符号链接进去，
子代理以显式 `cwd` 进入自己的 lane（`isolation` 默认 `none`）。契约要求的一写者一 worktree 不变。

| Lane | Issue | 分支 | worktree | 认领文件 | 候选容器端口 | 状态 | PR |
|---|---|---|---|---|---|---|---|
| R1 | #61 | `lane/r1-stage-routes` | `/root/worktrees/llm-round2/r1` | `apps/web-user/src/App.tsx`, `test/frontend_routes_test.go`, `test/l15_stage_routes.mjs` | — | running | — |
| R2 | #5 | `lane/r2-worker-batch-status` | `/root/worktrees/llm-round2/r2` | `apps/worker/main.go`, `internal/store/reasoning_store.go`, `internal/store/reward_store.go` | 18102 | running | — |
| R3 | #7 | `lane/r3-placeholder-content` | `/root/worktrees/llm-round2/r3` | `internal/llm/reasoning_generator.go`, `reward_generator.go`, `question_generator.go` | — | running | — |
| R4 | #9 | `lane/r4-generation-run-unique` | `/root/worktrees/llm-round2/r4` | `sql/migrations/0020_generation_runs_active_unique.sql`, `internal/store/generation_run_store.go`, `test/l15_worker_concurrency.py` | 18104 | running | — |
| R5 | #8 | `lane/r5-route-contract` | `/root/worktrees/llm-round2/r5` | `apps/api/routes.go`, `apps/api/main.go`, `docs/plans/eval-and-cleaning-plan.md`, `test/l15_route_contract.py` | 18105 | running | — |
| R6 | #63 | `lane/r6-admin-validation` | `/root/worktrees/llm-round2/r6` | `apps/api/main.go`, `internal/store/admin_store.go` | — | running | — |
| R7 | #58 | `lane/r7-dataset-provider-exists` | `/root/worktrees/llm-round2/r7` | `apps/api/datasets.go` | 18107 | running | — |
| R8 | #64 | `lane/r8-silent-guards` | `/root/worktrees/llm-round2/r8` | `apps/web-user/src/App.tsx`, `test/l15_silent_guards.mjs` | — | blocked by R1 | — |
| R9 | #10/#11 | `lane/r9-difficulty-buckets` | `/root/worktrees/llm-round2/r9` | `internal/llm/difficulty.go`, `internal/llm/question_generator_v2.go` | — | running | — |
| R10 | #65 | `lane/r10-capability-entries` | `/root/worktrees/llm-round2/r10` | `apps/web-user/src/**`, `test/l15_capability_entries.mjs` | 18110 | blocked by R8 | — |
| R11 | #37–#46, 0.5 | `lane/r11-issue-triage` | `/root/worktrees/llm-round2/r11` | `test/l15_issue_triage.py` | 18106 | running | — |
| R12 | — | `lane/r12-docs` | `/root/worktrees/llm-round2/r12` | `docs/architecture/**`, `README.md`, `docs/guides/**`, `docs/assets/**` | — | queued | — |

波次形态：`/root/pi-waves/wave1.js`（`workflowScriptPath`），8 阶段：
A = 无依赖写者（R3,R4,R5,R6,R7,R9,R11,R12）；B = R1,R2 写者；
C = R8,R10 写者 + 阶段 A 的 8 个评审者；D = R8,R10 评审者。
`globalConcurrencyLimit=7` 控制并发，避免 12 个 lane 同时 `docker build` 打爆宿主机。

评审者使用 `reviewer` agent + 显式 `cwd` 指向写者的 lane worktree，独立复跑门禁命令，
输出 `APPROVE` / `CHANGES-REQUIRED`，不修改文件。

### 串行约束

`App.tsx`：R1 → R8 → R10（R8/R10 的启动顺序已在 `wave1.js` 的阶段 B/C 中用顺序 `await` 强制）。

跨 worktree 的已知问题（父代理负责仲裁，已记录）：
三条 lane 的 worktree 都是在波次启动**之前**按同一个 `origin/main`(`3c8b772`) 建的，
因此 R8/R10 的**起点**不包含 R1 的提交。父子代理并未共享同一 checkout（一 lane 一 worktree），
所以在 stage C 启动 R8/R10 时无法用 “已合并的 main” 作为它们的基线。

处置办法（父代理在合并阶段执行，遵守 §2.2 合并顺序）：
1. 先合 R1（P0）→ `main` 前进；
2. 合 R8 前先 `git fetch && git rebase origin/main`，冲突就地解析（R1 改 `stageRouteNavMap` 与路由接线，
   R8 改 5 处守卫，区域基本不相交），然后**重跑 R8 全部门禁命令 + 它的 reviewer 复核**；
3. 合 R10 前同样 rebase 到含 R1+R8 的 `main`，重跑门禁与 reviewer 复核。

因此 R8/R10 的“已完成”以**父代理 rebase 后的复核**为准，而不是以 stage C/D 的首次输出为准。
这条差异不影响契约的接口/文件/测试名约定，只影响合并基线的建立时机。

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

## 4. PR 处置看板（3 open → 0 open）

| PR | 判定 | 证据 |
|---|---|---|
| #70 | **已合并**（squash → `a1bebf4`） | `git rev-list --count origin/main..origin/fix/61-restore-stage-routes` = 2；diff 非空（2 files, +233/-5）；三项 CI 全 pass；`mergeable=MERGEABLE, mergeStateStatus=CLEAN` |
| #4 | **已关闭** | checkout 后核验：`COMPOSE_FILE` 仍指向旧子文件路径（与 #71 冲突）；依赖非公开 GHCR API 且 `\|\| true` 吞错；`--no-build` 导致主机失去回滚兜底；`mergeable=CONFLICTING`；无 CI 记录 |
| #1 | **已关闭** | 2026-03 创建的陈旧分支；`mergeable=CONFLICTING`；空 PR body；改动 `apps/web-admin/**`（该目录在 `main` 上**已不存在**，PR 描述的「新版数据工厂控制台」实指已被并入 `apps/web-user`）；与冻结契约的 lane 划分无关联 |

## 5. 波次 0 交付的 PR

| PR | 内容 | 状态 |
|---|---|---|
| #70 | #61 阶段路由可达（PR #70 由父代理复核后合并） | merged `a1bebf4` |
| #71 | compose 根入口 + 删除覆盖 `env_file` 的 `environment` 块（修复 CI/CD/Makefile/脚本读不到根 `.env`） | merged `3c8b772` |
| #72 | 本看板文件 | pending |

### #66 的关单证据（父代理活体复现）

```
POST /api/v1/auth/login          -> 200
GET  /api/v1/datasets/999999999  -> 404 {"error":"请求的资源不存在"}
```

修复前为 500 + 英文 `internal server error`。已附证据关单。
