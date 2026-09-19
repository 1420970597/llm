# 父代理复核：issue #96 与 #83 是同一根因

## 结论

**#96（自动审查「数据集状态与记录数一致」，失败详情 `答案生成 记录数 0 < 4`）不是 R2（#5）的回归，
而是 #83（全新部署 `storage_profiles` 为空导致答案生成必然失败）的同一个根因。**

R2 修复的是「状态推进时机」（记录写完之后才推进状态），修得对且已由变异测试证明；
但**记录根本写不进去**（存储配置缺失 → 生成阶段解析存储失败 → 一条都没落库），
所以「状态与记录数一致」这条断言仍然失败 —— 它测的是一个**下游症状**。

## 决定性证据（父代理实测，可复现）

在**全新库**上跑完全部 0020 个迁移，然后计数：

```bash
docker rm -f l15-probe-pg
docker run -d --rm --name l15-probe-pg \
  -e POSTGRES_DB=llm_factory -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev \
  -p 15499:5432 postgres:17-alpine
for f in $(ls sql/migrations/*.sql | sort); do
  docker exec -i l15-probe-pg psql -v ON_ERROR_STOP=1 -q -U llm_factory -d llm_factory -f - < "$f" >/dev/null
done
docker exec l15-probe-pg psql -U llm_factory -d llm_factory -tAc "select count(*) from storage_profiles"
# 0        <-- 关键
docker exec l15-probe-pg psql -U llm_factory -d llm_factory -tAc "select count(*) from model_providers"
# 0
```

同时确认**没有任何 storage 引导机制**（与 provider 不同）：

```bash
grep -rn "BootstrapStorage\|bootstrapStorage\|APP_BOOTSTRAP_STORAGE" apps/api/*.go internal/config/*.go
# 无输出 —— 不存在该机制
```

而 `.env` **确实有** S3 配置，却没有任何代码把它写进 `storage_profiles`：

```text
S3_BUCKET=llm-factory-dev
S3_ENDPOINT=http://minio:9000
S3_ACCESS_KEY=***（已脱敏）
S3_SECRET_KEY=***（已脱敏）
```

> 对照：`model_providers` 有完整的引导（`APP_BOOTSTRAP_PROVIDER_*` + `EnsureProvider`，
> 见 `apps/api/provider_bootstrap.go`）。**storage profile 缺了这条对称路径** —— 这就是根因的形状。

## 为什么共享库现在有 19 条 storage profile

因为它们是**历史人工/测试创建的**，不是迁移或引导产生的：

```text
 id | name                      | provider | active | default
----+---------------------------+----------+--------+---------
  5 | l15-r11-1238060-storage   | minio    | t      | t        <- R11 lane 建的测试用
 11 | 本地 MinIO                 | minio    | t      | t        <- 人工建的
 12 | 自动存储-506335            | minio    | t      | t
  9 | (空名)                     | (空)     | f      | f        <- 脏数据（正是 #63 修的那类）
 10 | (空名)                     | (空)     | f      | f
```

这解释了为什么**共享开发库上一切正常、而全新部署必然失败** —— 也正是 #96 的自动审查
（用 `:18081` 全新起栈）能复现、而本地开发看不到的原因。

## 对两个 issue 的处置

| Issue | 判定 | 理由 |
|---|---|---|
| **#96** | **并入 #83**，不单开 lane | 它断言的是 #83 的下游症状。R15 lane（#83）修好「全新部署可用性」后，该断言自然通过。若单独修 #96（例如放宽断言），会**掩盖真实缺陷**。 |
| **#83** | 由 R15 lane 修复 | 根因：`.env` 有 S3 配置但无引导路径，且表为空时 `ResolveStorageProfile` 返回裸 `pgx.ErrNoRows`，用户只看到英文内部错误。 |

## 给 R15 lane 的补充要求

R15 的任务书已包含「决定并实现全新部署的开箱可用性」，此处补充两点**由本复核得出的具体输入**：

1. **对称性是可用的设计参照**：`model_providers` 的 `EnsureProvider`
   （`internal/store/provider_bootstrap.go`）是「按唯一键幂等引导、已存在时不覆盖管理员改动」
   的正确范例。storage profile 应走同样的形状，而且 `.env` 里的 `S3_*` 正好是现成的引导来源 ——
   这样「全新部署开箱可用」与「不覆盖管理员改动」可以同时成立。
2. **`#96` 的断言必须能通过**：R15 的验证里应包含「全新库 + 全新栈 → 答案生成真的产出记录」，
   这与 R2 的「状态不领先于记录数」是两条互补的断言，**两条都要成立**才算修好。
   建议在 R15 的 PR 里显式跑一次 R2 的 `test/l15_worker_batch_status.py`。

## 复现命令（供验证）

```bash
# 1. 全新库（见上方 docker run + 迁移循环）
# 2. 断言 storage_profiles 为 0  -> 证明「无引导」
# 3. 起候选栈（api + worker 指向该库）后走一遍答案生成
#    -> 预期：worker 日志出现 "no rows in result set"，数据集转 reasoning_failed，0 条记录
```
