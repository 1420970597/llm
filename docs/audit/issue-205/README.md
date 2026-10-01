# Issue #205 — `/today` 总览磁贴的跳转目标（复核轮）

## 结论

**已修复，本轮复核确认可关单。**

修复由 `91e7e1a`（PR #225，`fix(web): #214 第二轮 5 条未收口缺陷`）落地并已进入 `origin/main`；
issue 当时未被关闭，因此本轮的工作是**同条件复跑取证 + 守卫复核**，而不是再次修改代码
（与 #206 的处理形态一致）。

## 判定口径（approved 规则）

`TodayPages.tsx` 的 `overviewProjectHref` 有两条**经 issue 讨论后冻结**的分支：

| 工作区项目数 | 磁贴目标 | 理由 |
| --- | --- | --- |
| **恰好 1 个** | 深链到 `/p/{id}/…` | 能直接回答该数字 |
| **多个** | 先到 `/projects`（让用户选） | 不猜一个项目 —— 那会在界面上展示属于别人项目的数字 |

两种形态下**都不允许**出现：
1. `href` 等于当前路径（`/today`，点了原地不动）；
2. 「待人工判断」指向 `/activity`（动态页不显示任何待判断样本）；
3. 「被挡住 N > 0」而没有独立出口（交付库按定义只显示已发布版本）。

## 取证设计：为什么有两组证据

真实栈当前工作区（默认工作区）**有 2 个项目** —— 其中项目 #2 是 #209 复核轮创建的
（`209-verify-incomplete-conn`）。因此真实数据只会走到**多项目回退**分支。

issue 要求的磁贴目标表（`/p/{id}/runs`、`/p/{id}/review`、`/p/{id}/data`、`/p/{id}/releases`）
只在单项目时生效。为了把这条分支也变成**可重复的机器事实**，`repro.mjs` 支持
`--stub-single-project`：它用 `page.route` 只改写 `/api/v1/today` 响应里
`overview.projectCount / scopedProjectIds` 两个字段，**其余响应体原样透传**
（因此磁贴上的数字与真实数据一致，唯一变量是「工作区里有几个项目」）。
这份证据在 JSON 的 `controlledInput` 字段里明确标注为受控输入，**不冒充真实数据**。

「修复前」证据的取法：把 `apps/web-user/src/studio/pages/TodayPages.tsx` 临时换回
`91e7e1a~1`（修复前）的版本、用同一命令重新构建前端产物，`docker cp` 进正在运行的
`llm-web-user-1` nginx 根目录；采完立即还原。因此前后是**同栈、同账号、同视口、
同路由**，唯一变量是那份缺陷代码。

## 实测读数

### 真实数据（多项目回退分支）

| 磁贴 | 修复前 (`91e7e1a-before-nopatch`) | 修复后 (`e4a2bed`) |
| --- | --- | --- |
| projects | `/projects` | `/projects` |
| running | **`/today`** ❌ | `/projects` |
| shortfall | **`/today`** ❌ | `/projects` |
| pending | **`/activity`** ❌ | `/projects` |
| produced | **`/today`** ❌ | `/projects` |
| releases | `/deliveries`（「被挡住 1」无出口） | `div` + 内层 `/deliveries` 与出口链接 |

### 受控输入（单项目深链分支，`--stub-single-project`）

| 磁贴 | 修复前 | 修复后 | issue 要求 |
| --- | --- | --- | --- |
| running / shortfall | `/today` | `/p/1/runs` | ✅ `/p/{id}/runs` |
| pending | `/activity` | `/p/1/review` | ✅ `/p/{id}/review` |
| produced | `/today` | `/p/1/data` | ✅ `/p/{id}/data` |
| releases 的「被挡住 N」 | 无出口 | 独立出口 → `/p/1/releases` | ✅ |

两组证据的 `verdict.selfLinkCount` 均由 **3 → 0**，`pendingWrongTarget` 由 `true → false`，
`blockedWithoutExit` 由 `true → false`。`evidence-check` 对两组前后截图均返回 `EVIDENCE OK`。

## 守卫

`test/l15_issue197_remediation.mjs` 的 `problemsWithOverviewTileTargets(todaySrc, apiTypes)`：

- 断言磁贴行不得出现 `href={studioPath('today')}`；
- 断言磁贴走 `overviewProjectHref(`（不得退回硬编码路径）；
- 断言存在 `data-overview-blocked-link=`（「被挡住 N」的独立出口）；
- 断言 `WorkspaceOverview` 下发 `scopedProjectIds`（深链所需项目 ID 必须来自服务端）。

附 2 条变异自证（让磁贴退回指向当前页 / 删掉「被挡住」独立出口），
本轮复跑 `node test/l15_issue197_remediation.mjs`：`[PASS] #205 ...` 与两条变异均通过。

## 复现/验证命令

```bash
# 真实数据（多项目回退）
node docs/audit/issue-205/repro.mjs --phase before
node docs/audit/issue-205/repro.mjs --phase after

# 受控输入（单项目深链）
node docs/audit/issue-205/repro.mjs --phase before --stub-single-project
node docs/audit/issue-205/repro.mjs --phase after  --stub-single-project

# 机器门禁
scripts/issue-bot/preflight.sh evidence-check \
  docs/audit/issue-205/before-single-project-today-tiles.png \
  docs/audit/issue-205/after-single-project-today-tiles.png
```

> 脚本默认拒绝覆盖已提交的证据（与 issue-200/212 的同一约定）；要重采必须显式传 `--force`。

## 本轮未改动业务代码

本轮只新增取证脚本与证据（`docs/audit/issue-205/`），产品代码零改动 ——
复跑已合并的修复并确认缺陷消失，符合 SOP §8.3「已彻底解决、只是忘了关 issue」的处置路径。
