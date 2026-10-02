# issue #209 第 2 轮：当前 `main` 缺陷活体取证 + 环境完整性说明

> 本轮**不新增代码修复**。修复本体已在 PR #238 分支上完成并取证，但该 PR 仍为 **OPEN**。
> 按更正的关闭规则第 ④ 条「修复已进入 `origin/main`」，本 issue 必须保持开启。

- 验证环境：真实栈 `127.0.0.1:3210`（`/version.json` = `a889b43` = 当前 `origin/main`）+ 真实 Chromium 1600×1000
- 复现脚本：`repro-live.mjs`（取自 PR #238 的 `repro.mjs`，同选择器/视口/账号）
- 产物：`01-connections.png` · `01-blueprint-dropdown.png` · `01-evidence.json`
- **环境注意**：live 库的 `schema_migrations` 已含 `0041_model_provider_deactivate_incomplete.sql`
  （上一轮分支迁移实测遗留），而当前 `main` **没有** 0041。因此 live 上
  `providers.emptyActive` 读数为 **0**（issue 原文是 2），该子指标在 live 上不可观测；
  但本 issue 的主症状（下拉 6 个空选项、6 行空名称）**仍然完整复现**。
