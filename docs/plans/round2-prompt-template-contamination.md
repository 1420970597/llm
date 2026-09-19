# 父代理复核：自动审查 harness 遗留的脏 prompt 模板会**静默改变生产行为**

## 现象

父代理跑全量端到端验收（`test/test_acceptance_7requirements.py`）时，
需求 3（问题生成）失败：

```text
questions.v2.direction.error dataset_id=138 direction_id=612
  err=provider returned non-JSON content: 以下是一组可直接用于测试 AI助手的问题，按能力维度分类：
      ###基础与事实
      1.请用不超过50字解释"过拟合"。...
```

即：模型返回的是**与任务无关的通用问答清单**，而不是「军事领域某方向的具体问题」。

## 根因：脏 prompt 模板覆盖了内置默认提示词

```bash
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select id, name, stage, is_active, left(user_prompt,200) from prompt_templates
   where stage='question-generation' and is_active order by updated_at desc limit 3"
```

```text
18|自动测试模板-892389|question-generation|t|请生成测试问题。
17|自动测试模板-793393|question-generation|t|请生成测试问题。
16|自动测试模板-766084|question-generation|t|请生成测试问题。
```

而取值逻辑是「取最近更新的那条活跃模板」：

```go
// internal/store/admin_store.go:314 GetActivePromptByStage
WHERE stage = $1 AND is_active = TRUE
ORDER BY updated_at DESC, id DESC
LIMIT 1
```

**于是外部 harness 写一条 `user_prompt = "请生成测试问题。"` 的模板，
就静默接管了生产链路的提示词。** 生成出来的自然是一堆与主题无关的通用问题。

证据：`prompt_templates` 里有 **7 条** `自动测试模板-*` 处于 active，
全部由**自动审查 harness**（不是本仓库代码，`grep -rn "自动测试模板" --include=*.py|*.sh|*.go`
在本仓库无命中）在不同时间点写入，时间戳集中在 08:30–09:43。

## 为什么这是一类值得单独记录的缺陷（而不只是一条脏数据）

1. **它是「跨运行污染」，且污染的是行为而不是数据行数。**
   常规反污染（契约 §6.2）关注的是「测试别留下多余的行」；
   这里留下的是**一条会改变后续所有运行行为**的配置。
   它不会让任何测试「失败于自身」，只会让**别人的**运行产出错的数据。

2. **影响是静默的。** 提示词变差不会报错，只会让生成结果偏离主题。
   父代理之所以能发现，是因为端到端验收会**校验产出的语义**
   （问题必须与关键词相关），而不是只看「有没有产出」。

3. **`is_active` 是全局单值语义，但没有「归属」概念。**
   任何写入者都能接管全局提示词；写入者退出时也没有「恢复原状」的约定。

## 处置（父代理本次执行的）

1. **停用**这 7 条脏模板（`is_active = FALSE`，**不删除** —— 保留证据，且可逆）：
   ```sql
   UPDATE prompt_templates SET is_active = FALSE, updated_at = NOW()
   WHERE is_active = TRUE AND name LIKE '自动测试模板%';
   ```
   停用后 `GetActivePromptByStage` 查不到活跃模板，链路回落到内置默认提示词。

2. 清理被这次污染弄坏的验收数据集（精确 id + `name LIKE '验收%'` 双条件）。

3. 重跑全量验收。

## 未做的改动及理由（留给后续）

**不改产品代码**，理由：

- 「谁可以写全局提示词」是**权限模型**问题（当前所有管理员都能写），
  收紧它属于权限设计，不是本轮缺陷治理的范围；
- 「写入者退出时恢复」需要引入模板归属/作用域（例如按 provider 或按数据集隔离），
  是一个有设计取舍的功能（隔离会降低模板复用性），需要单独评估；
- 更小的改动（例如「测试写入的模板必须带可识别前缀且启动时清理」）
  依赖外部 harness 配合，本仓库单方面改不了。

因此本条作为**已定位、已缓解、未根治**的风险登记，并建议单开 lane：
> 给 prompt_templates 增加「作用域/归属」或「测试模板自动过期」机制，
> 使任何写入者都无法静默接管全局提示词。

## 复现命令

```bash
# 1. 看是否已被污染
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select id,name,stage,is_active from prompt_templates where is_active and name like '自动测试模板%'"
# 2. 停用
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -c \
  "UPDATE prompt_templates SET is_active=FALSE, updated_at=NOW() WHERE is_active AND name LIKE '自动测试模板%'"
# 3. 重跑验收
python3 test/test_acceptance_7requirements.py --base http://127.0.0.1:18100
```
