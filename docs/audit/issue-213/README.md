# Issue #213 — 交付映射「必填」复选框无可访问名 + 发布表单只显示最后一条错误（复核轮）

## 结论

**已修复，本轮复核确认可关单。**

修复由 `91e7e1a`（PR #225，`fix(web): #214 第二轮 5 条未收口缺陷`）落地并已进入 `origin/main`；
issue 当时未被关闭，因此本轮的工作是**同条件复跑取证 + 守卫复核**，而不是再次修改代码
（与 #205 / #206 的处理形态一致）。

本轮**未改动业务代码**，产品代码零改动。

## 两条独立缺陷与判定口径

| 编号 | 缺陷 | 机器判据 |
| --- | --- | --- |
| A | 字段映射的 3 个「必填」复选框**没有一个有可访问名**（读屏只会念三个一模一样的「必填」，无法知道它属于哪一行） | 每个 `input[type=checkbox]` 的可访问名非空**且互不相同**（`distinct === count`） |
| B | 发布表单只用一个 `error` 字符串，**多字段错误时只显示最后一条**，且不定位到字段 | 字段级提示落到对应输入框上：`#intended-use-error` 存在，且 `#intended-use` 带 `aria-invalid` / `aria-describedby` |

## 实测读数

### A. 可访问名（Playwright `ariaSnapshot` = 「读屏会念什么」的权威读数）

| 行 | 修复前 (`91e7e1a-before-nopatch`) | 修复后 (`a889b43`) |
| --- | --- | --- |
| 1 | `checkbox "必填" [checked]` | `checkbox "把交付字段 question设为必填" [checked]` |
| 2 | `checkbox "必填" [checked]` | `checkbox "把交付字段 reasoning设为必填" [checked]` |
| 3 | `checkbox "必填" [checked]` | `checkbox "把交付字段 answer设为必填" [checked]` |
| 可区分数 | **1 / 3** | **3 / 3** |

![修复前：三个「必填」复选框的可访问名完全相同，读屏无法区分](https://raw.githubusercontent.com/1420970597/llm/30fd21457f7e7cbd6f3ff574f5b27938cb2b0cac/docs/audit/issue-213/before-mapping-a11y.png)

![修复后：每个复选框的可访问名带本行交付字段名](https://raw.githubusercontent.com/1420970597/llm/30fd21457f7e7cbd6f3ff574f5b27938cb2b0cac/docs/audit/issue-213/after-mapping-a11y.png)

> 可访问名是**不可见**的事实：只截产品界面的话前后两张图会完全相同，而
> `evidence-check` 会（正确地）判定「无法证明缺陷发生变化」。
> 因此这两张图把浏览器**真实计算出的**可访问名以琥珀色标注画在每一行下方，
> 标注内容全部来自脚本的 DOM 读数、图上明确声明「可访问性标注（非产品界面）」；
> 这是「把不可见的事实变成可对比的图」，不是用文字代替截图。

### B. 多字段错误

```json
// 修复前：两次提交都只有页底一行字，没有字段锚点
{ "emptySubmit": { "rangeAnchorCount": 0, "rangeErrorText": "" },
  "intendedUse": { "errorCount": 0, "ariaInvalid": null, "ariaDescribedBy": null } }

// 修复后：字段级提示落在对应输入框上
{ "emptySubmit": { "rangeAnchorCount": 1, "rangeErrorText": "发布范围不能为空：请选择要发布的内容版本" },
  "intendedUse": { "errorCount": 1, "errorText": "必须填写用途：数据卡要能说清这份数据用来做什么",
                   "ariaInvalid": "true", "ariaDescribedBy": "intended-use-error" } }
```

![修复前：错误只在页底一行，输入框上没有提示也没有 ARIA 关联](https://raw.githubusercontent.com/1420970597/llm/30fd21457f7e7cbd6f3ff574f5b27938cb2b0cac/docs/audit/issue-213/before-field-errors.png)

![修复后：「必须填写用途」渲染在用途输入框正下方，并带 aria-describedby](https://raw.githubusercontent.com/1420970597/llm/30fd21457f7e7cbd6f3ff574f5b27938cb2b0cac/docs/audit/issue-213/after-field-errors.png)

## 修复前证据的取法（同条件保证）

把 `apps/web-user/src/studio/pages/ReleasePages.tsx` 与
`apps/web-user/src/studio/DocumentEditors.tsx` **临时**换回 `91e7e1a~1`（修复前）的版本，
用**同一条** `npm run build` 重新构建前端产物并 `docker cp` 进正在运行的 `llm-web-user-1`，
采完**立即还原**并核对 `git status` 为空。因此前后是**同栈、同账号、同视口、同路由**，
唯一变量是那两份缺陷代码。`/version.json` 两侧分别为
`91e7e1a-before-nopatch` 与 `a889b43`（记在各自 JSON 的 `deployedVersion` 里）。

## 守卫

`test/l15_issue197_remediation.mjs` 的
`problemsWithAccessibleMappingAndFieldErrors(editorSrc, releaseSrc)`：

- 断言映射行的复选框带行内可访问名（匹配 `aria-label={`把${accessible}设为必填`}`）；
- 断言发布表单消费服务端 `fieldErrors`（`applyServerFieldErrors(`）且「用途」有字段级渲染
  （`fieldErrors.intendedUse`），并用 `aria-describedby` 关联。

附 2 条变异自证（摘掉行内可访问名 / 让发布错误退回单行总体提示）；
本轮复跑 `node test/l15_issue197_remediation.mjs`：`[PASS] #213 ...` 与两条变异均通过。

## 复现/验证命令

```bash
node docs/audit/issue-213/repro.mjs --phase before
node docs/audit/issue-213/repro.mjs --phase after

scripts/issue-bot/preflight.sh evidence-check \
  docs/audit/issue-213/before-mapping-a11y.png \
  docs/audit/issue-213/after-mapping-a11y.png
scripts/issue-bot/preflight.sh evidence-check \
  docs/audit/issue-213/before-field-errors.png \
  docs/audit/issue-213/after-field-errors.png
```

> 脚本默认拒绝覆盖已提交的证据（与 issue-200/205/212 的同一约定）；要重采必须显式传 `--force`。

## 残留风险

- **B 的第二种形态（同一次提交里两个字段同时非法）未单独取证**。修复后前后端都按字段
  分别渲染，且服务端返回结构化 `fieldErrors[]`；本轮取证覆盖的是 issue 描述的实测序列
  （先「范围为空」、再「用途为空」），即两次提交各自落到正确字段。若要覆盖「同一请求两条
  字段错误」，需要构造一个能让服务端同时返回两条字段错误的请求 —— 属可追加的强化项，
  不影响本 issue 的收口判定（原缺陷是「后者覆盖前者」，现在两条各自锚定）。
- 复选框的 `<label>` 结构未加 `htmlFor`/`id` 关联（用的是 `aria-label`）。当前实现已满足
  可访问名要求（`ariaSnapshot` 实测三项互不相同），因此未改结构；若后续要支持
  「点行内『必填』文字也能切换」，再补 `id`/`htmlFor` 即可。
