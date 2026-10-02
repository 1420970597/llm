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

## 候选栈验证（本轮新增）

本轮在**合并了三条 PR（#240/#238/#234）的候选树**上重建了一套隔离栈
（`llm_candidate` 数据库 + `llm-web-user:0d7cfc6f`，端口 `:3310`），
用与 before **完全相同的脚本/选择器/视口/账号**重跑：

| 观察点 | main（before） | 候选（after） |
| --- | --- | --- |
| 连接表中名称为空的行 | **6** | **0** |
| 下拉里的空选项 | **6** | **0** |
| 下拉里被灰显不可选的选项 | **0** | **8** |
| 被标记「配置不完整」的行 | **0** | **8** |
| `providers.withConfigIssues` | **0** | **8** |

修复后下拉实测全量：`["自动服务-192538","未命名连接 #19","未命名连接 #18","未命名连接 #17",
"自动服务-506335","未命名连接 #15","未命名连接 #14","未命名连接 #13","hy3-judge",
"gpt-5.6-sol-judge","deepseek-v4.1-flash"]`。
**缺陷消失**（用户不再需要辨认无字选项）。
