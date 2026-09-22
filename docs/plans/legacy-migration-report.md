# 旧数据迁移盘点与映射报告（T30 交付物）

> 本文件是 Issue [#160](https://github.com/1420970597/llm/issues/160) 的任务 **T30**
> 的交付物之一。工具实现：`cmd/studio-migrate/`（CLI）与 `internal/legacy/inventory.go`
> （只读盘点与映射规划）。契约见
> [`atelier-implementation.md`](atelier-implementation.md) §6.3 与 §1.1 的 T30 行。
>
> **本文件不声称任何旧数据已经被迁移。** 它记录的是「盘点工具如何工作、如何解读输出、
> 以及当前仓库/部署状态下实测到的盘点结论」。真正的导入属于 T31。

---

## 1. 工具做什么、不做什么

| 做 | 不做 |
|---|---|
| 只读盘点 29 张旧表的行数、大小、状态分布 | 不写任何业务数据（没有任何 `INSERT`/`UPDATE`/`DELETE`/`TRUNCATE`） |
| 为每个旧 dataset 产出映射决策与可跳转的示例 ID | 不调用任何模型（`internal/legacy` 不 import `internal/llm`/`internal/eval`） |
| 显式标记「无法确定」的来源 | 不猜测归属，不伪造历史版本或旧文件 hash |
| 输出 JSON 报告 + 人类可读摘要 | 不删除任何源数据 |

两条**结构性**保证而不是约定：

1. **只读事务**：全部查询跑在 `BEGIN READ ONLY` 里。任何写操作会被 Postgres 直接拒绝
   （`cannot execute ... in a read-only transaction`），因此 dry-run 不是「代码里没写
   INSERT」，而是数据库拒绝写。测试用「在同样选项的事务里执行 `CREATE TEMP TABLE`
   必须失败」来冻结这条机制。
2. **不引用模型包**：测试直接断言 `internal/legacy/inventory.go` 的**代码**（去掉注释后）
   不含 `internal/llm`、`internal/eval`、`internal/studio`、`internal/exporter`，
   也不含任何写语句关键字。

没有 `-apply` 开关：T30 的原文要求就是 dry-run，而「提供一个写开关再要求别人别按」
是把保证建立在人的记性上。真实导入在 T31，会有独立的游标与对账路径。

## 2. 如何运行

```bash
# 直接跑（报告落盘）
go run ./cmd/studio-migrate -dsn "$LLM_MIGRATE_DSN" \
  -report artifacts/studio-migrate/report.json

# 只看摘要
go run ./cmd/studio-migrate -summary

# 容器里（本仓库宿主机没有 Go）
./scripts/go-test-postgres.sh go run ./cmd/studio-migrate -summary
```

DSN 解析顺序：`-dsn` → `LLM_MIGRATE_DSN` → `LLM_TEST_POSTGRES_DSN` → `DATABASE_URL`。

报告 JSON 的形状（`Report`）：

```text
generatedAt / dryRun / readOnlyTransaction
tables[]   { name, rows, sizeBytes, statuses{...} }
datasets[] { datasetId, name, status, ownerId,
             domains/questions/sftRecords/reasoningRecords/rewardRecords/
             grpoPrompts/evalRuns/cleaningRuns/artifacts/missingArtifacts/generationRuns,
             decisions[] { kind, severity, reason, sampleIds[] } }
totals{}   每个决策类型的计数
notes[]    本工具**无法验证**什么、以及为什么
```

## 3. 映射策略（决策类型 → 处置）

| 决策类型 | 严重度 | 判据 | 处置 |
|---|---|---|---|
| `needs_owner` | blocker | `datasets.created_by` 为空，或对应用户已删除 | 归入**受限待归属区**，由管理员显式分派。不得默认给任意用户或所有人读权限 |
| `mappable_project` | info | `generation_runs` 有行 | 映射为 legacy-origin 项目，并把运行映射为批次（保留原 dataset ID 以便追溯） |
| `import_snapshot` | warning | 无生成运行，但有领域/问题 | 导入为**快照批次**，不冒充可复现的历史运行 |
| `conflict` | warning | 同工作区已存在同名项目 | 导入前必须改名或合并 |
| `needs_reeval` | warning | 有 `eval_runs`，尤其缺维度配置 | 只保留为**未验证**的 legacy evidence；要让结论可用必须在 Atelier 里新建实验 |
| `reasoning_separate_source` | info | 有 `reasoning_records` | 保留为**独立来源**，不与 SFT 一等表合并 |
| `grpo_not_teacher_material` | warning | 有 `reward_records` | 分数**不映射**为 GRPO 教师材料（奖励信号不是判分标准） |
| `artifact_unverified` | warning | 有 `artifacts` | 路径可能已被覆盖，只能验证现存字节；保留为「历史工件/未验证」，不自动标为已发布 |
| `missing_reference` | blocker | 工件的 `object_key` 为空 | 无法定位对象，必须人工确认或标记为丢失 |
| `unknown_source` | info | 没有领域/问题/任何内容记录 | 空壳 dataset：映射时归档而不是导入 |

三条判据的**取舍理由**（都是「另一种写法会得到看似合理其实错误的结论」）：

- **「有生成证据」看 `generation_runs` 而不是 `datasets.status`**：status 是流程状态，
  历史上出现过「标记完成但零产出」。用它当证据会把不可复现的内容当成可复现运行。
- **无 owner 是 blocker 而不是「先导入再说」**：导入的动作本身就是一次授权
  （内容进入某个工作区、某些人有读权）。归属不明时先导入等于静默授权。
- **旧工件不自动标为已发布**：Atelier 的 `published` 承诺「内容、来源、mapping、
  manifest 与文件 hash 均可复算」。旧工件的固定路径可能已被覆盖，只能验证**现存字节**，
  无法证明历史 hash。给它 `published` 是伪造可复现性。

## 4. 实测结论（2026-09-22，本仓库开发环境）

| 环境 | 结果 |
|---|---|
| 临时迁移到最新的库（`scripts/go-test-postgres.sh`） | 29 张表全部可盘点；0 个 dataset；4 条 `notes` 全部输出 |
| 部署中的开发库（`llm-postgres-1`） | **迁移不完整**：`schema_migrations` 有 21 行，而目录有 35 个迁移文件（缺 0022–0038） |

后一条是这次盘点**发现的真实状态**，也是工具的一条设计依据：库落后于代码时，
`releases`/`sample_versions`/`batches`/`experiments` 等表不存在，直接 `COUNT(*)` 会以
`relation does not exist` 失败，让一次本来可以部分完成的盘点变成一个也拿不到的错误；
而静默跳过会让报告看起来完整。

工具因此冻结为：**跳过缺失的表、在 `notes` 里以「数据库迁移不完整」显式写明缺了哪些表、
并且不产出逐个 dataset 的决策**（决策依赖多张表，缺一张就会得出「没有证据 →
导入快照」这类看似合理其实错误的结论）。

> 结论：**任何真实盘点之前，必须先把目标库的迁移应用到最新**，然后重跑本工具。
> 在迁移不完整的库上得到的报告不能用于确认导入范围。

## 5. 已知未知项（本工具无法验证的）

1. **对象存储字节存在性未验证**：工具不连接 MinIO/S3（不持有凭证）。历史工件的 hash
   只能在其字节仍存在时重新计算；缺失/被覆盖必须人工确认。
2. **无法恢复被 upsert 抹掉的历史**：旧 SFT/GRPO 表按 `(dataset_id, question_id)`
   upsert，同一题的历史版本已经不存在。T30 只能导入**当前内容**，不能声称那是完整历史。
3. **旧评估口径无法复算**：`eval_runs` 缺维度配置时，当时用的量表/裁判不可知，
   因此只能标记未验证。
4. **`datasets.created_by` 指向已删除用户**时无法自动归属，一律进待归属区。

## 6. 下一步（T31）

- 迁移 `sql/migrations/0034_studio_legacy_imports.sql` 的 `legacy_imports` 唯一来源键/游标/内容 hash；
- 按 dataset 小批导入，支持暂停、续跑、重复执行零重复；
- 每个 dataset 迁移前冻结旧写入口并记录一致性水位；
- 旧路由兼容（`/console/tasks/:id` 映射跳转）；
- 迁移前后数量/状态/内容 hash/引用/文件下载对账，失败报告精确到对象。
