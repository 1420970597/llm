# Atelier 灰度、观测与回退手册（T33 交付物）

> 本文件是 Issue [#160](https://github.com/1420970597/llm/issues/160) 的任务 **T33**
> 的交付物。代码落点：`internal/studio/rollout.go`（特性开关判据）、
> `internal/studio/observability.go`（运维快照）、
> `apps/api/routes_studio_rollout.go`（只读状态端点）、
> `scripts/check-schema-compat.sh`（部署前兼容性检查）。
>
> 本文件**不声称已经做过生产灰度或演练**。它写的是「怎么开、怎么关、怎么判断、
> 以及演练时必须验证什么」。实际演练记录另附（见第 6 节）。

---

## 1. 开关

| 配置 | 默认 | 作用 |
|---|---|---|
| `STUDIO_ENABLED` | `true` | 总开关。`false` 时**拒绝新的 Studio 命令**（设计/运行/判断/发布），读取与已发布文件下载不受影响 |
| `STUDIO_DISABLED_PROJECT_IDS` | 空 | 项目级回退名单。支持 `12` 或 `12:发布积压,13:成本失控` 两种写法 |

两条语义是刻意的，改之前请先读这段：

- **只拦写入**：`AuthzRead`/`AuthzDownload`/`AuthzManageMembers`/`AuthzManageWorkspace`
  不受开关影响。回退时用户必须还能把已经拿到的数据取走 —— 一个「关掉后连自己
  已发布的文件都下载不了」的开关，会让运维在真正需要回退时不敢用。
- **零值 = 全部启用**。忘了传 rollout（`Service.Rollout` 为零值）不会把所有人
  挡在门外。反过来的设计会让一次漏传在生产上以「功能整体不可用」的形式爆炸。

拒绝的形状：HTTP **503** + `code=DEPENDENCY_UNAVAILABLE` + 文案点明是哪个开关。
用 503 而不是 404：这不是「资源不存在」，而是「服务被运维暂停」；503 带可重试
语义，客户端知道等一会儿再试，而 404 会让用户以为数据丢了并开始重复创建。

未知动作按**写入**处理：把未知当成读取，会让将来新增的写动作默认绕过开关。

## 2. 观测

```bash
curl -s "$API/api/v1/studio/rollout" -H "Cookie: ..." | jq   # 开关状态（管理员）
curl -s "$API/api/v1/studio/health"  -H "Cookie: ..." | jq   # 诊断快照（管理员）
```

两个端点都要求管理员：快照含预算金额与回退原因，而回退原因常是运维手写的
现场描述（可能提到客户或事故）。交给普通成员自助查看会把治理信息变成公共信息。

快照字段与「怎么读」：

| 字段 | 读法 |
|---|---|
| `outbox.pending` / `outbox.oldestPendingAgeSeconds` | **派发循环**是否停滞。pending 可以在停滞时保持不动（没有新事件），而年龄会一直涨 —— 年龄才是证据 |
| `jobs.pending` / `jobs.oldestPendingAgeSeconds` | **执行**是否停滞（worker 不足或不认新作业） |
| `leases.expired24h` / `leases.fenced24h` | worker 心跳丢失/崩溃。持续 > 0 表示同一作业可能被执行过两次（费用与产出都可能重复） |
| `budget.currencies[].uncertainMinor` | 「不确定但已占用」的额度。**未知不等于 0**：超时/断连时按预留金额占用 |
| `usage.unknownAmount24h` | 24 小时内金额未知的请求数。把它读成「成本正常」是最常见的误判 |
| `usage.reconciliationImpossible` | 永久无法与账单核对的记录（供应商不给明细）。这些成本不会有精确值 |
| `experiments.withMissingScores` | 有缺分的实验。缺分不是 0 分，报告的均值只覆盖已评分的格 |
| `releases.buildFailed` | 制品未确认的发布。它们不会进入 `published`，已发布文件仍可下载 |
| `notes[]` | 服务端给出的可行动解读（阈值：outbox 等待 > 600 秒、排队 > 3600 秒、任何租约回收） |

`notes` 是快照的一部分而不是只在本文档里：值班的人看的是接口，而不是手册第 2 节。

**SLO 阈值尚未标定**：上面的阈值是「明显不对」的下界，不是从真实基线测出来的。
在完成一次真实灰度的度量之前，本文件**不给出** SLO/告警数值 —— 编一个数字
比没有数字更糟（它会让人以为有依据）。

## 3. 部署顺序（不可颠倒）

```bash
# 1. 部署前：确认库不比代码新
scripts/check-schema-compat.sh "$PROD_DSN"

# 2. 加性迁移（进程启动时自动执行；也可以单独跑一次 API 容器）
#    库中尚未应用的迁移会被自动应用；库中**多出**代码不认识的迁移则直接失败。

# 3. 先部署 worker（它必须认得出新作业种类）
# 4. 再放开 API 写入口（STUDIO_ENABLED=true）
```

为什么顺序不能反：作业是写在 DB 里的（`jobs`/`outbox`），消息只带 `jobId`。
worker 认不出新 `job_kind` 时，用户看到的是「已排队」，而实际没人能执行它。
`scripts/check-schema-compat.sh` 只检查 schema；作业种类兼容性由
「worker 版本 ≥ 迁移版本」保证，因此第 3 步必须先于第 4 步。

## 4. 灰度步骤

| 阶段 | 范围 | 观察点 | 进入下一阶段的条件 |
|---|---|---|---|
| 0 内部 | 内部工作区的新建项目 | `health` 的队列年龄、租约回收、未知成本 | 连续 48 小时无租约回收、无 `buildFailed` 增长 |
| 1 受控 | 1–2 个真实新项目（SFT） | 同上 + 缺分实验比例、发布成功率 | 完成一次「设计→试制→实验→判断→发布→下载」全流程 |
| 2 GRPO | 1 个 GRPO 项目 | GRPO 发布 JSONL 的逐行结构校验是否通过 | GRPO 全流程跑通（试制→评估→判断→发布→下载） |
| 3 迁移项目 | 经 T30/T31 确认的 legacy 项目 | 迁移差异、旧文件下载 hash | 对账通过（数量/状态/hash） |
| 4 全量 | 全部新项目 | 同上 | —— |

回退到上一阶段用 `STUDIO_DISABLED_PROJECT_IDS`（项目级），不要为了单个项目
关掉总开关：总开关会影响所有用户。

## 5. 回退

```bash
# 项目级（首选）
STUDIO_DISABLED_PROJECT_IDS="12:发布积压"        # 重启 API（worker 可不动）

# 全局
STUDIO_ENABLED=false                              # API 与 worker 同时设
```

回退后**预期状态**（逐条核对，不是猜）：

| 对象 | 预期 |
|---|---|
| 新的 Studio 命令 | 503 + `DEPENDENCY_UNAVAILABLE`，文案点明开关 |
| 读取（版本/批次/样本/报告） | 正常 |
| 已发布文件下载 | 正常（**必须**，否则回退等于数据不可取） |
| 已提交的作业 | worker 侧 `STUDIO_ENABLED=false` 时**不再抢占新作业**，正在执行的作业正常跑完（不取消 ctx、不杀进程） |
| Redis 里未被消费的消息 | 留在队列里（worker 在 BRPOP **之前**停下，因此不会「取出来再丢掉」）；重新开启后正常消费 |
| outbox 未派发事件 | 保持 pending；`maintainLoop` 会继续重投未送达的意图 |
| 数据库 | 只有加法迁移，**不 down-migrate 删表**；回退不回滚 schema |
| 旧控制台 | 只读旧 dataset 资产；它不持有新项目对象的写路径（见第 6 节） |

## 6. 演练清单（必须逐项留下记录）

| 演练 | 怎么造 | 预期 |
|---|---|---|
| job schema 不兼容 | 把 Redis 里的消息 `schemaVersion` 改成 999 投递 | worker 记日志并送死信，**不**按旧格式解析；不丢消息 |
| API/worker 版本不匹配 | 先起新 worker 再用旧 worker 消费同一队列 | 旧 worker 看到 `type` 为空而忽略（不误吞）；新 worker 正常处理 |
| 发布积压 | 造一个 release 卡在 `building`（断开对象存储） | `health.releases.buildFailed` 增长并出现在 `notes`；重试可幂等续接；已发布文件不受影响 |
| 回退后恢复 | 关总开关 → 提交一个命令（503）→ 开启 → 重试同一命令 | 幂等键让「重试」返回同一个对象，不产生第二个 |
| 未知成本 | 让一次模型调用超时 | `usage.unknownAmount24h` 增长；`uncertainMinor` 增加；**不会被记成 0** |

## 7. 已知缺口（诚实清单）

1. **真实灰度和演练尚未执行**：本文件是方案与判据，不是执行记录。第 6 节的每一项
   都还没有证据，因此 T33 的验收项（「演练 API/worker 版本不匹配、发布积压与回退」）
   在真实环境做完之前**不应**被勾选。
2. **SLO 阈值未标定**：见第 2 节。
3. **旧写入口的冻结属于 T31**：本文件只说明了「旧控制台不持有新对象的写路径」
   这一结构性事实（新对象在独立表、由项目级授权与开关共同管辖），
   但「冻结旧 dataset 写入口 + 一致性水位」由 T31 交付。
4. **日志关联字段已提供（`studio.LogContext`）但未接入所有调用点**：
   `requestId` 已由既有 HTTP middleware 提供，`projectId`/`batchId`/
   `experimentId`/`releaseId` 的注入在 worker 的 Studio 作业路径上完成；
   旧控制台路径不在本轮范围内。
