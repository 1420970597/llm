# Issue #197 第 11 条 §A — 覆盖矩阵 `m × n × z` 结构公式算术不自洽（第 1 轮）

## 结论

**已修复。** 覆盖矩阵的「数据集结构」公式此前渲染为 `m 1 × n 2 × z 4 = 4`
（`1 × 2 × 4 ≠ 4`）—— `z` 取的是**全部配额之和**，结果又复用了同一个数。
修复后为 `m 1 × n 2 × z 2 = 4`，算术成立且结果等于本版本可产出量。

本轮只处理 #197 中**仍未收口**的这一点（第 11 条 §A）。第 11 条的其余诉求
（拖拽式工作流画布、参数 schema 先行）在上一轮已作为**设计决定**记录（PR 声明 R2），
不属于本轮范围。

## 缺陷形态与判定

同一页面上，公式自称「领域数 × 方向数 × 每个方向的题数」：

```text
m 1 × n 2 × z 4 = 4
```

- `m=1`（1 个冷链领域）、`n=2`（方向一、方向二）；
- 每个方向配额都是 2，合计 4，但 `z` 显示的是 **4（Σ配额）**而不是每方向题数 2；
- 等式右侧也复用 4，于是 `1 × 2 × 4 ≠ 4`。

这不是文案瑕疵：第 11 条的原话包含「**先明确对应流程的参数结构再做设计**」，
而一个自己都算不通的结构公式恰好在「参数结构」这一点上失效 ——
用户无法用它预判「改方向数或配额会不会影响产出量」。当各方向配额不等时误导更强。

**机器判据**（`repro.mjs`）：从页面读回 `m A × n B × z C = D` 四个数字，断言
`A × B × C === D` 且 `D ===` 服务端可产出量（Σ方向配额，`quota ≤ 0` 视为 1）。

## 实测读数

| 判定（机器事实） | 修复前 | 修复后 |
| --- | --- | --- |
| 公式文本 | `m 1 × n 2 × z 4 = 4` | `m 1 × n 2 × z 2 = 4` |
| `m × n × z == 结果` | **false** | **true** |
| 结果 == 可产出量(4) | true | true |
| `pageerror` / 5xx | 0 | 0 |

![修复前：公式 `m 1 × n 2 × z 4 = 4`，算术不成立](https://raw.githubusercontent.com/1420970597/llm/66ec6f95f2ffa66a8cac973651ea56fb59b43810/docs/audit/issue-197-11/before-coverage.png)

![修复后：公式 `m 1 × n 2 × z 2 = 4`，算术成立且等于可产出量](https://raw.githubusercontent.com/1420970597/llm/66ec6f95f2ffa66a8cac973651ea56fb59b43810/docs/audit/issue-197-11/after-coverage.png)

## 根因

`apps/web-user/src/studio/DocumentEditors.tsx` 的 `CoveragePayloadEditor` 内联计算：

```ts
const directionCount = domains.reduce(...)          // n
const capacity = domains.reduce(... Σquota ...)     // 被同时当作 z 与结果
...
m {domains.length} × n {directionCount} × z {capacity} = {capacity}
```

`m × n × z` **只有在「各领域方向数相同、且各方向配额相同」时**才是一个成立的乘积；
旧实现把「每方向题数 z」与「Σ配额（可产出量）」混为同一个数，于是当方向数 > 1 时
等式必然不成立。

## 改动

| 文件 | 改动 | 目的 |
| --- | --- | --- |
| `apps/web-user/src/studio/coverageStructure.ts`（新增） | 结构推导抽成无 React 依赖模块：`deriveCoverageStructure` / `formatCoverageFormula` / `describeCoverageFormula` | 让「公式算术是否成立」成为可直接调用的读数；口径与后端 `model.CoverageCapacity` 一致（`quota ≤ 0` 视为 1） |
| `apps/web-user/src/studio/DocumentEditors.tsx` | 删除内联的 `directionCount` / `capacity` 与手拼公式，改为消费模块 | 消除第二份口径；乘积成立才输出 `m×n×z = 结果`，否则并列读数 + 诚实说明 |
| `test/l15_issue197_remediation.mjs` | 新增 1 条结构断言 + 4 条真实模块断言 + 2 条变异自证 | 见下 |

**乘积不成立时不编造等式**：各方向配额不等（如 1 与 3）时输出
`m 1 领域 · n 2 方向 · 计划单元合计 4`，而不是硬凑一个算不通的 `m×n×z = 结果`。
这正是 §A 的缺陷形态，必须从根上避免。

## 验证（修复后）

同条件重跑（真实栈 `127.0.0.1:3210` + 真实 Chromium 1600×1000，`/p/p_1/coverage`）：

```text
[issue-197-11] phase=after
  公式=m 1 × n 2 × z 2 = 4
  m×n×z == 结果: true · 结果 == 可产出量(4): true
  判定: FIXED
```

修复前证据的取法：部署中的前端为修复前版本（`/version.json = a889b43`），
直接采 `before`；随后用 `GIT_SHA=$(git rev-parse HEAD)` 重建并 `compose up -d --build web-user`
（`/version.json = 7964dd8`）后采 `after`。前后**同栈、同账号、同视口、同路由、同数据**。

## 门禁与守卫

```text
go-gate.sh: gofmt clean · go vet clean · go build ok · go test ok（EXIT=0）
npm run build: 通过（tsc + vite）
node test/l15_issue197_remediation.mjs: 通过（20 条结构断言 + 29 条变异自证）
node test/l15_studio_shell.mjs: 通过
scripts/check-docs.mjs: 通过
evidence-check: EVIDENCE OK
```

守卫分两层（与 `l15_studio_wizard.mjs` 同一约定，默认路径不需要容器/浏览器）：

1. **源码层**：断言公式推导来自共享模块（`coverageStructure.ts`），
   并且**禁止**出现旧缺陷形态 `× z {x} = {x}`（同一个数当 z 与结果）。
2. **真实模块层**：用 esbuild 打包 `coverageStructure.ts`，从公式文本里
   重新读回四个数字并重算 `m×n×z == 结果`；另覆盖 `quota ≤ 0 视为 1`、
   非法乘积形态不编造等式、空覆盖容量为 0 三条边界。
3. **变异自证**（证明守卫非空转）：
   - 源码退回内联（改 import）→ 结构断言 FAIL；
   - 模块退回 `z = Σ配额` → 真实模块层的算术自洽断言复现原缺陷 `1×2×4≠4` 并 FAIL。

## 残留风险

- 本轮**未**解决第 11 条的其余诉求（拖拽式流程编排画布、参数 schema 先行的
  schema 定义）。它们是**产品定位级**的改造，上一轮已作为设计决定记录
  （`docs/prototypes/blueprint-workflow-rearchitecture/`），不属于本轮自动化范围。
- 第 6 条（不应强制关联历史版本、编辑后自动迭代）**仍维持现状**：`变更理由` 仍是必填、
  保存走 `expectedRevision` 乐观锁。这与并发安全存在取舍，与第 11 条 §A 无关，
  且「取消必填 / 自动生成变更说明」属未获批的产品决策 —— 沿用上一轮的记录。
- `deriveCoverageStructure` 对形态异常的历史 payload 采取「按后端口径取 1」的降级，
  不抛异常；这与后端 `CoverageCapacity` 的 `quota ≤ 0 → 1` 一致，但**没有**对
  「payload 根本不是覆盖结构」做告警（服务端 schema 校验已覆盖该路径）。
