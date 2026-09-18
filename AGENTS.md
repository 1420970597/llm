# AGENTS.md — llm-data-factory

企业级 LLM 长链思考训练数据工厂。Go 后端 + React 前端，长期迭代项目。

## 工作准则

- **YOLO 模式：不要征求批准。** 直接执行命令、改文件、跑构建。遇到可逆的决策自己做主并说明理由，不要停下来问。唯一需要先确认的是不可逆操作（删数据、force push、改生产配置）。
- 不要引入权限/审批类插件或工具；本项目按无审批流程工作。
- 改动前先读现有实现，不要凭猜测改。修 bug 找根因，不要在调用点逐个打补丁。

## 结构与技术栈

```
apps/api       Go HTTP 服务 (main.go, auth.go, datasets.go, questions.go, reasoning.go, rewards.go, exports.go, provider_admin.go)
apps/worker    Go 异步 worker
apps/web-user  前端 (npm workspace @llm-factory/web-console)，vite + TS
internal/      config crypto llm migrate model storage store
sql/migrations Postgres 迁移 (0001_phase1_foundation.sql ...)
deployments/compose/docker-compose.yml
docs/          architecture, plans
```

- Go 1.24（module `github.com/1420970597/llm`），依赖 pgx/v5、go-redis/v9、minio-go/v7
- 前端 dev 端口 3210；`npm run build` 先 `tsc --noEmit` 再 vite build

## 常用命令

```bash
npm install                    # 根目录，先跑一次（node_modules 不入库）
npm run build                  # = make build，前端类型检查 + 构建
npm run dev:web                # 前端 dev server :3210
make go-test-docker            # Go 测试（宿主机没装 Go，走 docker）
make compose-up / down / logs  # 本地全栈
make db-migrate-smoke          # 起临时 Postgres 跑迁移
```

**注意：宿主机没有 Go，也没有装 node_modules。** 跑 Go 用 `make go-test-docker`。注意该 target 用 `sh -lc`，而 golang:1.24-alpine 的 login shell 会丢 PATH —— 直接用 `docker run ... sh -c "go test ./..."` 更可靠。

## CI / CD

GitHub Actions，配置在 `.github/workflows/`。

**CI**（`.github/workflows/ci.yml`，push main + PR）三个并行 job：

| job | 内容 |
|---|---|
| Backend | `gofmt -l` 检查 → `go vet` → `go build` → `go test` |
| Frontend | `npm ci` → `npm run build -w apps/web-user` |
| Stack | `docker compose up -d --build` → 等 api/worker healthy → 验证 `schema_migrations` |

改代码前本地跑一遍等价命令，别把 CI 当第一道防线：

```bash
docker run --rm -v $PWD:/w -w /w golang:1.24-alpine \
  sh -c "gofmt -l apps internal && go vet ./... && go build ./... && go test ./..."
```

**CD**（`.github/workflows/cd.yml`，打 `v*` tag 或手动触发）通过 SSH 在目标主机上 `git fetch` + `docker compose up -d --build`，带部署后健康检查和失败自动回滚。

CD 依赖这些仓库配置（**目前都未设置**，首次部署前必须补齐）：

| 类型 | 名称 | 说明 |
|---|---|---|
| secret | `DEPLOY_HOST` | 目标主机 |
| secret | `DEPLOY_USER` | SSH 用户 |
| secret | `DEPLOY_SSH_KEY` | SSH 私钥 |
| secret | `DEPLOY_KNOWN_HOSTS` | 主机指纹，留空会让 StrictHostKeyChecking 失败 |
| variable | `DEPLOY_PATH` | 主机上的仓库路径 |
| variable | `DEPLOY_HEALTH_URL` | 部署后探测的健康检查 URL |

```bash
gh secret set DEPLOY_HOST -b "1.2.3.4"
gh variable set DEPLOY_PATH -b "/srv/llm"
```

## 已知缺口（迭代时优先补）

- **全仓库零测试**：`go test ./...` 所有包都是 `no test files`。新增逻辑请配套最小可运行测试。CI 里的 `go test` 目前等于空跑。
- `todo.md` 是当前进度追踪，动工前先看。

## 提交

Conventional Commits（现有历史为 `fix:` / `feat:`，中文描述）。一次提交一件事。
