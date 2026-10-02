# Issue #197 第 13 条残余子项：数据集分析的「长度」口径不可见（第 2 轮）

> 代码基线 `origin/main` @ `a889b43`；验证环境 真实容器栈 `127.0.0.1:3210` + 真实 Chromium 1600×1000。
> 采集脚本：`test/audit/issue-197-13/repro.mjs`（可重跑，`--phase before|after`，默认拒绝覆盖已提交证据）。

## 结论

**已修复。** 生产批次详情的「数据集结构与内容分析」里，「长度中位 / P90」是
`question + reasoning + answer (+teacherPrompt)` 的**合并字符数** —— 但：

1. 服务端 `fieldCount` **硬编码为 1**，而实际参与统计的是 3 个字段；
2. 卡片上的 hint 只写「最短 / 最长 / 均值（字符数）」，**没有任何口径说明**；
3. `notes`（口径说明）列了分位法、重复率、接地率，**唯独漏了长度口径**。

用户看到「长度中位 1082」会把它读成单条内容的长度（例如只算问题），而它是
「问题 + 推理过程 + 答案」之和。**数字可见但口径不可见**，与 #191 的
「内部键泄漏」是同一类失败：把存储表示当成了用户概念。

> 这一条在多轮复核里被反复记为「已知限制」但从未收口 —— 本轮把它作为该 issue
> **第 13 条的唯一未收口点**正式收口。

## 复现（修复前）

`node test/audit/issue-197-13/repro.mjs --phase before`（部署版本 `a889b43`）：

```json
{"count":4,"shortest":1052,"longest":1699,"p50":1082,"p90":1699,"meanChars":1235,"fieldCount":1}
```

```text
fieldCount 声称=1 · 字段明细=缺失
界面口径可见=false
界面读到的长度磁贴=长度中位 / P90 | 1082 / 1699 | 最短 1052 · 最长 1699 · 均值 1235（字符数）
```

![修复前：长度读数只有数字，没有口径；fieldCount 恒为 1](https://raw.githubusercontent.com/1420970597/llm/5df6bdc0a2dd62d3d3464e87f26c53113e9d6404/docs/audit/issue-197-13/before-analysis.png)

**为什么这是实质缺陷**：第 13 条的原文诉求是「加入分析数据集的长度、数量、占比等指标」，
目标是「使用户清晰可见」。数字本身算对了（读数是真实的字符数），
但「这串字符数是谁的和」没有交代 —— 用户拿它做判断（例如「内容够不够长」）时会读错。

## 根因

| 位置 | 形态 |
| --- | --- |
| `internal/studio/dataset_analysis.go` | `LengthAnalysis.FieldCount: 1` 是**常数**，与本次统计的真实字段集无关 |
| `internal/store/batch_store.go` | `summarizePayload` 把多个字段累加成一个 `chars`，**逐字段事实在函数内被丢弃** |
| `apps/web-user/src/studio/pages/RunPages.tsx` | 卡片只渲染 `p50/p90/shortest/longest/meanChars`，没有口径行 |
| `internal/studio/dataset_analysis.go` | `notes` 里没有长度口径说明 |

## 改动

| 文件 | 改动 | 目的 |
| --- | --- | --- |
| `internal/model/studio_docs.go` | 新增 `SampleLengthFields()`：长度口径的**唯一权威** | 口径此前隐含在 store 的实现细节里；跨 store/studio/前端三处使用，必须有单一来源（AGENTS.md §3.1） |
| `internal/store/batch_store.go` | `summarizePayload` 返回逐字段长度 `LengthByField`；`SampleVersionFact` 带出该事实 | 让「长度由哪些字段构成」成为可核对的读数，而不是上层的猜测 |
| `internal/studio/dataset_analysis.go` | 新增 `lengthFieldsOf`（取并集、按权威顺序）；`LengthAnalysis` 新增 `Fields`，`FieldCount` 改为 `len(fields)` | 字段数从**事实**推导；批次内不同样本字段集不同时取并集 |
| `apps/web-user/src/lib/enumLabels.ts` | 新增 `SAMPLE_FIELD_LABELS` / `describeSampleField` / `describeLengthScope` | 口径文案集中一处；**未登记字段给中性中文**，绝不把英文键漏到界面（#191 形态） |
| `apps/web-user/src/studio/pages/RunPages.tsx` | 长度磁贴下方新增口径行（`data-analysis-length-scope`） | 用户可见「长度是 问题 + 推理过程 + 答案 的字符数合计（共 3 个字段）」 |
| `apps/web-user/src/lib/api/studio.ts` | `length` 类型补 `fieldCount` / `fields` | 类型与端点一致 |
| `internal/store/sample_payload_length_test.go`（新增） | 4 条纯函数断言 | 正常路径（逐字段之和 == 总量）+ 边界（空串/非字符串/非法 JSON 不参与） |
| `internal/studio/dataset_analysis_test.go`（新增） | 5 条纯函数断言 | 正常（多字段）+ 边界（单字段、空集合、并集与稳定顺序） |
| `test/l15_issue197_remediation.mjs` | 新增 1 条结构断言 + **3 条变异自证** | 防退回硬编码 / 防界面只给数字 / 防口径文案漏英文键 |

**关键设计取舍**：

1. **不编造字段数**。旧实现「恰好为 1」在只有 `question` 的历史数据上是对的，
   但那是巧合 —— 新实现按实际命中字段给出（b_3 实测 3 个字段）。
2. **取并集而不是首条**。批次内不同样本可能写了不同字段集，只取首条会把
   「有的样本算了 4 个字段」抹掉，而用户面对的是整批读数。
3. **口径文案不放在服务端**。若服务端拼接 `question + reasoning + answer`，
   就会把英文键直接漏到界面 —— 那正是 #191 要消灭的形态。服务端只给**事实**
   （字段键），中文由前端的集中映射产出。

## 验证（修复后）

`node test/audit/issue-197-13/repro.mjs --phase after`（部署版本 `5df6bdc`，同脚本同条件）：

![修复后：长度读数下给出字段合计口径](https://raw.githubusercontent.com/1420970597/llm/5df6bdc0a2dd62d3d3464e87f26c53113e9d6404/docs/audit/issue-197-13/after-analysis.png)

| 判定（机器事实） | 修复前 | 修复后 |
| --- | --- | --- |
| `length.fieldCount` | **1**（常数） | **3**（事实） |
| `length.fields` | **缺失** | `["question","reasoning","answer"]` |
| 界面口径元素数 | 0 | **1** |
| 界面口径文案 | — | `长度是 问题 + 推理过程 + 答案 的字符数合计（共 3 个字段）；不是单条内容的长度。` |
| 长度读数本身（p50/p90/最短/最长/均值） | 1082/1699/1052/1699/1235 | **完全相同**（修复只补口径，不改读数） |
| `pageerror` / 5xx | 0 | 0 |

**修复前后是同栈、同账号、同视口、同路由、同数据**：`before` 采于部署中的修复前前端
（`/version.json = a889b43`），随后重建并 `compose up -d --build`（`/version.json = 5df6bdc`）再采 `after`。

## 门禁结果

```text
go-gate.sh:       gofmt clean · go vet clean · go build ok · go test ok（EXIT=0）
go-test-postgres.sh: ok（真实 Postgres，internal/store 54s）
npm run build:    通过（tsc + vite）
l15_issue197_remediation.mjs: 通过（20 条结构断言 + 31 条变异自证）
l15 冻结守卫 22 个脚本: 全部 rc=0
evidence-check:   EVIDENCE OK
```

**变异自证**（证明守卫非空转）：

| 变异 | 结果 |
| --- | --- |
| `FieldCount: len(fields)` → `FieldCount: 1` | 断言 FAIL（捕获 2 个问题） |
| 界面摘掉 `data-analysis-length-scope` | 断言 FAIL（捕获 1 个问题） |
| `describeSampleField` 退回 `return raw` | 断言 FAIL（捕获 1 个问题） |

> 第三条变异**最初空转**：断言当时只查「函数存在」，于是把函数体改成 `return raw`
> 仍然通过。变异自证把它抓了出来，随后收紧为「必须查 `SAMPLE_FIELD_LABELS[raw]`」——
> 这正是变异自证存在的意义（守卫先自己失败一次，好过它假装通过）。

## 未收口 / 残留风险

| #197 子项 | 状态 |
| --- | --- |
| 第 13 条 · 长度口径可见（本子项） | **已修复（本轮）** |
| 第 13 条 · 逐字段**分列**展示 | **未做**：本轮只把「合计了哪几个字段」说清楚，未新增逐字段长度表。属增量展示，非缺陷 |
| 第 6 条 · 变更理由必填 / 乐观锁 | **未修复（需人工决策）**：`变更理由` 仍必填、保存走 `expectedRevision`。取舍需产品决策，见第 1 轮评论 §B |
| 第 11 条 · 拖拽式流程编排画布 + 参数 schema 先行 | **未修复**：产品定位级改造（原型见 `docs/prototypes/blueprint-workflow-rearchitecture/`），超出本轮自动化范围 |
| 其余 14 条 | 前两轮真机复核判定已解决 |

- 本轮**不关闭**该 issue：第 6 条与第 11 条仍在等待人工决策。按 SOP §8.3 记 `partial`。
- 第 1 轮的 PR #245（第 11 条 §A）与本轮的 PR 都在等人工合并；两处证据图链依赖各自分支存活。

## 复现/验证命令

```bash
node test/audit/issue-197-13/repro.mjs --phase before   # 需部署 a889b43（修复前）
node test/audit/issue-197-13/repro.mjs --phase after    # 需部署含修复的版本
node test/l15_issue197_remediation.mjs
```
