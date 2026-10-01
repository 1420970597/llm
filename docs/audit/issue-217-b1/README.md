# Issue #217 B1 — `make db-migrate-smoke` 只应用 0001，却「永远绿灯」（第 1 轮）

## 结论

**已修复。** `make db-migrate-smoke`（AGENTS.md §1 描述为「验证 SQL 迁移脚本」）
此前只执行 `sql/migrations/0001_phase1_foundation.sql`：实测走完后库里只有 **3 张表**，
`samples` / `sample_versions` / `batches` / `review_projections` / `legacy_imports`
**全部不存在**。也就是说这条「迁移验证」命令对 0022–0039（共 18 份）**零覆盖**，
却永远返回 0 —— 与「把无证据当成已验证」是同一类失败。

修复后逐文件应用**全部 39 份迁移**，库里有 **76 张表**、核心表齐全。

> 本轮只处理 #217 的 B1 子项（迁移验证补齐）。#217 其余 19 项是外部素材/产物导入的
> **产品功能**（两个端点、`source_chunks`、分块引擎、前端页面等），属独立的大范围特性开发，
> 不在本轮自动化范围内（见「未收口」）。

## 复现（修复前）

`docs/audit/issue-217-b1/repro.sh before` 复刻旧 target 的行为（只应用 0001）：

```json
{
  "appliedMigrations": 1,
  "publicTableCount": 3,
  "coreTables": { "datasets": true, "samples": false, "sample_versions": false,
                  "batches": false, "review_projections": false, "legacy_imports": false },
  "missingCoreTables": ["samples","sample_versions","batches","review_projections","legacy_imports"],
  "ok": false
}
```

对照：AGENTS.md §1 的原文是

```bash
make db-migrate-smoke          # 启动临时 Postgres 验证 SQL 迁移脚本
```

「SQL 迁移脚本」（复数）与「只跑 0001」之间的落差就是缺陷本体。

## 根因

`Makefile` 的 target 内联了单文件 psql：

```make
db-migrate-smoke:
	docker exec -i llm-postgres-migrate-smoke psql ... < sql/migrations/0001_phase1_foundation.sql
```

它既没有遍历 `sql/migrations/*.sql`，也没有任何「迁移真的生效了吗」的断言，
因此当部署退回到「只跑第一个迁移」时，命令照样成功 —— 静默空转。

## 改动

| 文件 | 改动 | 目的 |
| --- | --- | --- |
| `scripts/db-migrate-smoke.sh`（新增） | 起临时 Postgres → 按文件名顺序 `ON_ERROR_STOP=1` 应用**全部**迁移 → 断言核心表存在；就绪判定用「日志两次 ready + TCP 连续两次 SELECT 1」 | 与 `go-test-postgres.sh` 同一套就绪/失败定位约定；CI 与人工同一入口 |
| `Makefile` | `db-migrate-smoke` 改为 `bash scripts/db-migrate-smoke.sh` | 消除内联单文件 |
| `test/l15_migration_smoke.mjs`（新增） | 源码契约断言 + 迁移清单断言 + **3 条变异自证** | 防「退回只跑 0001」的静默回归 |
| `.github/workflows/ci.yml` | 把 `migration_smoke` 加入 frozen l15 守卫列表 | 守卫真的会在 CI 跑 |
| `Makefile`（`legacy-import`） | `@test` → `@command test`（语义等价） | 消除 shellcheck 把 Make 配方误判为 bats 的解析错误 |

`REQUIRED_TABLES` **不含** `schema_migrations`：它由 Go 侧 `internal/migrate` 在启动时创建
（记录已应用文件名），不是 SQL 迁移产物，断言它会误报。

## 验证（修复后）

`docs/audit/issue-217-b1/repro.sh after`（本次修复路径）：

```json
{
  "phase": "after",
  "appliedMigrations": 39,
  "publicTableCount": 76,
  "missingCoreTables": [],
  "allCoreTablesPresent": true,
  "ok": true
}
```

`make db-migrate-smoke` 实测输出（节选）：

```text
[db-migrate-smoke] 按文件名顺序应用全部迁移（共 39 个）
[db-migrate-smoke] 迁移完成：39 个文件
[db-migrate-smoke] 核心表已建：datasets samples sample_versions batches review_projections legacy_imports
[db-migrate-smoke] OK：全部迁移在干净库上应用成功且核心表存在
```

| 判定（机器事实） | 修复前 | 修复后 |
| --- | --- | --- |
| 应用的迁移文件数 | 1 | **39** |
| `public` schema 表数 | 3 | **76** |
| 缺失核心表 | 5 张 | **0** |
| 命令退出码（缺表时） | 0（静默空转） | **1**（缺表即失败） |

## 门禁与守卫

```text
go-gate.sh: gofmt clean · go vet clean · go build ok · go test ok（EXIT=0）
node test/l15_migration_smoke.mjs: 通过（1 条结构断言 + 1 条清单断言 + 3 条变异自证）
make -n db-migrate-smoke: 解析正常
bash scripts/db-migrate-smoke.sh: 退出码 0
```

**变异自证**（证明守卫非空转）：

| 变异 | 结果 |
| --- | --- |
| 脚本退回只遍历 `0001` | 结构断言 FAIL（捕获 1 个问题） |
| 删掉核心表断言（`REQUIRED_TABLES=()`） | 结构断言 FAIL（捕获 1 个问题） |
| Makefile 退回内联只跑 0001 | 结构断言 FAIL（捕获 2 个问题） |

> 第一条变异最初**空转**：断言当时用「文本里有没有 `sql/migrations/*.sql`」判断，
> 而脚本的提示语里也出现这个 glob。变异自证把这一点抓了出来，随后收紧为
> 「必须匹配 `MIGRATIONS=(sql/migrations/*.sql)` 赋值本身」——
> 这正是变异自证存在的意义（守卫自己先失败一次，好过它假装通过）。

## 未收口 / 残留风险

| #217 子项 | 状态 |
| --- | --- |
| **B1 迁移验证补齐** | **已修复（本轮）** |
| A1/A2/A3 目标结构树与容量校验（服务端类型/配额校验、结构树页面） | 未开始。A2 的「批次创建前可产出量校验」已由 #190 的 `CoverageCapacity` 落地（`internal/store/batch_store.go:268`），但 A1 的 `sourceChunkIds`/`source` 取值集合与 A3 的结构树页面（`apps/web-user/src/studio/pages/` 下的 TargetStructurePage.tsx）**不存在** |
| B2–B7 素材来源（第六类文档、分块引擎、上传端点、采集台账扩展、worker job、前端页面） | 未开始（`internal/import/source_document.go`、`internal/store/source_chunk_store.go`、`sql/migrations/0040_studio_source_documents.sql` 均不存在） |
| C1–C4 成品导入（格式映射、导入端点、导入向导、契约文档） | 未开始 |
| D1–D3 存量盘点与 `questionFor` 接地替换 | 未开始。D2 指出的 `questionFor` / `grpoQuestionFor` 两份副本仍在（`apps/worker/studio_batch.go:210` 与 `studio_grpo.go:236`） |
| E1–E3 质量门与证据 | 未开始 |

- 其余 19 项是**成体系的产品功能**（涉及新表、新端点、新页面与 LLM 接线），
  与本轮「迁移验证补齐」不是同一量级；按 SOP §8.3，本轮记 `partial` 并保持开启，
  由后续轮次/人工按计划推进，不在本轮擅自扩张范围。
- #217 自称的「权威正文」（仓库根的 `docs/plans/` 下的 external-source-import-plan.md）**在 `main` 上不存在**：
  它只存在于 `docs/TASK-165-197-blueprint-workflow-rearchitecture` 分支（提交 `3fc7ce2`）。
  issue 正文因此目前是唯一权威；这不影响 B1 的修复，但计划「双层结构」的文档侧链接暂时悬空。

## 复现/验证命令

```bash
bash docs/audit/issue-217-b1/repro.sh before   # 复刻旧行为（只应用 0001）
bash docs/audit/issue-217-b1/repro.sh after    # 新 target 行为（应用全部）

bash scripts/db-migrate-smoke.sh               # 等价于 make db-migrate-smoke
node test/l15_migration_smoke.mjs
```
