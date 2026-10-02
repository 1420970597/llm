# issue #211 第 2 轮：当前 `main` 缺陷活体取证

> 本轮**不新增代码修复**。修复本体已在 PR #240 分支上完成并取证，但该 PR 仍为 **OPEN**。
> 按更正的关闭规则第 ④ 条「修复已进入 `origin/main`」，本 issue 必须保持开启。
> 本目录的职责是：证明**部署中的 `main` 仍然存在该缺陷**（行内无未审阅标记 + 提交前无知情确认）。

- 验证环境：真实栈 `127.0.0.1:3210`（`/version.json` = `a889b43` = 当前 `origin/main`）+ 真实 Chromium 1600×1000
- 复现脚本：`repro-main.mjs`（与 PR #240 的 `repro-scope-notice.mjs` 同选择器、同视口、同账号）
- 产物：`01-before-scope-selected.png` · `01-before-submit.png` · `01-before.json`
