# issue #203 第 3 轮：当前 `main` 缺陷活体取证

> 本轮**不新增代码修复**。修复本体已在 PR #234 分支上完成并取证，但该 PR 仍为 **OPEN**。
> 本目录证明**部署中的 `main`（`a889b43`）仍然把含未审阅内容的冻结范围称作「已接纳」**。

- 验证环境：真实栈 `127.0.0.1:3210`（`/version.json` = `a889b43` = 当前 `origin/main`）+ 真实 Chromium 1600×1000
- 复现脚本：`repro-main.mjs`（取自 PR #234 的 `repro-freeze-scope.mjs`，同选择器/视口/账号）
- 产物：`01-main-buggy-title.png` · `before-freeze-scope.json`
