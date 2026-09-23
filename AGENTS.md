# AGENTS.md — llm-data-factory (Multi-Agent Operating Protocol)

企业级 LLM 长链思考训练数据工厂（Go 1.24 后端 + React/Vite 前端）。
本文件是所有接入本仓库的自主 Agent（Codex、Pi、Claude Code、OpenClaw 等）与各工作节点的**最高运行宪章**。

---

## 1. ⚠️ 运行环境强约束（违者必死）

> **宿主机未安装 Go 与 Node 环境。** 严禁直接在宿主机上尝试执行 `go`、`npm`、`npx` 等原生命令。

- **后端所有操作（检查、格式化、构建、测试）强制通过 Docker 容器执行**：

  ```bash
  # 统一后端完整验证命令（必须用 sh -c 避免 alpine login shell 丢失 PATH）
  docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh -c "gofmt -s -w apps internal && go vet ./... && go build ./... && go test -v ./..."
  ```

- **前端构建与类型检查**：在具有 node 环境的专用容器中执行 `npm run build`（底层包含 `tsc --noEmit` 与 vite build）。

- **全栈与数据库迁移验证**：

  ```bash
  make db-migrate-smoke          # 启动临时 Postgres 验证 SQL 迁移脚本
  make compose-up                # 本地全栈集成验证
  ```

---

## 2. 🔀 多机协同与分支安全协议（治理分支失控）

当前仓库严禁任何无序创建分支的行为。多台机器并行工作时，必须严格遵守以下分支流转生命周期：

1. **分支命名与绑定格式**：

   - 严禁随意创建未关联任务的分支。分支名必须严格遵循：
     - 新功能：`feat/TASK-<编号>-<英文简短描述>`
     - 缺陷修复：`fix/TASK-<编号>-<英文简短描述>`
     - 调研/设计/文档：`docs/TASK-<编号>-<英文简短描述>`

2. **作业前同步基线**：

   - 每次开工前，第一步必须执行变基拉取：

     ```bash
     git fetch origin main
     git checkout -b <规范分支名> origin/main
     ```

3. **分支清理与合并生命周期**：

   - **单分支单任务原则**：一个分支仅承载一个闭环的子任务。

   - 任务通过 PR 合入 `main` 后，执行机器必须立即清理远程与本地分支，严禁死分支残留：

     ```bash
     git push origin --delete <规范分支名>
     git branch -D <规范分支名>
     ```

---

## 3. 🛡️ 架构红线：严禁制造屎山代码（Anti-Spaghetti Code）

Agent 的常见劣习是“为了不改坏旧逻辑而在旁边新建一套逻辑”。本仓库对此零容忍：

1. **严禁平行文件与副本函数**：
   - 严禁创建 `xxx_v2.go`、`xxx_new.go`、`xxx_patch.go` 等投机文件。
   - 严禁在调用侧直接复制旧函数改名（如 `ProcessQuestion2`）。如遇需求变动，必须直接重构原函数，并同步修改所有调用方。
2. **分层架构职责边界**：
   - `apps/api/`：仅处理 HTTP 编解码、入参校验、鉴权与调用上下文装配。**严禁在 Controller/Handler 内部直接编写复杂的业务 SQL、状态机流转或外部 LLM 调度逻辑**。
   - `apps/worker/`：异步任务消费与 Pipeline 驱动。
   - `internal/store/`：所有数据库与缓存访问（基于 `pgx/v5`、`go-redis/v9`），必须抽入该层，严禁把 SQL 散落在 Controller 中。
   - `internal/llm/`：外部大模型调用与链式思考数据生成。
3. **禁止半成品代码（TODO Mocking）**：
   - 核心链路严禁留有 `// TODO: implement later`、`panic("unimplemented")` 或返回假数据的伪实现。

---

## 4. 🔄 任务全生命周期循环（Anti-Laziness Protocol）

针对 Agent “遇到复合任务只做 1~2 项就停滞”、“假装完成”的偷懒行为，必须执行状态机驱动循环：

1. **显式任务状态追踪表**：

   - 当接收到包含多项 TODO 的复合指令时，Agent **必须在每一次输出的最前端输出显式状态矩阵**：

     ```text
     [Task Tracker]
     - Total Tasks: N
     - Completed: [任务A (已自测通过), 任务B (已自测通过)]
     - Active Task: [任务C]
     - Remaining: [任务D, 任务E, ...]
     ```

2. **强制自动推进（Non-stop Loop）**：

   - **禁止提前停止**：只要 `Remaining` 列表中仍有待办项，Agent 严禁向用户汇报“已完成部分”或停下来询问“是否继续”。必须自动切换至下一个 Active Task 并继续执行。
   - 只有当 `Remaining` 为 0 且所有验证命令退出码为 0 时，才允许向用户输出最终总结。

3. **自测闭环要求**：

   - 全仓库目前处于缺乏测试的状态。**任何新增或重构的 Go 业务模块，必须在同目录配套 `*_test.go`**，并且测试用例必须覆盖至少一个正常路径和一个边界/异常路径。

---

## 5. 📑 调研、设计与原型任务规范（必须配套图文并茂的 Markdown 文档）

任何包含“竞品调研”、“系统设计/技术方案”、“页面原型设计”的工作，**严禁仅在终端打印散装文字**，必须在 `docs/` 下同步生成或更新配套的 Markdown 交付文档。

### 5.1 存放路径规范

- 调研报告：`docs/research/YYYY-MM-<topic>.md`
- 架构/技术方案设计：`docs/architecture/<system-or-module>.md`
- UI/交互与原型设计：`docs/prototypes/<feature-name>.md`

### 5.2 “图文并茂”的硬性判定标准（违者判定为未完成）

纯文本罗列、缺乏结构化视图的文档一律打回。交付文档必须包含以下要素：

1. **架构与逻辑图（必须使用原生 Mermaid 代码块渲染）**：
   - **数据流与时序**：使用 `sequenceDiagram` 绘制全链路时序（从前端交互到后端 API、Worker、Store、LLM 的完整流转）。
   - **状态机流转**：涉及复杂任务状态的，使用 `stateDiagram-v2` 绘制所有状态跳变与边界异常。
   - **系统关系/模块分层**：使用 `graph TD` 或 `flowchart LR` 绘制模块依赖图。
2. **对比与评估矩阵表（Markdown Tables）**：
   - 调研类文档必须包含多维评估矩阵表，对比不少于 3 个市面成熟竞品，横向涵盖 6 个以上核心维度（吞吐架构、扩展复杂度、数据标准、容错与重试、自研成本、依赖风险）。
3. **高保真原型布局图（UI / 原型类专属要求）**：
   - **必须提供 ASCII/Markdown 字符线框图**（包含 Header、Sidebar、Content 区域、表单字段、关键操作按钮的完整空间分布）。
   - **必须涵盖 5 大界面状态定义表**：
     1. `Default`（常规完整数据态）
     2. `Loading`（骨架屏/加载指示设计）
     3. `Empty`（缺省状态文案与行动倡导按钮）
     4. `Error`（网络错误/校验失败 Toast/Banner 交互设计）
     5. `Edge-Case`（长文本截断、极值数据溢出等适配规则）
4. **配图与视觉参考标准**：
   - 引用现有界面的对比分析时，在文档中以引用格式规范说明参考来源与界面示意；涉及代码原型实现的，必须在 `apps/web-user` 中同时生成可实际预览的组件。

---

## 6. 🏁 最终交付检查清单 (Definition of Done)

在提交 Git Commit 或汇报任务完成前，执行机器必须在命令行依次跑通以下流水线：

```bash
# 1. 代码格式化与规范检查
docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh -c "gofmt -s -w apps internal && go vet ./..."

# 2. 编译与单元测试验证
docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh -c "go build ./... && go test -v ./..."

# 3. 前端类型安全检查（涉及前端改动时）
npm run build

# 4. 文档完整性自检（涉及调研/设计/原型时）
# 确认 docs/ 下已生成对应的 .md 文件，且内含完整的 Mermaid 流程图与数据矩阵表格

# 5. 提交规范
git add .
git commit -m "<type>(<scope>): <简明中文描述本次改动的核心改动>"
```

**Git 提交类型（Conventional Commits）**：

- `feat`: 新增业务功能
- `fix`: 修复具体缺陷（必须说明根因）
- `refactor`: 重构现有架构（不改变外部行为）
- `test`: 补齐或修复测试用例
- `docs`: 架构方案、调研报告、图文原型或设计文档更新
