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

**CD**（`.github/workflows/cd.yml`，打 `v*` tag 或手动触发）两个 job：

| job | 内容 |
|---|---|
| publish | `docker compose build` → `docker compose push` 到 GHCR（`ghcr.io/1420970597/llm-{api,worker,web-user}`），随后校验三个包为 public |
| deploy | SSH 到主机 `docker compose pull` + `up -d --no-build`，部署后健康检查，失败回滚到上一个镜像 tag |

镜像 tag 取 `IMAGE_TAG`（tag push 时为 tag 名，手动触发时为输入 ref）。**主机不再编译，不需要 Go 工具链**；部署状态记录在主机仓库的 `.deployed-tag` / `.previous-tag`。回滚仅在 deploy 步骤本身失败时触发。

**GHCR 包默认是 private**（即使仓库 public 也不继承可见性），而主机是匿名拉取。`publish` job 会尝试自动设为 public，失败会直接报错；需手动到 `https://github.com/users/1420970597/packages/container/<pkg>/settings` 改一次 Public，之后不再变。

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

## 文档规范

文档必须采用简体中文撰写，格式美观，必须图文并茂！

## 开发规范

- 1. 在完成某一工作步骤后，需要更新本地容器，并且规范提交至git。**注**：`main` 分支有 pre-tool-use 钩子保护，禁止直接提交；提交一律走特性分支 + PR（开 PR 不需要征求批准，不破坏 YOLO 模式）。
- 2. 代码撰写必须具备鲁棒性，注释采用简体中文。
- 3. 如果遇到功能限制，优先向 pi 官方插件市场（`https://pi.dev/packages`）查询相关缺失能力是否具备成熟的插件，如有则自动安装并继续开发（需要注意！不得破坏当前的全自动yolo模式）。**注**：插件是 npm 包，`pi install npm:<包名>` 安装后写入 `~/.pi/agent/npm/` 与 `settings.json`，但**扩展在会话启动时加载，当前会话不会立即生效**——安装后需重启会话才能使用，因此本步骤不是“装完即可继续”的闭环。另外 pi 官方明确警告 *Pi packages run with full system access*，自动安装等于自动执行任意代码；仅安装官方市场内、且已阅读过源码的包。
- 4. 这是一个大型的项目，用户要求你具备完全的自我开发能力，所以考虑问题要充分，不得以“最小mvp”、“假数据注入”等理由进行自我欺骗，必须采用真实接口数据，测试全程使用python进行接口调试，需要放到test目录下，并且写清楚测试项。**注**：真实 LLM 接口调用需要用户提供可用的 `provider.APIKey` 与 `provider.BaseURL`（`internal/llm/openai_client.go`、`domain_generator.go` 均有非空校验）；缺 key 时这部分测试无法运行，属于输入缺失而非能力缺失。基础设施（Postgres/Redis/MinIO/HTTP）可用真实容器测试，不属此列。宿主机没有 Go 工具链，Go 相关命令一律走 docker；python 测试若需依赖 Go 服务同样走 docker。
- 5. 当收到用户的开发需求的时候，需要你进行持续开发，先理解问题，再列出计划（todo），再按照计划进行开发、测试，最后进行自我审查（如有问题则将问题项进行分析、计划、开发、测试，循环直到全部解决），最后审视当前的完整项目，列出未来的功能计划（前提是必须完整的完成了用户让开发的内容）。**注**：自我审查循环需设终止条件——同一问题连续 3 轮未收敛、或遇到外部阻塞（缺 key、缺凭证、需用户决策）时停止循环，向用户报告阻塞点，不得无限循环。
- 6. 如果用户输入“按照todo完成全部开发工作”，则需要你进行不少于3轮的自我迭代，即迭代至少3轮开发规范-5。
- 7. 无论你是什么智能体框架，本项目使用的模型服务（即你的llm服务）是无限token的，必须采用最主动的迭代方式！**注**：意图是“不因成本顾虑而偷懒、主动迭代”，此点完全执行；但需澄清字面含义——任何 LLM 都有上下文窗口上限，长会话必然触发压缩。因此本条的准确执行方式是：在上下文预算内采用最主动的迭代策略，不因 token 成本放弃必要的验证与探索。
- 8. git提交署名只能保留系统git配置的名称！**注**：即不得添加 `Co-authored-by`、`Generated with` 等任何额外署名行。
