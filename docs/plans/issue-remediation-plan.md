# 缺陷治理 Lane 计划与冻结契约（第二轮）

> 本文件是**第二轮（缺陷治理）**的唯一权威契约。
> 第一轮（功能开发，L1–L15）的契约见 `docs/plans/eval-and-cleaning-plan.md`，该文件已冻结，本轮不修改它。
> 本文件冻结后，所有子代理以它为准；需要变更必须先经父代理更新本文件。

## 0. 背景与判定

第一轮 15 条 lane 已全部合并，7 项需求验收 73/73 通过（真实 LLM，容器 `:18100`）。随后拉取远程 issue 并逐条对 `main` 复核，结论如下。

### 0.1 对 `main` 复核后确认仍然存在的缺陷

| Issue | 严重度 | 复核证据（main 上） |
|---|---|---|
| #61 | **P0 阻塞** | `apps/web-user/src/App.tsx:3647-3651` 五个阶段路由全是 `<Navigate to={activeTaskDetailRoute}>`，而 `activeTaskDetailRoute`（`App.tsx:937`）在 `activeDataset` 存在时正是 `/console/tasks/{id}` —— 自指重定向。`renderDomains` 全文件仅 1 处（定义，`App.tsx:2521`），无任何路由引用。「生成方向结构」按钮（`App.tsx:2558`）不可达，流水线在 `draft` 硬阻塞。 |
| #5 | **P0** | `apps/worker/main.go:282` 与 `:350` 把 `reasoningStore.Insert` / `rewardStore.Insert` 放在 `for _, question := range questions` 循环内；而 `internal/store/reasoning_store.go:24-26` 的 `Insert` 语义是「整批完成 + 推进数据集状态」。第一次迭代即写死终态，`failedCount/partialCount` 只看当次迭代。 |
| #7 | **P0** | `internal/llm/reasoning_generator.go:82-93` 只在 JSON 解析失败时走 fallback，`unmarshalStructuredContent` 成功后**无任何内容校验** → `reasoning="..."` 被判 `status="generated"`，占位数据进入训练集。 |
| #9 | **P0** | `sql/migrations/0016_generation_runs.sql` 只有非唯一索引 `idx_generation_runs_lookup (dataset_id, stage, status)`；`internal/store/generation_run_store.go:46-66` 的 `StartRun` 是 `SELECT`→`INSERT` 无锁无 `ON CONFLICT`，并发产生孤儿 `running` 记录；`ActiveRun` 用 `ORDER BY id DESC LIMIT 1`，孤儿记录永久不被读取/结束。 |
| #8 | **P1** | `docs/plans/eval-and-cleaning-plan.md:72-73` 冻结了 `RegisterDatasetAction` / `RegisterDatasetGet`，实现是 `RegisterDatasetRouter`（`apps/api/routes.go:45`），契约符号全仓库不存在。更危险的是 `apps/api/main.go:339-362` 的 `routeDatasetGet` `default:` 把**任意未知子路径**静默交给 `getDataset`（返回 200），漏注册端点不会报错。 |
| #63 | **P1** | `apps/api/main.go:189/213/237/261` 四个 upsert（provider/storage/strategy/prompt）**零校验**：空 body 直接落库并返回 200。实测可写出 `domainCount:1000` + `isDefault:true` 的脏策略。 |
| #58 | **P1** | `apps/api/datasets.go:30-50` 的 `createDataset` 不校验 `ProviderID` 是否存在，创建后到生成阶段才失败。 |
| #64 | **P1** | `apps/web-user/src/App.tsx:2557`、`:2760` 用 `activeDatasetId && void loadDatasetWorkspace(...)` 短路，未选中任务时点「刷新结构」「刷新结果」**无请求、无提示**。 |
| #10 / #11 | **P1** | `internal/llm/difficulty.go:31-42` 的 `DifficultyAssigner` 用 `bucket := (index*10)/total`；`total=2` 时 bucket∈{0,5} → 只出 easy/medium，`hard` 恒为 0（生产数据 `dataset 6` 已复现）。`DifficultyFromMix` 的实现按 `position=index/total` 落桶，`total=2, mix={easy:0.3,medium:0.5,hard:0.2}` 时 position∈{0,0.5} → 同样不出 hard。 |
| #65 | **P1** | 后端 L2–L6/R1 能力已实现，但 UI 无入口；`apps/web-user/src/lib/api.ts` 存在未被任何视图调用的方法。 |
| #37–#46 | **P2** | 自动审查 issue，均在验收脚本通过前提出；需逐条以当前 `main` 重新取证后关单，不得无证据关闭。 |

### 0.2 复核后确认已被第一轮修复（不重复开工）

- **#62** 已由 PR #67 修复并关闭（裁判候选按数据集计算）。
- **#66** 已由 PR #68 修复（`writeError` 集中把 `pgx.ErrNoRows` 降级 404）。
- **#11 的一半**：`DifficultyFromMix` / `NormalizeDifficultyMix` 已不再是「零调用」—— `internal/llm/question_generator_v2.go:67` 通过 `AllocateDifficultyMix` 落地了配比。剩余问题是**小 total 下的丢档算术**（见上表 #10/#11 行），故 #10/#11 合并为一条 lane 处理，只修算术与配比落地缺口。

### 0.3 本轮的取证方法（可复现）

本节所有结论都来自**当前 `main`（`5aada8e`）**，且已完成静态取证 + 活体复现双证据：

```bash
# 静态：定位代码位置
grep -n "Navigate to={activeTaskDetailRoute}" apps/web-user/src/App.tsx   # 3647-3651
wc -l internal/llm/reasoning_generator.go                                  # 97 行
ls sql/migrations/ | tail -2                                               # 0018, 0019（无 0020）

# 活体：打 main 重建的验收容器（真实 LLM + 真实 Postgres）
curl -s -o /tmp/ck -c /tmp/ck -X POST :18100/api/v1/auth/login \
  -d '{"email":"admin@company.com","password":"admin123456"}'
curl -s -b /tmp/ck -X POST :18100/api/v1/admin/providers -d '{}'          # -> 200 + 空行
curl -s -b /tmp/ck -X POST :18100/api/v1/datasets \
  -d '{"name":"probe","providerId":999999,...}'                        # -> 201

docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select indexname,indexdef from pg_indexes where tablename='generation_runs'"
  # -> 只有 pkey 与 idx_generation_runs_lookup(dataset_id,stage,status)，非唯一
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select status,count(*) from generation_runs group by status"
  # -> running=11（孤儿）、completed=81、partial_failed=2、failed=1
```

> 数据库连接（写死，便于子代理复用）：容器 `llm-postgres-1`，用户 `llm_factory`，库 `llm_factory`。
> **不要**用 `-U postgres` 或 `-U llm`，本部署不存在这两个角色。

### 0.4 两个需要修正的既有认知

1. **`#61` 曾被尝试修复但工作全部丢失**：本地分支 `fix/task-stage-pages-reachable` 相对 `origin/main` 的 diff **为空**（`git log origin/main..fix/task-stage-pages-reachable` 无输出），与 MEMORY.md 记录的『rebase 后 push 被拒仍 merge』是同一失败模式。因此 #61 **仍然成立**，必须重做，且合并前必须确认 push 成功。
2. **`#5` 的机制不是「漏写」而是「共享入口自带状态推进」**：
   `internal/store/reasoning_store.go:22-24` 的 `Insert` → `upsert(..., markGenerated=true)`，
   而 `upsert` 末尾（`reasoning_store.go:73-84`）无条件执行
   `UPDATE datasets SET status = $2 ...`，把 `failedCount/partialCount/generatedCount`
   按**本次传入的这批记录**推算后写死数据集终态。
   `apps/worker/main.go:282/350` 在**逐题循环内**调用 `Insert`，故第 1 题写完即推进状态，
   `partial` 更会被 `default:` 分支计入 `generatedCount`（`reasoning_store.go:48-54`）。

### 0.5 新增待取证疑点（不预先断言，交给 R11 出证据）

`llm-postgres-1` 中存在 **`status='export_generated'` / `'directions_completed'` 但 `reasoning_records` 与 `reward_records` 均为 0 条**的数据集（id 266–271）。
状态机不该允许这种不一致，但当前无法区分是「worker 缺陷造成」还是「验收脚本清理导致」。
因此**不作为独立 lane**，而是：
- 并入 **R2** 的验收断言（批处理结束后 `status` 不得领先于实际记录数）；
- 由 **R11** 单独出证据后决定是并入 R2 还是单开 lane。

## 1. 冻结的跨 lane 接口契约

### 1.1 本轮**不新增**任何 HTTP 路由

所有修复都落在已有端点上。因此 HTTP 路径、请求/响应 JSON 形状**保持不变**，前端既有 `apps/web-user/src/lib/api.ts` 函数签名不变。

唯一的响应语义变更是**错误路径**（这是修复目标本身）：

| 端点 | 变更前 | 变更后 |
|---|---|---|
| `GET /api/v1/datasets/{id}/<未知子路径>` | 200 + 数据集图（静默） | **404** `{"error":"未找到该子资源"}` |
| `POST /api/v1/datasets/{id}/<未知子路径>` | 200 + 数据集图（静默） | **404** `{"error":"未找到该子资源"}` |
| `GET /api/v1/datasets/{id}`（精确） | 200 数据集图 | 200 数据集图（不变） |
| `POST/PUT /api/v1/admin/{providers,storage-profiles,generation-strategies,prompts}` 空 body | 200 + 脏数据 | **400** `{"error":"<字段名> 必填"}` |
| `POST /api/v1/datasets` 带不存在的 `providerId` | 201 后到生成阶段才失败 | **400** `{"error":"指定的 AI 服务不存在"}` |

`writeError` 的既有约定（PR #68）继续生效：4xx 原样透出中文业务提示；5xx 不泄漏内部详情。

### 1.2 数据库迁移编号区间

**本轮只允许一条新迁移：`sql/migrations/0020_generation_runs_active_unique.sql`**（对应 #9）。

- 现有迁移 0001–0019 全部冻结，**任何 lane 不得修改**。
- 0021+ 预留，本轮的其他 lane 不得占用。
- 迁移必须幂等（`IF NOT EXISTS` / `ON CONFLICT DO NOTHING`），且必须能在已存在重复 `running` 记录的历史库上成功执行 —— 因此**先清理孤儿再建唯一索引**，清理语句必须明确写在迁移里并带注释。

### 1.3 共享类型与函数签名冻结

本轮**不新增** `internal/model/*.go` 类型。允许的内部签名变更（仅限下述，其他一律不改）：

```go
// internal/llm/reasoning_generator.go —— #7
// 新增内容有效性判定，不改动 GenerateReasoning 的对外签名。
// 不合格记录返回 status = "invalid"（新状态值，见下），不返回 "generated"。
const ReasoningStatusInvalid = "invalid"

// internal/store/reasoning_store.go —— #5
// Insert 语义保持「整批完成 + 推进状态」不变，调用点从循环内移到循环外。
// 新增：允许调用方显式传入整批统计，避免 Insert 从当批推断。
// 不新增导出函数，仅调整 apps/worker 调用方式。

// internal/llm/difficulty.go —— #10/#11
// 签名不变，只修实现：保证 total >= 档位数时每档至少 1 条。
func DifficultyAssigner(index, total int) string        // 签名不变
func DifficultyFromMix(mix map[string]float64, index, total int) string // 签名不变
```

**新状态值 `invalid` 的落库约定**（#7）：`reasoning_records.status` 与 `reward_records.status` 允许值扩展为
`generated | failed | invalid`。`invalid` 表示「模型返回了合法 JSON 但是占位/无效内容」。
下游过滤规则：**只有 `generated` 可进入导出与评估**；`invalid` 与 `failed` 同等看待，但分开计数以便区分「网络失败」与「模型摆烂」。

该扩展**不需要新迁移**（`status` 是 `TEXT`，无 CHECK 约束）—— lane 必须自行核实这一点并在测试中证明。

### 1.4 前端契约

- 不新增路由段；**修复** `App.tsx:3647-3651` 的五个阶段路由，使其渲染各自页面而非重定向。
- `stageRouteNavMap`（`App.tsx:104-110`）与 `taskWorkbenchPages`/`resultWorkbenchPages` 若与修复后的路由不一致，必须一并修正。
- `#64` 的静默守卫统一改为：未选中任务时给出明确提示（toast），不静默 return。全文 5 处同类守卫必须行为一致。
- `#65` 新增的 UI 入口所用 API 方法**必须已存在于 `lib/api.ts`**；确属无需暴露的方法删除，不留死代码。

## 2. Lane 划分（每条 lane = 写者 → 自测 → 独立 reviewer → 修）

每条 lane 的写者在**独立 worktree** 内提交；reviewer 为 fresh 只读上下文；父代理是唯一合并者。

| Lane | Issue | 写者改动范围 | 必须留下的可运行验证 |
|---|---|---|---|
| **R1** | #61 | `apps/web-user/src/App.tsx` | `npm run build -w apps/web-user` 通过；`test/l15_stage_routes.mjs`：登录后逐个访问 5 个阶段路由，断言 URL 未被重定向且目标 DOM 存在 |
| **R2** | #5 | `apps/worker/main.go`；若需调整 `internal/store/reasoning_store.go` / `reward_store.go` 的 `Insert` 语义须在 PR 里说明 | Go 测试断言 `Insert`（或等价批处理入口）每阶段只调用一次；`test/l15_worker_batch_status.py`：真实数据集跑答案+评分，断言全部记录落库后 `datasets.status` 才推进，且**结束后 `status` 不领先于实际记录数**（覆盖 0.5 的疑点） |
| **R3** | #7 | `internal/llm/reasoning_generator.go`、`reward_generator.go`、`question_generator.go` | Go 表驱动测试：`reasoning="..."` / `""` / `"N/A"` / 与 answer 雷同 → `invalid`；正常长文本 → `generated` |
| **R4** | #9 | `sql/migrations/0020_*.sql`、`internal/store/generation_run_store.go` | 迁移 smoke（含 11 条既有 `running` 孤儿的历史库可执行）；Go 测试 + 并发集成测试：N 个 goroutine 并发 `StartRun` 只产生 1 条活跃记录 |
| **R5** | #8 | `apps/api/routes.go`、`apps/api/main.go`（`routeDatasetGet`/`routeDatasetActions` default）、`docs/plans/eval-and-cleaning-plan.md` | Go 测试：未知子路径 → 404，精确路径 → 200；文档与实现符号一致（新增契约一致性测试） |
| **R6** | #63 | `apps/api/main.go`（4 个 upsert）、`internal/store/admin_store.go` | Go 表驱动测试：4 个端点的空 body / 缺必填字段 → 400 + 字段名；合法 body → 200 |
| **R7** | #58 | `apps/api/datasets.go` | Go 测试 + `test/`：`providerId` 不存在 → 400；存在 → 201 |
| **R8** | #64 | `apps/web-user/src/App.tsx` | `test/l15_silent_guards.mjs`：未选中任务时点击 5 处守卫，断言出现提示文案且无静默 return |
| **R9** | #10/#11 | `internal/llm/difficulty.go`、`question_generator_v2.go` | Go 表驱动测试：`total=2` 且 mix 含 hard 时 hard 数 ≥1；`total=3/4` 每档 ≥1；配比总数守恒 |
| **R10** | #65 | `apps/web-user/src/**`（新视图 + 路由接线 + `lib/api.ts` 清理） | `npm run build` 通过；`test/l15_capability_entries.mjs`：L2/L3/L4/L5/L6/R1 能力各有可达入口并触发真实请求 |
| **R11** | #37–#44 + 0.5 疑点 | `test/`（取证脚本）、无产品代码改动 | 逐条 issue 的判定脚本；结论只有两种：**有当前证据 → 关单**，或**无证据 → 保持开启并说明缺口**。（#45 已关闭；#46 属导出真实性，与 R11 的 #40/#41/#42 取证合并；#58 由 R7 修） |
| **R12** | — | `docs/architecture/**`、`README`/使用说明 | 文档构建/链接检查；中文图文并茂；覆盖本轮修复 |

### 2.1 串行约束（避免同文件并发写冲突）

`App.tsx` 被 R1、R8、R10 共同涉及，**必须串行**：

```
R1 → R8 → R10        （blockedBy 链）
```

其余 backend lane（R2–R7、R9）文件互不重叠，可并行。R11、R12 与全部 lane 无文件重叠，可并行。

### 2.2 合并顺序（父代理执行）

1. 先合 P0：R1、R2、R3、R4、R5。
2. 再合 P1：R6、R7、R8、R9、R10。
3. 最后合 P2：R11、R12。
4. 每次合并前：`git rebase origin/main` → **`git push` 成功后** → `gh pr merge --squash`。
   （push 失败时绝不 merge —— 见 MEMORY.md 的 rebase 教训。）

## 3. 硬性约束（沿用第一轮，违反即回滚）

1. **一个 worktree 一个写者**，写者必须 `isolation: "worktree"`；reviewer 用 fresh 只读上下文。
2. 父代理是唯一合并者；子代理只在自己的 worktree 内提交。
3. `main` 禁止直推，一律特性分支 + PR。
4. 一次提交一件事，Conventional Commits **中文**描述。
5. **禁止最小 MVP、禁止假数据**。真实 LLM 调用需要 `provider.APIKey`/`BaseURL`；缺 key 的测试标记「输入缺失」并明确报告，**不得伪造通过**。
6. Go 验证统一走：
   ```
   docker run --rm -v $PWD:/w -w /w golang:1.24-alpine \
     sh -c "gofmt -l apps internal && go vet ./... && go build ./... && go test ./..."
   ```
   （注意：不要用 `sh -lc`，alpine login shell 会丢 PATH。）
7. 接口测试用 python 放 `test/`，写清测试项。
8. 端口冲突只改本项目映射端口（web-user 3210），**绝不停止其他业务容器**。
9. 完成一个功能必须开 PR 合并；每次 commit 后拉远程 issue 并处理。

## 6. 测试文件与产物位置契约

### 6.1 新增测试文件命名

`test/` 目录现有 L1–L14 命名（`test_l1_directions.py` … `test_l14_cleaning_ui.py`）。本轮沿用 `l15_` 前缀作为「第二轮」命名空间：

| Lane | 必须新建的文件 |
|---|---|
| R1 | `test/l15_stage_routes.mjs` |
| R2 | `test/l15_worker_batch_status.py` |
| R4 | `test/l15_worker_concurrency.py` |
| R5 | `test/l15_route_contract.py` |
| R8 | `test/l15_silent_guards.mjs` |
| R10 | `test/l15_capability_entries.mjs` |
| R11 | `test/l15_issue_triage.py` |

Go 测试直接与源码同包（`apps/api/write_error_test.go` 已是此惯例），文件名 `*_test.go` 放对应包目录。

> 文件由各自 lane 创建，父代理不预建空文件（避免占位实现被误认为验证证据）。
> 名字已冻结，lane **不得改用其他文件名**，否则验收无从定位。

### 6.2 测试产出的数据必须可清理（硬约束）

现有 `test_acceptance_7requirements.py` 会遗留 `status='draft'` 的空数据集，验收后又留下 `export_generated` 但零记录的数据集（见 0.5 的疑点）。**本轮所有新测试必须自带反污染**：

1. 每个写测试用**唯一名字前缀**（如 `l15-r2-<pid>-`），不要用 `验收-` 这类重复名。
2. 测试结束（含失败路径 `finally`）清理自己创建的行，至少覆盖 `datasets` 及其级联子表。
3. 清理使用**精确条件**（按 id 或唯一名字），**禁止**无 `WHERE` 的批量删除。
4. 若测试需保留数据作为证据，必须在输出里写明保留的 id，便于后续清理。
5. 数据库访问统一用 `docker exec llm-postgres-1 psql -U llm_factory -d llm_factory`（**不是** `postgres`/`llm` 角色）。

这条约束的理由：`llm-postgres-1` 是共享开发库，被污染的脏数据会让后续 lane 的断言假失败，也会掩盖真实缺陷。

## 7. 子代理预算

- 写者 12（R1–R12）
- reviewer 12（每条 lane 一个，fresh 只读）
- 横切复核 2（契约一致性、安全与数据完整性）
- **合计 26 个子代理**（≥ 20）

## 5. 交付物

1. 12 条 lane 全部合并到 `main`。
2. 全量 CI 等价命令通过（含新增测试）。
3. 逐条核对 `功能说明.txt` 的 7 项需求 + 本轮 issue 清单，输出验收报告（需求 → 实现位置 → 测试证据 → PR 号）。
4. 后续功能计划。
