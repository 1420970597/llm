# issue #203 第 3 轮：当前 `main` 缺陷活体取证

> 本轮**不新增代码修复**。修复本体已在 PR #234 分支上完成并取证，但该 PR 仍为 **OPEN**。
> 本目录证明**部署中的 `main`（`a889b43`）仍然把含未审阅内容的冻结范围称作「已接纳」**。

- 验证环境：真实栈 `127.0.0.1:3210`（`/version.json` = `a889b43` = 当前 `origin/main`）+ 真实 Chromium 1600×1000
- 复现脚本：`repro-main.mjs`（取自 PR #234 分支上的复现脚本，同选择器/视口/账号）
- 产物：`01-main-buggy-title.png` · `before-freeze-scope.json`

## 候选栈验证（本轮新增）

本轮在**合并了三条 PR（#240/#238/#234）的候选树**上重建了一套隔离栈
（`llm_candidate` 数据库 + `llm-web-user:0d7cfc6f`，端口 `:3310`），
用与 before **完全相同的脚本/选择器/视口/账号**重跑：

| 判定项 | main（before） | 候选（after） |
| --- | --- | --- |
| 冻结请求体 | `fromFilter:{}`（无范围意图） | `fromFilter:{"reviewStatus":"all"}` |
| 候选页区块标题 | 「发布范围（**已接纳**的内容版本）」 | 「发布范围（快照 15：已接纳 2 条 · 未审阅 7 条）」 |
| 服务端 `composition` | `null`（不下发） | `{accepted:2, pending:7, quarantined:0, conflict:0}` |
| 缺陷判定 | **成立** | **不成立** |
