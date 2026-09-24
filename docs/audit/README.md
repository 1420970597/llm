# 甲方验收审计证据包（截图 + 采集数据）

本目录是**甲方视角验收审计**的原始证据，供 GitHub Issue 引用。
所有截图均由真实 Chromium（Playwright，headless）在真实容器栈上采集，
非设计稿、非 mock。

## 环境

| 项 | 值 |
| --- | --- |
| 前端 | `http://127.0.0.1:3210`（`llm-web-user-1`，`docker compose up -d --build`） |
| 后端 | `llm-api-1` / `llm-worker-1` / `llm-postgres-1` / `llm-redis-1` / `llm-minio-1` |
| 源码 | `main` @ `3838067`（`feat(web): complete Atelier legacy capability coverage (#166)`） |
| 视口 | 桌面 `1600×1000`；移动 `390×844` |
| 账号 | `admin@company.com`（管理员，工作区 owner） |
| 采集时间 | 2026-09-24 |

> `version.json` 返回 `"version": "unknown"`（构建未注入 `GIT_SHA`），
> 因此镜像与源码的对应关系**无法自证** —— 这一点本身也是本次审计的发现之一。

## 文件

- `screenshots/` —— 111 张全页截图，命名 `<序号>-<页面键>.png`
- `capture.json` —— 全路由采集结果：可见文案、标题、按钮、空状态、console/pageerror/失败请求
- `functional.json` —— 功能与流程深挖的观察记录

## 采集脚本（可复现）

```bash
cd /root/llm
docker compose up -d --build
node test/audit/capture.mjs      # 全路由截图 + 结构化采集
node test/audit/functional.mjs   # 深链、边界、无障碍、移动端
```

## 数据基线

采集时数据库：项目 1 个（本次审计通过向导真实创建）、批次 1 个（失败 12/12）、
旧数据集 58 个、模型连接 11 条、方案 0、发布 0、质量实验 0。
