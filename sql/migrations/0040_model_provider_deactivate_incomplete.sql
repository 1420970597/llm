-- issue #209：把「配置不完整却处于启用状态」的历史模型连接停用。
--
-- 缺陷形态（实测）：库里 6 条完全空的连接（name/base_url/model 全空），其中 2 条
-- is_active=true；另有 2 条 base_url='not-a-url' 也是 is_active=true。它们
--   1. 混进蓝图「生成 → 模型服务」下拉（11 个选项里 6 个没有任何文字）；
--   2. 可以被选中并建出批次，直到**批次已经开始跑**才报
--      `model connection unavailable` —— 一次真实的时间与额度浪费。
--
-- 为什么改 `is_active` 而不是删行（issue 建议里的另一种写法）：
-- 三条外键以 ON DELETE RESTRICT 引用 model_providers
-- （model_price_versions / usage_ledger 的连接列，见 0027_studio_usage_budget.sql），
-- 删行在真实库里会直接失败而不是「顺便清理干净」。而 `is_active` 正是选择层
-- （蓝图下拉、质量实验裁判）已经在用的过滤条件，因此停用就是**这条连接不再被
-- 提供选择**的最小正确表达，也不需要动任何历史引用。
--
-- 为什么这不等同于「丢弃用户数据」：行仍在，管理员可以在「连接与设置」里看到
-- （列表会把配置不完整标红），补齐字段后重新启用即可。
--
-- 判据必须与 Go 侧 store.ProviderConfigIssues 一致（utils：那一份是唯一权威，
-- 这里只是一次性数据修复）。用 SQL 复述规则是必要之恶 —— 迁移不能调用 Go 代码，
-- 因此把「同一个谓词」逐条写出来，并在注释里钉住它在 Go 侧的对应物：
--   服务名称/基础 URL/模型名称 去空白后非空；基础 URL 是完整 http(s) 地址；
--   最大并发数 > 0；超时秒数 > 0。
--
-- 幂等：只做「true → false」单向收敛，重复执行结果相同；已被停用的行不产生变化。
-- 不去动任何**已经合法**的连接。
UPDATE model_providers
SET is_active = FALSE,
    updated_at = NOW()
WHERE is_active = TRUE
  AND (
    btrim(name) = ''
    OR btrim(base_url) = ''
    OR btrim(model) = ''
    OR base_url !~ '^https?://[^/[:space:]]+'
    OR max_concurrency < 1
    OR timeout_seconds < 1
  );
