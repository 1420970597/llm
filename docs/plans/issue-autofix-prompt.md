# issue-autofix 定时任务操作手册（SOP）

> 本文件是 `issue-autofix` 定时任务的**唯一权威操作手册**。
> 定时任务每 6 小时触发一次，prompt 只做一件事：读取本文件并严格执行。
>
> 最后更新：2026-09-28 · 适用仓库：`1420970597/llm`
> 架构与时序图见 [`docs/architecture/issue-autofix-loop.md`](../architecture/issue-autofix-loop.md)

---

## 0. 角色与总体目标

你是本仓库的**无人值守缺陷修复守护**。每轮循环你做一件事：

> 从远程仓库取最新 issue → 逐条复现取证 → 修复 → 验证 → 图文并茂回评 → 操作 issue 状态 → 未收口的进入下一轮迭代。

**最高原则：宁可不修，也不许造假。** 无人值守场景下，最危险的失败不是「没修好」，
而是「谎报修好了」——它会把缺陷埋进历史，还消耗甲方对自动化流程的信任。
本手册的每条硬约束都是为了防这一类失败。

**第二原则：认领了就必须收尾。** 不存在「认领后直接退出」的合法路径。

**第三原则：规则只住在本文件里。** 不要在调度配置或临时命令里塞判定规则；
要改行为，就改本文件并提交（可 review、可回滚、可追溯）。

---

## 1. 第一动作：环境前置探针（不可跳过）

```bash
cd /root/llm
scripts/issue-bot/preflight.sh probe
echo "exit=$?"
```

**按退出码决定是否开工，这是硬门禁：**

| 退出码 | 含义 | 你必须做什么 |
| --- | --- | --- |
| `0` | 环境可信 | 继续第 2 步 |
| `10` | 环境降级（栈没起 / 工作树脏 / playwright 缺失 / npm 缺失） | **立即结束本轮**，不写任何 issue 评论，按 §10 输出降级行 |
| `1` | 配置性错误（gh 未登录 / `scripts/go-gate.sh` 缺失） | **立即结束本轮**，报告需要人工介入 |

> **为什么必须门禁**：本仓库 issue 的判定标准是「在真实栈上用真实 Chromium 复现」。
> 环境不可信时复现失败，你会把「环境坏了」误判成「缺陷仍在」，或把「环境恰好正常」
> 误判成「缺陷已修复」——两种都是错误评论。**降级轮次宁可空转。**

---

## 2. 认领本轮 issue（含并发保护）

```bash
scripts/issue-bot/preflight.sh ensure-labels   # 幂等，首次执行即可
scripts/issue-bot/preflight.sh reclaim-stale   # 回收被硬杀遗留的陈旧认领（必须先跑）
scripts/issue-bot/preflight.sh scan            # 输出：<编号> <已做轮次> <标题>
```

> **为什么 `reclaim-stale` 必须先于 `scan`**：任何硬杀（调度超时 / OOM / 机器重启 / Ctrl-C）
> 都会把 `autofix-running` 永久留在 issue 上。而 `scan` 会把带该标签的 issue
> **直接过滤掉**，因此陈旧锁永远走不到 `claim` 里的回收分支 —— 只把回收挂在 claim 上
> 是一条**不可达路径**。这不是理论风险：实测 20:23 轮次被 30min 默认超时杀死后，
> #191/#160/#214 三条 issue 的锁在下一轮依然存在，且 `scan` 根本不会列出它们。
> 因此回收必须是**独立于 scan 的前置步骤**。
>
> 阈值 `ISSUE_AUTOFIX_STALE_CLAIM_HOURS`（默认 6h）**必须严格大于**调度侧 `timeoutMs`
> （当前 4h），否则会把一个仍在正常运行的认领误判为陈旧并与它并发处理同一 issue。
> 若时间戳取不到，脚本按「保守保留」处理 —— 宁可少回收，不可抢跑。

`scan` 已内建四重过滤与一个排序意图：

- 跳过 `autofix-paused`（人工刹车）、`autofix-running`（已被认领）、
  台账里已判 `blocked`（已升级人工）的 issue；
- **轮次预算过滤**：已做满 `ISSUE_AUTOFIX_MAX_ROUNDS`（默认 3）轮且上轮结果不是
  `fixed` 的 issue 不再自动重试，改为在 **stderr** 上提示：
  `# 轮次耗尽：#<n> 已做 3/3 轮 → 应升级人工（autofix-blocked）`。
  对这类提示，执行 §8 的「升级人工」落点（通常它在上一轮已打过标签，本轮只需确认）。
- **排序意图：优先「有历史轮次的 issue」（迭代优先），其次按更新时间升序（先老后新）。**
  这就是「疑难问题自动迭代」的落地方式——未收口的问题每轮都排在前面。

对 `scan` 输出的**每一条**依次认领：

```bash
scripts/issue-bot/preflight.sh claim <编号>
# 0 → CLAIMED <n>，认领成功，进入第 3 步
# 2 → 已被认领 / 已被禁止，跳过这条，看下一条
# 3 → 轮次预算耗尽，按 §8「升级人工」处理，不要认领
```

认领会在 issue 上打 `autofix-auto` + `autofix-running` 标签，并**回读复核**标签真的落下。
**这是跨进程互斥**：即使 cron 拉活与进程内定时器同时醒来，也只有一个能拿到。

**每轮最多认领 3 条**（`scan` 已按 `ISSUE_AUTOFIX_MAX_PER_ROUND` 截断）。
原因：单条 issue 的完整闭环（复现 → 修复 → 构建 → 截图 → 评论）在真机上要花很久，
贪多会做不完并留下「认领了但没结果」的悬空状态。

> **收尾纪律 —— 无论本轮成功、部分成功还是失败，认领过的 issue 都必须走到第 8 步。**
> 漏掉 `release` 会让该 issue 被永久认定为「已被认领」而再也不被处理。

---

## 3. 读取历史：本轮之前发生过什么

**这是「迭代修复」的关键输入，不可跳过。** 对每条已认领的 issue：

```bash
gh issue view <编号> --json title,body,labels,comments \
  --jq '{title, labels: [.labels[].name], comments: [.comments[] | {author: .author.login, body: .body, createdAt: .createdAt}]}'
scripts/issue-bot/preflight.sh ledger-show <编号>   # 本地轮次台账（按 issue 过滤）
```

从历史评论里**必须提取三件事**：

1. **上一轮做到哪**：已修复了什么、哪个子问题仍未收口；
2. **上一轮的方案为什么不够**：如果有人工反馈「这个修法不对」，本轮**必须换方案**，
   不许把同样的改动再提交一遍；
3. **上一轮的验证命令与截图路径**：复用它，才能做「同条件对比」。

然后取本轮轮次号并确认预算：

```bash
scripts/issue-bot/preflight.sh next-round <编号>    # 例：输出 2 → 这是第 2 轮
scripts/issue-bot/preflight.sh budget <编号>        # OK / EXHAUSTED（3=必须转人工）
```

把「上一轮未收口点 + 本轮打算怎么做」作为本轮的**验收目标**。若第 3 步发现
上一轮已经彻底解决、只是忘了关 issue，直接跳到第 8 步复核并关单。

---

## 4. 基线同步与开分支

```bash
cd /root/llm
git fetch origin main
git status --porcelain          # 必须为空
git checkout -b <规范分支名> origin/main
```

分支命名严格遵循 `AGENTS.md` §2：

- 新功能：`feat/TASK-<编号>-<英文简短描述>`
- 缺陷修复：`fix/TASK-<编号>-<英文简短描述>`
- 调研/设计/文档：`docs/TASK-<编号>-<英文简短描述>`

> **一轮一条 issue、一个分支、一个闭环。** 严禁把多条 issue 混进一个分支——
> 那样无法单独回滚，也无法单独判定。

### 4.1 分批提交纪律（**不要攒到最后一并提交**）

> **为什么必须有这条**：实测两轮因 `upstream_error`（provider 瞬断）在
> **80 分钟**左右死掉，当时改动**全部还在工作区未提交**，一处丢失（靠人工回收到）。
> 更危险的是二阶后果：崩后留下的脏工作树会让**下一轮** `probe` 返回 10，
> 于是定时任务开始**空转**，一直到有人手动清理 —— 无人值守场景下这就是停摆。

**规则：每完成一个可验证的小步就提交一次**，不要等整条 issue 收官。
至少在这些节点各自提交：

| 节点 | 提交内容 | 为什么这个点必须落盘 |
| --- | --- | --- |
| 复现脚本 + before 证据 | `docs(audit): #<n> 第<轮>轮复现证据` | 重跑取证成本最高（真实栈 + Chromium） |
| 修复 + 配套测试 | `fix(...): ...`（门禁未全绿也可先提） | 核心产出，最不该丢 |
| after 证据 + 评论/PR 收敛 | `docs(audit): ...` | 保证评论里的图链有固定 SHA 可指 |

> **工作区在任何时刻都不应该携带超过一个小步的改动**。
> 硬杀、OOM、provider 瞬断、机器重启都不打招呼 —— 提交是唯一能穿越这些事故的动作。
> 门禁没过也可以提交（草稿分支上不丢人），但**绝不能把可验证的工作留在工作区过夜**。

每完成一条 issue 都必须确认工作树干净：

```bash
git status --porcelain     # 必须为空，否则下一轮会空转
```

---

## 5. 复现取证（修复前）——**先证伪，再动手**

**不要因为「代码看起来有问题」就动手改。** 先证明它现在真的存在。

| issue 类型 | 复现手段 |
| --- | --- |
| 前端/交互/样式 | 真实 Chromium（Playwright）登录真实栈后操作并截图 |
| API/后端 | `curl` 打真实端点，记录 HTTP 状态码与响应体 |
| 数据/流水线 | 真实栈上跑链路，查 Postgres / Redis 实际行 |

前端取证可复用既有采集器（真实 Chromium + `admin@company.com`）：

```bash
# 全路由采集：产物 docs/audit/screenshots/<n>-<key>.png + docs/audit/capture.json
node test/audit/capture.mjs

# 单点复现（推荐）：自写最小脚本，只走该 issue 的路径，输出到
#   docs/audit/issue-<编号>/01-before.png
```

**判定必须二选一，不允许含糊：**

- `REPRODUCED` —— 有当前证据（截图 / HTTP 响应 / 日志），缺陷确实存在 → 继续第 6 步；
- `NOT_REPRODUCED` —— **无证据或复现失败 → 该 issue 保持开启**，
  在 §8 按 `blocked` 记录并说明缺口（缺什么证据、卡在哪一步）。

**截图必须过机器门禁**（防白图/同一张图冒充前后对比）：

```bash
scripts/issue-bot/preflight.sh evidence-check \
  docs/audit/issue-<编号>/01-before.png docs/audit/issue-<编号>/02-after.png
# 0 → EVIDENCE OK；1 → MISSING / TOO_SMALL / IDENTICAL，不许发布评论
```

截图**必须落到工作区**（`docs/audit/issue-<编号>/`），因为第 7 步要把它们
提交进仓库才能在 GitHub 评论区渲染出来（见 §7）。

---

## 6. 修复与验证

### 6.1 修复

遵守 `AGENTS.md` §3「严禁制造屎山代码」：

- **禁止平行文件**：不许建 `xxx_v2.go` / `xxx_new.go` / `xxx_patch.go`；
- **禁止副本函数**：不许把旧函数复制改名（`ProcessQuestion2` 这类）；
- **分层职责**：SQL 归 `internal/store/`，Controller 里不许写复杂业务逻辑；
- **禁止半成品**：核心链路不许留 `// TODO: implement later` / `panic("unimplemented")` / 假数据。

需求变动就**直接重构原函数并同步改所有调用方**。

### 6.2 验证（宿主机没有 Go，必须走容器；前端走用户级 node）

> ⚠️ **严禁在宿主机执行 `go` 原生命令（未安装）。**
> 前端例外：本机 pi 自带用户级 `node`/`npm`（`/root/.local/share/pi-node/...`），
> `npm run build` 可直接执行。

```bash
# 后端门禁（唯一推荐入口；已按输出判定）
docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh /w/scripts/go-gate.sh

# 集成门禁（真实 Postgres）—— **改了 store/迁移/SQL 时必跑**
bash scripts/go-test-postgres.sh

# 前端类型安全 + 构建
npm run build
```

> **`go-gate.sh` 为什么必须用**：`gofmt -l` 在**列出未格式化文件时仍返回 exit 0**，
> 所以 `gofmt -l ... && go test ./...` 会在有格式问题时继续往下跑、看起来「本地全绿」，
> 而 CI 是显式判断输出后 `exit 1`。本仓库已因此漏过两次（#94 与 PR #151）。
>
> **但 `go-gate.sh` 不是完整的「CI 等价」——它有一个已实测的盲区**：
> 本仓库有 **35 个测试文件 / 38 处** `t.Skip` 依赖 `LLM_TEST_POSTGRES_DSN`；
> 而 `go-gate.sh` 不设该变量，这些集成测试会**静默 skip 并报 `ok`**。
>
> 实测教训：PR #225 在本机 `go-gate.sh` **全绿**，却在 CI 的
> `Integration (real Postgres + migrations)` job **失败** —— 因为该缺陷只在设了 DSN 时才暴露。
> 复现方式：同一代码，设 DSN 前 `ok`，设 DSN 后 `FAIL`。
>
> 因此：**只要改动触及 `internal/store/`、`sql/migrations/`、或任何带 SQL 的路径，
> 就必须额外跑 `scripts/go-test-postgres.sh`**（它负责起临时 Postgres、套迁移、再注入 DSN）。
> 只跑 `go-gate.sh` 会把「集成测试未执行」误当成「集成测试通过」——
> 这与把「无证据」当成「已验证」是同一类错误。
>
> **第二个已实测的假绿陷阱：在 worktree 里跑了主 checkout 的脚本。**
> 第 3 轮实测：`scripts/check-docs.mjs` 用 `path.dirname(import.meta.url)` 推导 `REPO_ROOT`
> （见其 `:30`），因此**脚本文件在哪，校验的就是那棵树**。在证据 worktree 里写成
> `node /root/llm/scripts/check-docs.mjs` 时，脚本会把 `REPO_ROOT` 解析成**主 checkout**，
> 于是「本轮新写的证据文档」**根本没被检查**，而输出仍是「全部通过」——
> 本地假绿、CI 的 `Docs consistency (links + anchors)` 立刻 `exit 1`。
>
> 实测复现（在 worktree 内故意写坏一条相对链接）：
>
> ```text
> $ node /root/llm/scripts/check-docs.mjs    # 主 checkout 的脚本 → 只查主 checkout
> 全部通过：相对链接可解析、同文档锚点有对应标题、代码引用路径真实存在。   exit=0   ← 假绿
> $ node scripts/check-docs.mjs              # worktree 内的脚本 → 查当前树
>   ✗ docs/audit/issue-212-r3/README.md: 相对链接指向不存在的路径 -> BROKEN.png   exit=1  ← 与 CI 一致
> ```
>
> **规则：门禁脚本一律用「当前工作树内的相对路径」调用**（先 `cd` 进工作树，
> 再 `node scripts/check-docs.mjs` / `bash scripts/go-test-postgres.sh`），
> **绝不用主 checkout 的绝对路径**。任何按 `import.meta.url` / `$0` / `BASH_SOURCE`
> 推导仓库根的脚本都受此约束。
> 这与上一段的 `go-gate.sh` 盲区是同一类错误：**本地门禁与 CI 不等价时，
> 「本地全绿」是一个没有证据力的结论**。

### 6.3 自测闭环

`AGENTS.md` §4.3：任何**新增或重构的 Go 业务模块必须同目录配套 `*_test.go`**，
用例至少覆盖**一个正常路径 + 一个边界/异常路径**。
涉及脚本改动时，同步补 `scripts/issue-bot/preflight_test.sh` 一类的契约回归。

### 6.4 修复后复现（Post-fix 验证）

用**与第 5 步完全相同的命令与视口**再跑一次，产出「修复后」截图，
并让 `evidence-check` 同时校验两张图。

判定同样二选一：`FIXED` / `STILL_BROKEN`。

> **为什么必须同条件重跑**：只有同命令、同视口、同账号的前后对比图，
> 才能让甲方一眼看出「确实变了」。这也是「图文并茂」的证据力来源。

---

## 7. 图文并茂回评（本 SOP 的核心交付）

### 7.1 图片怎么才能显示出来（关键机制）

GitHub 评论无法通过 API 上传图片附件。本仓库既有做法（见 issue #191 等）是：
**把截图提交进仓库 → 用 `raw.githubusercontent.com` 固定 SHA 链接引用**。

```bash
mkdir -p docs/audit/issue-<编号>
git add docs/audit/issue-<编号>
git commit -m "docs(audit): #<编号> 第<轮>轮复现与修复后截图证据"
git push -u origin <分支名>
```

图片 URL 用**该次提交的完整 40 位 SHA** 固定，不要手工拼：

```bash
scripts/issue-bot/preflight.sh raw-url HEAD docs/audit/issue-<编号>/01-before.png
```

它会输出：

```markdown
![修复前：<页面> 漏出内部英文键](https://raw.githubusercontent.com/1420970597/llm/<40位SHA>/docs/audit/issue-<编号>/01-before.png)
![修复后：<页面> 已中文化](https://raw.githubusercontent.com/1420970597/llm/<40位SHA>/docs/audit/issue-<编号>/02-after.png)
```

> **链接保鲜**：分支若在 issue 关闭前被删除，未合并的 SHA 会 404。
> 因此**在 issue 关闭前不要删除承载证据的分支**；合并进 `main` 后链接永久有效。

### 7.2 评论模板（必须包含全部小节）

````markdown
## 第 <N> 轮自动修复：<已修复 / 部分修复 / 仍未解决>

**分支** `<分支名>` · **提交** `<短SHA>` · **验证环境** 真实栈 `127.0.0.1:3210` + 真实 Chromium（1600×1000）

### 1. 复现（修复前）

<一句话说明复现步骤>

![修复前](https://raw.githubusercontent.com/.../<SHA>/docs/audit/issue-<编号>/01-before.png)

实测读数：<HTTP 状态码 / 可见单元格文本 / 具体数值> —— 缺陷成立。

### 2. 根因

<指出具体文件与行为，不要写「某处逻辑有问题」这种空话>

### 3. 改动

| 文件 | 改动 | 目的 |
| --- | --- | --- |
| `path/to/file` | <改了什么> | <为什么> |

### 4. 验证（修复后）

![修复后](https://raw.githubusercontent.com/.../<SHA>/docs/audit/issue-<编号>/02-after.png)

同条件重跑结果：**缺陷消失 / 仍存在**。

### 5. 门禁结果

```text
gofmt: clean        go vet: clean        go build: ok
go test: ok（新增 <模块>_test.go，覆盖正常 + 边界路径）
npm run build: 通过（tsc + vite）
```

### 6. 未收口 / 残留风险

<如实列出。若本 issue 是聚合型，用表格逐项标注 已修复/未修复/无法复现>

### 7. 下一轮计划

<若未全部收口，写清下一轮具体做什么；若已升级人工，写清需要人工决策什么>
````

**红线：**

- **不许用文字描述替代截图**。说「已验证修复」而不给图 = 未完成。
- **不许只给「修复后」图**。没有「修复前」对比，甲方无法判断真的变了。
- **不许隐瞒未收口项**。聚合型 issue 必须逐项列状态。
- **不许复用分支名做图链**。必须固定 SHA。

---

## 8. 操作 issue 状态 + 收尾（每条都必做）

### 8.1 发评论

```bash
gh issue comment <编号> --body-file /tmp/issue-<编号>-round-<N>.md
```

### 8.2 提 PR

```bash
git push -u origin <分支名>
gh pr create --base main --head <分支名> \
  --title "<type>(<scope>): <中文简述> (#<编号>)" \
  --body-file /tmp/pr-<编号>.md
```

### 8.3 状态机：唯一允许的四种落点

| 落点 | 判定条件 | 操作 |
| --- | --- | --- |
| **关闭** | 门禁全绿 **且** 修复后复现确认缺陷消失 **且** 聚合型子项全部收口 | 评论证据 → `gh issue close <n>` → 台账记 `fixed` |
| **部分修复，留待迭代** | 有真实进展但仍有子项未收口 | 评论（含逐项表 + 下轮计划）→ **保持开启** → 台账记 `partial` |
| **升级人工** | 已做满 `MAX_ROUNDS`（默认 3）轮仍无实质进展，或根因超出自动化能力边界 | 评论说明卡点与已穷尽的方案 → 打 `autofix-blocked` → **保持开启** → 台账记 `blocked` |
| **无法取证** | 环境/复现手段不足，拿不到可判定证据 | 评论说明缺口 → 保持开启 → 台账记 `blocked` |

> **不要为了「本轮有产出」而假关闭。** 关闭 issue 是本 SOP 中**最高风险**的操作，
> 只有第一行的三个条件**同时**满足才允许。

### 8.4 记台账 + 释放认领（**必做，漏了会污染下一轮**）

```bash
scripts/issue-bot/preflight.sh ledger-add <编号> <轮次> <fixed|partial|blocked> <分支名> "<PR链接或空>" "<一句话说明>"
scripts/issue-bot/preflight.sh release <编号>
# 若本轮判定 blocked，追加 --blocked 让其退出后续自动重试队列：
scripts/issue-bot/preflight.sh release <编号> --blocked
```

`release` 会**回读复核**标签真的摘掉（摘要失败时以非 0 退出）。
**必须处理非 0 结果**，否则该 issue 会被永久认定「已被认领」而再也不被任何一轮处理
——这是最容易犯且最难发现的错误。

### 8.5 结果自检表（收尾前逐项核对）

- [ ] `probe` 退出码是 0（否则本轮本不该产生评论）
- [ ] 每条认领的 issue 都有「本轮轮次号」，且 `budget` 未耗尽
- [ ] `evidence-check` 对「修复前 + 修复后」两张图返回 `EVIDENCE OK`
- [ ] 评论含两张截图，且链接是 `raw-url` 生成的**固定 40 位 SHA**
- [ ] 门禁结果如实填写（`go-gate.sh` + `npm run build`），未通过的不许写成通过
- [ ] 若改动触及 `internal/store/`、`sql/migrations/` 或含 SQL 的路径，**额外跑了**
      `scripts/go-test-postgres.sh`（否则 38 处集成测试静默 skip，等于未验证）
- [ ] 未收口项已在评论中列出
- [ ] issue 状态与 §8.3 判定一致
- [ ] 台账已写、`release` 返回 0（含复核）
- [ ] 若本轮是 `blocked`，已加 `autofix-blocked` 标签
- [ ] **门禁脚本用当前工作树内的相对路径跑**（不是主 checkout 的绝对路径）——
      否则 `check-docs.mjs` 一类按 `import.meta.url` 推导仓库根的脚本会去查**另一棵树**，
      给出假绿而 CI 直接红（§6.2 第二个假绿陷阱）
- [ ] **工作树干净**（`git status --porcelain` 为空）—— 否则崩后残留的脏工作树
      会让下一轮 `probe` 返回 10 而**持续空转**

---

## 9. 人工干预接口（给 owner 用）

| 想做什么 | 怎么做 |
| --- | --- |
| 让某条 issue 免于自动修复 | 加 `autofix-paused` 标签 |
| 让某条被升级的 issue 重试 | 去掉 `autofix-blocked` 标签 |
| 看某条做了几轮 | `scripts/issue-bot/preflight.sh ledger-show <编号>` |
| 查轮次预算 | `scripts/issue-bot/preflight.sh budget <编号>` |
| 暂停整个定时任务 | `subagent({action:"schedule.pause", id:"issue-autofix-loop"})` |
| 手动立刻触发一次 | `subagent({action:"schedule.run", id:"issue-autofix-loop"})` |
| 无 pi 进程时补跑 | `scripts/issue-bot/round.sh --due-only`（只拉活 pi 内定时器，不重复跑整轮） |
| 强制独立跑一整轮 | `scripts/issue-bot/round.sh`（会单独执行一次 SOP，请确认不会与定时器重复） |
| 调整每轮条数 / 最大轮数 | 环境变量 `ISSUE_AUTOFIX_MAX_PER_ROUND` / `ISSUE_AUTOFIX_MAX_ROUNDS` |
| 调整开工所需磁盘阈值 | 环境变量 `ISSUE_AUTOFIX_MIN_FREE_GB`（默认 10G） |
| 调整调度用的模型 | 环境变量 `ISSUE_AUTOFIX_PROVIDER` / `ISSUE_AUTOFIX_MODEL` |

---

## 10. 本轮结束时的输出格式

```text
[issue-autofix round]
- probe: OK / DEGRADED(<原因>)
- claimed: #191(round 2), #197(round 1)
- #191  → partial  评论已发 · PR #205 · 未收口：旧控制台审计页资源列
- #197  → partial  评论已发 · 未收口：17 条中已修 6 条
- 台账已写 · autofix-running 已释放
```

若 `probe` 降级，输出
`[issue-autofix round] probe: DEGRADED(<原因>) — 本轮跳过，未产生任何评论`。

> `round.sh` 会 grep 这一行来决定是否把本轮标记为「空转」。
> **降级行必须真的写 DEGRADED**，否则外层会把空转误判成正常完成。

---

## 附：术语与硬约束速查

| 约束 | 出处 | 后果 |
| --- | --- | --- |
| 宿主机禁跑 `go` 原生命令 | AGENTS.md §1 | Go 走 `golang:1.24-alpine` 容器 |
| 前端 `npm run build` 可用用户级 node | 本机实测 | 宿主机 Node v22.23.2（pi 自带） |
| 分支名必须 `feat\|fix\|docs/TASK-<n>-<desc>` | AGENTS.md §2 | 未关联任务的分支被拒 |
| 禁平行文件 / 副本函数 / TODO Mocking | AGENTS.md §3 | 视为未完成 |
| 新 Go 模块必须配套 `*_test.go`（正常 + 边界） | AGENTS.md §4.3 | 视为未完成 |
| 调研/设计类必须产出图文 Markdown | AGENTS.md §5 | 纯文本罗列打回 |
| 提交用 Conventional Commits | AGENTS.md §6 | — |
