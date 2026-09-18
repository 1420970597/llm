# 长链思考数据工厂 · 评估与清洗能力建设方案（契约冻结版）

> 本文档是 **唯一权威契约**。所有 lane 子代理必须以本文档为准，不得自行改动接口、表结构、字段名。
> 冻结版本：`v1.0`（对应迁移 `0011` ~ `0019`）
> 对应需求：`功能说明.txt` 全 7 项；`todo.md` 的 TO C 信息架构（第 3 节、第 12 节）。

---

## 0. 需求 → 交付映射

| # | 功能说明.txt 需求 | 现状 | 本方案 lane |
|---|---|---|---|
| R1 | 关键词 → n 个领域 → m 个方向（n/m 用户可控） | 只有一层 `domains`，`domain_count` 可控，无 m、无断点续跑 | L1 |
| R2 | 每个方向生成长链思维标准步骤 | **完全缺失** | L2 |
| R3 | 每个方向生成 x 个具体问题（x 可控） | 有 `questions` 表与生成器，但按 domain 而非 direction、无难度分层、无去重 | L3 |
| R4 | GRPO 分支：按打分档次自动生成教师模型评判提示词 | **完全缺失** | L4 |
| R5 | SFT 分支：生成思维链 + 答案 | 有 `reasoning_records`（单字段 `reasoning`），无 SFT 结构 | L5 |
| R6 | 多格式导出（字段映射可配） | 只有 JSONL 单格式 | L6 |
| R7 | 数据集评估：多 LLM 互评 / ≥50 维度 / 全量+抽样 / 剔除自评 / 汇总统计可视化 | **完全缺失** | L7 L8 L9 L10 |
| R8 | 数据清洗：拒答关键词库 + 问题/思维链/答案各步骤拦截 + 报告 | **完全缺失** | L11 L12 |
| R9 | 前端评估 UI / 清洗 UI + TO C 信息架构 | 有 TO C 导航雏形（`/console/*`） | L13 L14 |
| R10 | 中文图文并茂架构文档 + 使用说明 | 有 phase-1~7 文档 | L15 |

---

## 1. 并发写入架构（冲突规避设计）

**核心问题**：15 个写者并行改同一个 Go 模块，必然产生编译冲突与 merge 冲突。

**解决方案**：父代理在 foundation 阶段一次性冻结所有「共享文件」，并引入 **注册表（registry）机制**。此后：

> **每个 lane 只允许新增文件，禁止修改任何已存在的共享文件。**

```
┌─────────────────────────────────────────────────────────────┐
│  foundation（父代理独占，已冻结）                              │
│  ┌───────────────┐ ┌──────────────┐ ┌────────────────────┐  │
│  │ sql/0011-0019 │ │ model/*.go   │ │ store/*_store.go   │  │
│  │ 迁移（全表）   │ │ 类型（全量）  │ │ 构造器（全量）      │  │
│  └───────────────┘ └──────────────┘ └────────────────────┘  │
│  ┌───────────────────────────┐ ┌──────────────────────────┐ │
│  │ apps/api/routes.go        │ │ apps/worker/registry.go  │ │
│  │ 路由注册表 + 数据集动作表   │ │ job handler 注册表        │ │
│  └───────────────────────────┘ └──────────────────────────┘ │
│  ┌───────────────────────────┐ ┌──────────────────────────┐ │
│  │ web-user/src/lib/api.ts   │ │ App.tsx 导航/路由（唯一） │ │
│  │ 全部新 API 函数签名        │ │ + 占位视图组件            │ │
│  └───────────────────────────┘ └──────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        ▼                     ▼                     ▼
   ┌─────────┐           ┌─────────┐           ┌─────────┐
   │ lane L1 │           │ lane L7 │           │ lane L11│
   │ worktree│           │ worktree│           │ worktree│
   │ 只加文件 │           │ 只加文件 │           │ 只加文件 │
   └─────────┘           └─────────┘           └─────────┘
```

### 1.1 注册表接口（冻结）

**后端路由** — `apps/api/routes.go`

```go
// 顶层路由注册（/api/v1/eval/**、/api/v1/cleaning/** 等新前缀）
type routeRegistrar func(mux *http.ServeMux, app *application)

func RegisterRoutes(r routeRegistrar)          // lane 在 init() 中调用

// 数据集子动作注册（POST /api/v1/datasets/{id}/<suffix>）
// 优先级高于 main.go 内置 legacy 分支
func RegisterDatasetAction(suffix string, fn datasetActionFunc)
func RegisterDatasetGet(suffix string, fn datasetActionFunc)

type datasetActionFunc func(w http.ResponseWriter, r *http.Request, id int64)
```

**Worker job** — `apps/worker/registry.go`

```go
type jobHandler func(ctx context.Context, jc *jobContext, job jobPayload) error

func RegisterJobHandler(jobType string, h jobHandler)   // lane 在 init() 中调用
// 查表优先于 legacy switch；未注册的 jobType 走 legacy 分支
```

### 1.2 lane 文件归属表（禁止越界）

| lane | 独占新增文件 | 禁止修改 |
|---|---|---|
| L1 | `internal/llm/direction_generator.go`、`internal/store/dataset_store_directions.go`、`internal/store/generation_run_store.go`、`apps/api/routes_directions.go`、`apps/worker/job_directions.go` | 所有 `_store.go` 既有文件、`main.go` |
| L2 | `internal/llm/chain_standard_generator.go`、`internal/store/chain_standard_store.go`、`apps/api/routes_chain_standards.go`、`apps/worker/job_chain_standards.go` | 同上 |
| L3 | `internal/llm/question_generator_v2.go`、`internal/llm/dedupe.go`、`internal/store/question_store_v2.go`、`apps/api/routes_questions_v2.go`、`apps/worker/job_questions_v2.go` | 同上 |
| L4 | `internal/llm/grpo_prompt_generator.go`、`internal/store/grpo_store.go`、`apps/api/routes_grpo.go`、`apps/worker/job_grpo.go` | 同上 |
| L5 | `internal/llm/sft_generator.go`、`internal/store/sft_store.go`、`apps/api/routes_sft.go`、`apps/worker/job_sft.go` | 同上 |
| L6 | `internal/exporter/*.go`、`internal/store/export_mapping_store.go`、`apps/api/routes_export_formats.go`、`apps/worker/job_export_multi.go` | `apps/api/exports.go` |
| L7 | `internal/eval/judge.go`、`internal/store/eval_store_judges.go`、`apps/api/routes_eval_judges.go` | `internal/eval/catalog.go`、`internal/eval/scoring.go` |
| L8 | `internal/eval/catalog.go`、`internal/store/eval_store_dimensions.go`、`apps/api/routes_eval_dimensions.go` | `internal/eval/judge.go` |
| L9 | `internal/eval/scoring.go`、`internal/eval/sampling.go`、`internal/store/eval_store_items.go`、`apps/api/routes_eval_runs.go`、`apps/worker/job_eval.go` | 同上 |
| L10 | `internal/eval/aggregate.go`、`internal/eval/report.go`、`internal/store/eval_store_summary.go`、`apps/api/routes_eval_report.go` | 同上 |
| L11 | `internal/cleaning/keywords.go`、`internal/store/cleaning_store_keywords.go`、`apps/api/routes_cleaning_keywords.go` | `internal/cleaning/scanner.go` |
| L12 | `internal/cleaning/scanner.go`、`internal/store/cleaning_store_runs.go`、`apps/api/routes_cleaning_runs.go`、`apps/worker/job_cleaning.go` | `internal/cleaning/keywords.go` |
| L13 | `apps/web-user/src/views/EvaluationView.tsx`、`apps/web-user/src/views/eval/*.tsx` | `App.tsx`、`api.ts` |
| L14 | `apps/web-user/src/views/CleaningView.tsx`、`apps/web-user/src/views/cleaning/*.tsx` | `App.tsx`、`api.ts` |
| L15 | `docs/architecture/phase-8-*.md`、`docs/guides/*.md` | 已有 docs |

**跨 lane 依赖的唯一允许形式**：通过 foundation 冻结的 `internal/model/*.go` 类型与 `internal/store/*_store.go` 构造器。

---

## 2. 数据库迁移契约（编号区间 0011 ~ 0019）

编号区间已冻结。**lane 不得新增迁移文件**；若确需字段，走 issue 由父代理统一加。

### 编号 → lane 归属表（全部已落地 `main`）

| 编号 | 文件 | 归属 |
|---|---|---|
| 0011 | `eval_core.sql` | foundation |
| 0012 | `cleaning_core.sql` | foundation |
| 0013 | `chain_standards.sql` | L2 |
| 0014 | `questions_v2.sql` | L3 |
| 0015 | `export_mappings.sql` | L6 |
| 0016 | `generation_runs.sql` | foundation |
| 0017 | `grpo_prompts.sql` | L4 |
| 0018 | `sft_records.sql` | L5 |
| 0019 | `cleaning_run_rule_ids.sql` | 父代理（L12 `cleaning_runs.rule_ids` 接线） |

> **历史教训**：本节此前写 `0011 ~ 0016`，与 `sql/migrations/` 实际内容不符。
> 后果是后续 lane 读到会以为 0017/0018 仍可用，从而撞编号。
> 任何新增迁移都必须同步更新本节与本表。

### 0011 `eval_dimensions` / `eval_runs` / `eval_run_judges` / `eval_items` / `eval_item_scores` / `eval_summaries`

```sql
eval_dimensions(id, key UNIQUE, name, category, description, rubric,
                scale_min, scale_max, is_builtin, is_active, weight,
                created_at, updated_at)

eval_runs(id, dataset_id, name, sampling_mode['full'|'ratio'|'count'],
          sample_ratio, sample_size, target_kind['sft'|'grpo'],
          dimension_keys JSONB, judge_provider_ids JSONB, generator_provider_id,
          status['draft'|'queued'|'running'|'partial_failed'|'failed'|'completed'],
          total_items, scored_items, error_summary, created_by, created_at, updated_at)

eval_run_judges(id, eval_run_id, provider_id, provider_name, model,
                excluded, exclude_reason, status, scored_items, error_summary,
                created_at, UNIQUE(eval_run_id, provider_id))

eval_items(id, eval_run_id, dataset_id, question_id, item_index,
           payload JSONB, created_at, UNIQUE(eval_run_id, question_id))

eval_item_scores(id, eval_run_id, eval_item_id, judge_provider_id, dimension_key,
                 score, rationale, raw_response, status, created_at,
                 UNIQUE(eval_item_id, judge_provider_id, dimension_key))

eval_summaries(id, eval_run_id, scope['overall'|'judge'|'dimension'|'item'],
               ref_key, score, sample_count, detail JSONB, created_at,
               UNIQUE(eval_run_id, scope, ref_key))
```

### 0012 `cleaning_keywords` / `cleaning_rules` / `cleaning_runs` / `cleaning_findings`

```sql
cleaning_keywords(id, pattern, category, match_mode['contains'|'regex'|'prefix'],
                  severity['block'|'warn'], is_builtin, is_active, note,
                  created_at, updated_at, UNIQUE(pattern, category))

cleaning_rules(id, name UNIQUE, stage_scope JSONB, min_hits,
               action['drop'|'flag'|'retry'], priority, is_active, config JSONB,
               created_at, updated_at)

cleaning_runs(id, dataset_id, stages JSONB, status,
              scanned_items, flagged_items, dropped_items, report JSONB,
              error_summary, created_at, updated_at)

cleaning_findings(id, cleaning_run_id, dataset_id, question_id, stage,
                  keyword_id, matched_text, snippet, action, created_at)
```

### 0013 `chain_standards` / `chain_standard_versions`

```sql
chain_standards(id, dataset_id, domain_id, direction_key, current_version, status,
                created_at, updated_at, UNIQUE(dataset_id, domain_id))

chain_standard_versions(id, standard_id, version, steps JSONB, source['ai'|'user'],
                        change_note, created_by, created_at,
                        UNIQUE(standard_id, version))
```

### 0014 `questions` 扩展

```sql
questions += direction_domain_id BIGINT DEFAULT 0,
             difficulty TEXT DEFAULT 'medium',          -- easy|medium|hard
             difficulty_score INTEGER DEFAULT 2,        -- 1|2|3
             dedupe_key TEXT DEFAULT '',
             source TEXT DEFAULT 'ai',
             cleaning_status TEXT DEFAULT 'clean';      -- clean|flagged|dropped
INDEX (dataset_id, dedupe_key), INDEX (dataset_id, direction_domain_id)
```

### 0015 `export_mappings`

```sql
export_mappings(id, name UNIQUE, format, target_kind, field_map JSONB,
                options JSONB, is_builtin, is_default, created_at, updated_at)
```

### 0016 `generation_runs` + `datasets` 扩展

```sql
generation_runs(id, dataset_id, stage, status, cursor JSONB,
                total_units, done_units, attempts, error_summary,
                started_at, finished_at, created_at, updated_at)
INDEX (dataset_id, stage, status)

datasets += target_kind TEXT DEFAULT 'sft',
            direction_count INTEGER DEFAULT 3,
            reward_levels JSONB DEFAULT '["-1","0","1"]',
            cleaning_enabled BOOLEAN DEFAULT TRUE
```

---

## 3. HTTP 接口契约（冻结）

统一响应约定（沿用现有实现）：
- 成功：`200` / `202`（入队）
- 错误：`{"error": "..."}`，4xx 为可读消息，5xx 为 `internal server error`
- 入队响应统一为 `model.StageEnqueueResult`：
  ```json
  { "datasetId": 1, "stage": "directions", "state": "queued",
    "message": "方向生成任务已入队", "acceptedAt": "2026-09-18T10:00:00Z" }
  ```
- 异步任务统一落 `generation_runs` 表，前端通过 `GET /api/v1/datasets/{id}/generation-runs` 轮询进度。

### 3.1 L1 方向生成

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/datasets/{id}/directions/generate` | `{ "directionCount": 3 }`（可省略，取 `datasets.direction_count`） | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/directions` | — | `Domain[]`（`level=2`，`parentId` 指向领域） |
| POST | `/api/v1/datasets/{id}/generation-runs/{stage}/resume` | — | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/generation-runs` | — | `GenerationRun[]` |

### 3.2 L2 长链思维标准步骤

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/datasets/{id}/chain-standards/generate` | `{ "domainIds": [1,2] }`（空=全部方向） | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/chain-standards` | — | `ChainStandard[]`（含 `steps`） |
| PUT | `/api/v1/datasets/{id}/chain-standards/{domainId}` | `{ "steps": [{index,title,description,checkpoint}], "changeNote": "..." }` | `ChainStandard` |
| GET | `/api/v1/datasets/{id}/chain-standards/{domainId}/versions` | — | `ChainStandardVersion[]` |

### 3.3 L3 问题生成（x 可控 + 去重 + 难度分层）

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/datasets/{id}/questions/generate` | `{ "questionsPerDirection": 5, "difficultyMix": {"easy":0.3,"medium":0.5,"hard":0.2} }` | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/questions` | — | `Question[]`（含 `difficulty`） |
| GET | `/api/v1/datasets/{id}/questions/difficulty-stats` | — | `{ "levels": {"easy":3,"medium":5,"hard":2}, "total":10 }` |

> L3 覆盖 `questions.generate` job handler 与 `POST .../questions/generate` 路由。旧路由语义兼容（无请求体时按 `strategy.questions_per_domain` 执行）。

### 3.4 L4 GRPO 教师评判提示词

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| PUT | `/api/v1/datasets/{id}/reward-levels` | `{ "levels": ["-1","0","1"] }` | `{ "levels": ["-1","0","1"] }` |
| POST | `/api/v1/datasets/{id}/grpo/generate` | `{ "levels": ["-1","0","1"] }`（可省略） | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/grpo` | — | `GrpoPrompt[]` |

### 3.5 L5 SFT 思维链 + 答案

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/datasets/{id}/sft/generate` | `{ "includeAnswer": true }` | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/sft` | — | `SftRecord[]` |

### 3.6 L6 多格式导出

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| GET | `/api/v1/datasets/{id}/export/formats` | — | `{ "formats": ["jsonl","csv","parquet","alpaca","sharegpt"], "mappings": ExportMapping[] }` |
| POST | `/api/v1/datasets/{id}/export` | `{ "format": "alpaca", "mappingId": 0, "filters": {} }` | 202 `StageEnqueueResult` |
| GET | `/api/v1/admin/export-mappings` | — | `ExportMapping[]` |
| POST/PUT | `/api/v1/admin/export-mappings` | `ExportMapping` | `ExportMapping` |

### 3.7 L7 裁判模型接入

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| GET | `/api/v1/admin/eval/judges` | — | `EvalJudgeOption[]` |
| PUT | `/api/v1/eval/runs/{runId}/judges` | `{ "providerIds": [2,3] }` | `EvalRunJudge[]` |

### 3.8 L8 评估维度（≥50 内置 + 自定义）

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| GET | `/api/v1/eval/dimensions` | `?category=&builtin=` | `EvalDimension[]` |
| POST/PUT | `/api/v1/eval/dimensions` | `EvalDimension` | `EvalDimension` |
| DELETE | `/api/v1/eval/dimensions/{id}` | — | `{ "deleted": true }` |
| POST | `/api/v1/eval/dimensions/seed` | — | `{ "inserted": 56, "total": 56 }` |
| GET | `/api/v1/eval/dimensions/categories` | — | `{ "categories": ["long_chain","faithfulness",...] }` |

### 3.9 L9 评估运行（全量 / 抽样 + 逐条多维打分）

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/eval/runs` | `EvalRunCreateRequest` | `EvalRun` |
| GET | `/api/v1/eval/runs` | `?datasetId=` | `EvalRun[]` |
| GET | `/api/v1/eval/runs/{id}` | — | `EvalRunDetail` |
| POST | `/api/v1/eval/runs/{id}/start` | — | 202 `StageEnqueueResult` |
| GET | `/api/v1/eval/runs/{id}/items` | `?limit=&offset=` | `EvalItem[]` |

### 3.10 L10 汇总统计与分析结论

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| GET | `/api/v1/eval/runs/{id}/report` | — | `EvalReport` |
| GET | `/api/v1/eval/runs/{id}/scores` | `?judgeProviderId=&dimensionKey=` | `EvalItemScore[]` |

### 3.11 L11 清洗关键词库

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| GET | `/api/v1/cleaning/keywords` | `?category=&active=` | `CleaningKeyword[]` |
| POST/PUT | `/api/v1/cleaning/keywords` | `CleaningKeyword` | `CleaningKeyword` |
| DELETE | `/api/v1/cleaning/keywords/{id}` | — | `{ "deleted": true }` |
| POST | `/api/v1/cleaning/keywords/import` | `{ "patterns": ["对不起","我不能"], "category": "refusal" }` | `{ "inserted": 2, "skipped": 0 }` |
| GET/POST/PUT | `/api/v1/cleaning/rules` | `CleaningRule` | `CleaningRule[]` / `CleaningRule` |

### 3.12 L12 分步清洗 + 报告

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/datasets/{id}/cleaning/run` | `{ "stages": ["question","reasoning","answer"], "ruleIds": [] }` | 202 `StageEnqueueResult` |
| GET | `/api/v1/datasets/{id}/cleaning/runs` | — | `CleaningRun[]` |
| GET | `/api/v1/cleaning/runs/{id}/report` | — | `CleaningReport` |
| GET | `/api/v1/cleaning/runs/{id}/findings` | `?stage=&limit=` | `CleaningFinding[]` |

---

## 4. 前端契约（冻结）

`apps/web-user/src/lib/api.ts` 由 foundation 一次性写入下列导出，**lane 不得修改该文件**。

### 4.1 导航（App.tsx 冻结）

```ts
// 一级导航（普通用户）
{ label: '工作台',     route: '/console/home',      icon: LayoutDashboard }
{ label: '新建任务',   route: '/console/planning',  icon: CirclePlus }
{ label: '我的任务',   route: '/console/tasks',     icon: Target }
{ label: '数据资产',   route: '/console/results',   icon: HardDriveDownload }
{ label: '质量评估',   route: '/console/evaluation',icon: ShieldCheck }   // L13
{ label: '数据清洗',   route: '/console/cleaning',  icon: Filter }        // L14
{ label: '账户与帮助', route: '/console/help',      icon: Users }

// 管理员额外
{ label: '系统管理',   route: '/console/admin',     icon: Settings }
```

### 4.2 视图组件契约

| 文件 | 导出 | 说明 |
|---|---|---|
| `src/views/EvaluationView.tsx` | `export function EvaluationView({ datasets }: { datasets: Dataset[] })` | L13 独占 |
| `src/views/CleaningView.tsx` | `export function CleaningView({ datasets }: { datasets: Dataset[] })` | L14 独占 |
| `src/views/eval/*.tsx` | 内部子组件 | L13 独占 |
| `src/views/cleaning/*.tsx` | 内部子组件 | L14 独占 |

foundation 已创建占位实现，lane 需保持 `props` 签名不变，替换内部实现。

---

## 5. Lane 划分与依赖（3 波次）

```
Wave 1（10 lane 并行，无跨 lane 依赖）
  L1 方向生成(断点续跑)  L2 长链标准步骤  L3 问题生成v2  L4 GRPO  L5 SFT
  L6 多格式导出          L7 裁判接入      L8 评估维度     L11 关键词库  L12 清洗执行
Wave 2（依赖 Wave 1 合并）
  L9 评估运行(全量/抽样/打分)   L13 前端评估UI   L14 前端清洗UI
Wave 3（依赖 Wave 2）
  L10 汇总统计与结论             L15 文档
```

依赖原因（不可并行）：
- `L9` 需要 `L7` 的 `eval.ResolveJudges` 与 `L8` 的 `eval.DimensionByKeys` 编译通过。
- `L10` 读取 `L9` worker 写入的 `eval_item_scores`。
- `L13`/`L14` 需要 Wave 1 的接口真实存在，才能跑接口联调测试。
- `L15` 文档需引用全部 lane 的最终实现位置。

### 5.1 每 lane 的串行链

```
writer(worktree, isolation) → self-test → reviewer(fresh, 只读) → fix(resume writer)
```

- **writer**：`isolation: worktree`，只新增归属文件，提交 Conventional Commits（中文）。
- **self-test**：Go 用 `docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh -c "gofmt -l apps internal && go vet ./... && go build ./... && go test ./..."`；接口测试用 Python 放 `test/test_lNN_*.py`。
- **reviewer**：`fresh` 上下文、只读，检查契约一致性 / 真实数据（禁止 mock 与假数据）/ 边界与错误处理 / 测试有效性。输出 `Merge verdict: BLOCK | OK | OK with notes`。
- **fix**：reviewer 判 BLOCK 时 `resume: "previous"` 回到 writer 修复。

### 5.2 子代理总数

| 波次 | writer | reviewer | fix（按需） | 小计 |
|---|---|---|---|---|
| 侦察 | — | — | — | 2 scout |
| Wave 1 | 10 | 10 | ≤10 | 20~30 |
| Wave 2 | 3 | 3 | ≤3 | 6~9 |
| Wave 3 | 2 | 2 | ≤2 | 4~6 |
| **合计** | **15** | **15** | ≤15 | **32~47** |

满足「不少于 20 个子代理」。

---

## 6. 硬性约束（违反即回滚）

1. **禁止修改 foundation 冻结文件**（`apps/api/main.go`、`apps/api/routes.go`、`apps/worker/main.go`、`apps/worker/registry.go`、`internal/model/*.go`、`internal/store/*_store.go` 构造器部分、`apps/web-user/src/lib/api.ts`、`apps/web-user/src/App.tsx`）。
2. **禁止 mock 与假数据**。真实 LLM 调用需要 `provider.APIKey` + `provider.BaseURL`；缺 key 的测试必须显式标记「输入缺失」并报告，不得伪造通过。
3. **禁止最小 MVP 自我欺骗**。功能必须端到端可用：DB → store → LLM → API → worker → 前端。
4. **main 分支禁止直推**，一律特性分支 + PR。
5. **端口冲突只改本项目映射端口**（web-user `3210`），绝不停止其他业务容器。
6. **一次提交一件事**，Conventional Commits 中文，不得添加 `Co-authored-by` / `Generated with` 等额外署名。
7. **每个 lane 必须留下可运行验证**：Go 编译/测试 + Python 接口测试（写清测试项）。
8. 完成后必须开 PR 并合并。

---

## 7. 验收标准

- `功能说明.txt` 7 项需求逐条可演示。
- `docker run ... golang:1.24-alpine sh -c "gofmt -l apps internal && go vet ./... && go build ./... && go test ./..."` 全绿。
- `npm run build`（tsc --noEmit + vite build）全绿。
- `test/` 下 Python 接口测试可运行，缺 key 项明确标注「输入缺失」。
- `docs/architecture/phase-8-*.md` 中文图文并茂。
