# Issue #200 — 今日工作「待人工判断」恒为 0，与审阅队列真实待办矛盾（复核轮）

## 结论

**已修复（核验通过，可关单）。** 本轮**没有改任何业务代码**：缺陷已由
`a340120`（PR #231，`fix(studio): #200 概览「待处理决定」恒为 0`）落地并进入
`origin/main`，只是 issue 未被关闭、也未写台账。本轮做**同条件前后复跑取证 + 守卫变异自证**，
确认三条渲染路径的口径已收敛，缺陷确实消失。

这与 #205 / #206 / #208 / #210 / #213 的处理形态一致：修复已在 main，复核轮只补证据与关单。

## 缺陷形态与三条渲染路径

同一个「待判断」事实在界面上出现**互相矛盾的读数**：直接数 `review_projections` 的行，
在「一次判断都没做过的项目」上恒为 0（新项目没有投影行），而审阅队列把未判断内容都算作待判断。

受影响的渲染路径有三条，修复统一到**同一条共享谓词**
（`internal/store/sample_query.go` 的 `latestReviewProjectionJoin` / `effectiveReviewStatusSQL`）：

| 路径 | 位置 | 缺陷形态 |
| --- | --- | --- |
| 1 | `/today` 磁贴「待人工判断」与 DECISIONS「需要你的决定」列表 | 待办列表里根本没有 `pending_review` 这一行 |
| 2 | `/p/{id}/overview`「待处理决定」指标 | `apps/api/routes_studio_read.go` 从未给 `stats.pendingReview` 赋值 → 恒为 0 |
| 3 | 审阅队列 `/p/{id}/review` | 对照组（本来就是正确口径） |

## 实测读数（真实栈 + 真实 Chromium 1600×1000，`admin@company.com`，项目 1）

| 判定（机器事实） | 修复前 | 修复后 |
| --- | --- | --- |
| 审阅队列条数（对照组） | 3 | 3 |
| `/p/1/overview` `stats.pendingReview` | **0** | **3** |
| DECISIONS 存在 `pending_review` 待办 | **❌ 缺失** | **✅ 3** |
| 概览指标 == 队列 | ❌ | ✅ |
| 待办计数 == 队列 | ❌ | ✅ |
| `ok`（三条判据全成立） | false | true |

### 修复前（`verify-before.json`）

```json
{"queueItems": 3,
 "todaySurface": {"overviewPendingReview": 3, "pendingTodoCount": null, "cardRendersPending": false},
 "overviewSurface": {"statsPendingReview": 0, "overviewMatchesQueue": false},
 "ok": false}
```

### 修复后（`verify-after.json`）

```json
{"queueItems": 3,
 "todaySurface": {"overviewPendingReview": 3, "pendingReviewTodoCount": 3,
                  "pendingReviewTodoSummary": "3 条内容等待你判断", "cardRendersPending": true},
 "overviewSurface": {"statsPendingReview": 3, "overviewMatchesQueue": true},
 "ok": true}
```

## 前后对比图

![修复前：概览「待处理决定」显示 0，而审阅队列为 3](https://raw.githubusercontent.com/1420970597/llm/d0a19b7edded402c183f3961efefb1f061f90771/docs/audit/issue-200/verify-before-overview.png)

![修复后：概览「待处理决定」显示 3，与队列一致](https://raw.githubusercontent.com/1420970597/llm/d0a19b7edded402c183f3961efefb1f061f90771/docs/audit/issue-200/verify-after-overview.png)

![修复前：今日工作 DECISIONS「需要你的决定」里没有「待判断」这一行](https://raw.githubusercontent.com/1420970597/llm/d0a19b7edded402c183f3961efefb1f061f90771/docs/audit/issue-200/verify-before-today.png)

![修复后：DECISIONS 出现「待判断 3 条内容等待你判断」，与队列一致](https://raw.githubusercontent.com/1420970597/llm/d0a19b7edded402c183f3961efefb1f061f90771/docs/audit/issue-200/verify-after-today.png)

## 修复前证据的取法（同条件保证）

缺陷在**后端 API 二进制**里（前端只是渲染服务端读数），因此无法用「换回前端源码」的办法取证。
本轮的做法是**把 `origin/main` 上仅 #200 的三处改动回退**，用同一个
`deployments/docker/api.Dockerfile` 的构建方式产出一个「单变量」API 二进制：

```bash
git worktree add --detach /tmp/prefix200-clean origin/main
git -C /tmp/prefix200-clean checkout a340120~1 -- \
  apps/api/routes_studio_read.go internal/store/activity_store.go internal/store/sample_query.go
# 汉 is 唯一变量：git diff origin/main -- apps internal sql cmd 只有这三处（3 insertions / 36 deletions）
docker run --rm -v /tmp/prefix200-clean:/src -w /src golang:1.24-alpine sh -c "go build -o /src/api-prefix ./apps/api"
docker cp /tmp/prefix200-clean/api-prefix llm-api-1:/usr/local/bin/api
docker restart llm-api-1        # 采完立即用备份二进制还原并核对 md5
```

因此前后是**同栈、同账号、同视口、同路由、同数据**，唯一变量是那三处 #200 代码；
web-user 侧 `version.json` 两侧均为 `a889b43`（前端未参与前后差异）。
采集后把 API 二进制还原为运行中栈的原始版本（`md5 = b06b976abf6a29df0fc60e6290c5e6cb`），
`/p/1/overview` 读数回到 3，确认栈已复原。

## 门禁 / 守卫

三条守卫测试（`internal/store/activity_store_test.go`）在**真实 Postgres** 上全绿：

```text
TestOverviewPendingReviewMatchesQueue ............... PASS
TestTodosPendingReviewMatchesQueue ................. PASS
TestCountPendingReviewSamplesByProjectMatchesQueue .. PASS
```

**变异自证（证明三条守卫非空转）**：在同一套真实 Postgres 上分别把 fix 改回旧实现，
对应守卫必须失败 —— 实测两条都失败：

| 变异 | 回退的代码 | 结果 |
| --- | --- | --- |
| A（待办路径） | `LoadTodos` 的 `pending_review` spec 退回直数 `review_projections` | `TestTodosPendingReviewMatchesQueue` **FAIL**（概览待判断 0 与队列 1 不一致） |
| B（概览路径） | `CountPendingReviewSamplesByProject` 退回直数 `review_projections` | `TestCountPendingReviewSamplesByProjectMatchesQueue` **FAIL**（概览待判断 0 与审阅队列 1 不一致） |

## 复现/验证命令

```bash
OUT_DIR=/root/llm/docs/audit/issue-200 FILE_PREFIX=verify- \
  node test/audit/issue-200/repro.mjs --phase before   # 需先部署单变量前缀二进制
OUT_DIR=/root/llm/docs/audit/issue-200 FILE_PREFIX=verify- \
  node test/audit/issue-200/repro.mjs --phase after

scripts/issue-bot/preflight.sh evidence-check \
  docs/audit/issue-200/verify-before-overview.png docs/audit/issue-200/verify-after-overview.png
scripts/issue-bot/preflight.sh evidence-check \
  docs/audit/issue-200/verify-before-today.png docs/audit/issue-200/verify-after-today.png

bash scripts/go-test-postgres.sh go test ./internal/store/ \
  -run 'TestCountPendingReviewSamplesByProjectMatchesQueue|TestTodosPendingReviewMatchesQueue|TestOverviewPendingReviewMatchesQueue' -v
```

> `test/audit/issue-200/repro.mjs` 本轮新增 `OUT_DIR` / `FILE_PREFIX` 两个环境变量，
> 让复核轮把证据写到独立文件名（`verify-*`），**不覆盖**修复轮已提交的
> `docs/audit/issue-200/{before,after}-*.{png,json}`（该归档仍是修复轮的原件）。
> 默认行为（不设这两个变量）与修复轮完全一致，防覆盖逻辑照旧生效。

## 残留风险

- 本轮**未改动任何产品代码**，因此没有新增门禁风险；唯一的仓库改动是
  `test/audit/issue-200/repro.mjs` 的输出路径开关（默认行为不变）与本 README。
- 「修复前」读数是**受控构造**（回退 #200 三处代码的二进制）而非历史遗留的现场截图：
  历史现场截图（`docs/audit/usertest-20260928/`）证明缺陷曾存在，本轮构造证明
  「在其余代码不变时，仅这三处决定读数」。两者互补。
