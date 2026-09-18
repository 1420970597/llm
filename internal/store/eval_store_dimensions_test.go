package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 维度的 upsert 语义、内置保护与幂等 seed 都落在 SQL（ON CONFLICT / is_builtin 判定），
// 纯函数单测无法证明，因此这里用真实 Postgres 做集成测试。
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过，避免在无数据库环境里伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestEvalDimension -v"
func TestEvalDimensionStoreIntegration(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	// 测试数据用独立前缀，避免污染真实维度目录，也避免与内置 key 冲突。
	const testPrefix = "l8test_"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM eval_dimensions WHERE key LIKE $1`, testPrefix+"%")
	}
	cleanup()
	defer cleanup()

	dimStore := store.NewEvalDimensionStore(pool)

	// 第 1 步：内置目录 seed 幂等。
	//
	// 断言刻意与「运行前库里已有多少内置维度」无关：共享 postgres 上目录通常已被
	// 其它 lane 或前一次运行 seed 过，此时本次 inserted=0 但 total 依然是 58。
	// 真正的不变量是「seed 之后内置目录完整存在」与「重复 seed 不重复插入」。
	_, total, err := eval.Seed(ctx, dimStore)
	if err != nil {
		t.Fatalf("seed builtin dimensions: %v", err)
	}
	if total < 56 {
		t.Fatalf("seed 后维度总数应 >=56，实际 %d", total)
	}

	againInserted, againTotal, err := eval.Seed(ctx, dimStore)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if againInserted != 0 {
		t.Fatalf("二次 seed 必须幂等（新增 0 条），实际新增 %d 条", againInserted)
	}
	if againTotal != total {
		t.Fatalf("幂等 seed 不应改变总数：首次 %d，二次 %d", total, againTotal)
	}

	// 内置维度必须真的落库且可查回完整判据（rubric 非空是需求「可操作分档判据」的底线）。
	builtinTrue := true
	builtins, err := dimStore.List(ctx, "", &builtinTrue)
	if err != nil {
		t.Fatalf("list builtin dimensions: %v", err)
	}
	if len(builtins) < 56 {
		t.Fatalf("库中内置维度应 >=56 个，实际 %d 个", len(builtins))
	}
	for _, dim := range builtins {
		if strings.TrimSpace(dim.Rubric) == "" {
			t.Fatalf("内置维度 %s 的 rubric 落库后为空", dim.Key)
		}
		if !dim.IsBuiltin {
			t.Fatalf("内置维度 %s 的 is_builtin 必须为 true", dim.Key)
		}
	}

	// 第 2 步：用户自定义维度 —— 新增。
	created, err := dimStore.Upsert(ctx, model.EvalDimension{
		Key:         testPrefix + "custom_risk",
		Name:        "风险提示充分度",
		Category:    "robustness",
		Description: "结论是否提示了执行风险",
		Rubric:      "无风险提示记 1 分；仅笼统提及记 3 分；分条列出具体风险与触发条件记 5 分。",
		ScaleMin:    1, ScaleMax: 5, IsActive: true, Weight: 1.4,
	})
	if err != nil {
		t.Fatalf("create custom dimension: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("新建维度必须返回主键，实际 %d", created.ID)
	}
	if created.IsBuiltin {
		t.Fatal("用户自定义维度的 is_builtin 必须为 false")
	}

	// 第 3 步：同名 key 再 upsert 是更新而非重复插入。
	updated, err := dimStore.Upsert(ctx, model.EvalDimension{
		Key:         testPrefix + "custom_risk",
		Name:        "风险提示充分度（修订）",
		Category:    "robustness",
		Description: "结论是否提示了执行风险",
		Rubric:      "无风险提示记 1 分；仅笼统提及记 3 分；分条列出具体风险、触发条件与缓解措施记 5 分。",
		ScaleMin:    1, ScaleMax: 5, IsActive: true, Weight: 1.6,
	})
	if err != nil {
		t.Fatalf("update custom dimension by key: %v", err)
	}
	if updated.ID != created.ID {
		t.Fatalf("同 key upsert 必须复用同一行：原 id=%d 新 id=%d", created.ID, updated.ID)
	}
	if updated.Name != "风险提示充分度（修订）" {
		t.Fatalf("更新后名称未生效，实际 %q", updated.Name)
	}
	if updated.Weight != 1.6 {
		t.Fatalf("更新后权重未生效，实际 %v", updated.Weight)
	}

	// 第 4 步：按 ID 更新，且内置标记不被客户端篡改。
	byID, err := dimStore.Upsert(ctx, model.EvalDimension{
		ID:       created.ID,
		Key:      testPrefix + "custom_risk",
		Name:     "风险提示充分度（二次修订）",
		Category: "robustness",
		Rubric:   "按风险覆盖度打分：无提示 1 分，笼统 3 分，分条且含缓解措施 5 分。",
		ScaleMin: 1, ScaleMax: 5, IsActive: true, Weight: 2,
	})
	if err != nil {
		t.Fatalf("update custom dimension by id: %v", err)
	}
	if byID.ID != created.ID || byID.Weight != 2 {
		t.Fatalf("按 ID 更新失败：id=%d weight=%v", byID.ID, byID.Weight)
	}

	// 第 5 步：ListByKeys 保持传入顺序，并跳过不存在的 key。
	ordered, err := dimStore.ListByKeys(ctx, []string{testPrefix + "custom_risk", "lc_step_sufficiency", "不存在的key"})
	if err != nil {
		t.Fatalf("list by keys: %v", err)
	}
	if len(ordered) != 2 {
		t.Fatalf("ListByKeys 应返回 2 个已存在的维度，实际 %d 个", len(ordered))
	}
	if ordered[0].Key != testPrefix+"custom_risk" || ordered[1].Key != "lc_step_sufficiency" {
		t.Fatalf("ListByKeys 必须保持传入顺序，实际 %s, %s", ordered[0].Key, ordered[1].Key)
	}

	// 第 6 步：按分类过滤 + 自定义维度与内置维度共存。
	customFalse := false
	customs, err := dimStore.List(ctx, "", &customFalse)
	if err != nil {
		t.Fatalf("list custom dimensions: %v", err)
	}
	foundCustom := false
	for _, dim := range customs {
		if dim.Key == testPrefix+"custom_risk" {
			foundCustom = true
		}
		if dim.IsBuiltin {
			t.Fatalf("builtin=false 的过滤结果里混入了内置维度 %s", dim.Key)
		}
	}
	if !foundCustom {
		t.Fatal("自定义维度未出现在 builtin=false 的过滤结果中")
	}

	robustness, err := dimStore.List(ctx, "robustness", nil)
	if err != nil {
		t.Fatalf("list by category: %v", err)
	}
	sawBuiltinRobustness, sawCustomRobustness := false, false
	for _, dim := range robustness {
		if dim.Category != "robustness" {
			t.Fatalf("按分类过滤返回了其他分类的维度 %s（%s）", dim.Key, dim.Category)
		}
		if dim.IsBuiltin {
			sawBuiltinRobustness = true
		}
		if dim.Key == testPrefix+"custom_risk" {
			sawCustomRobustness = true
		}
	}
	if !sawBuiltinRobustness || !sawCustomRobustness {
		t.Fatalf("robustness 分类应同时含内置与自定义维度：builtin=%v custom=%v",
			sawBuiltinRobustness, sawCustomRobustness)
	}

	// 第 7 步：分类列表去重。
	categories, err := dimStore.Categories(ctx)
	if err != nil {
		t.Fatalf("categories: %v", err)
	}
	seen := map[string]bool{}
	for _, category := range categories {
		if seen[category] {
			t.Fatalf("分类 %q 重复出现", category)
		}
		seen[category] = true
	}
	if !seen["long_chain"] {
		t.Fatal("分类列表必须包含 long_chain")
	}

	// 第 8 步：内置维度拒绝删除，且删除失败后记录仍然存在。
	var builtinID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM eval_dimensions WHERE key = 'lc_step_sufficiency'`).Scan(&builtinID); err != nil {
		t.Fatalf("lookup builtin id: %v", err)
	}
	if err := dimStore.Delete(ctx, builtinID); !errors.Is(err, store.ErrBuiltinDimensionProtected) {
		t.Fatalf("删除内置维度必须返回 store.ErrBuiltinDimensionProtected，实际 %v", err)
	}
	if _, err := dimStore.Get(ctx, builtinID); err != nil {
		t.Fatalf("被拒绝删除的内置维度必须仍然存在：%v", err)
	}

	// 第 9 步：自定义维度可以删除；重复删除返回 pgx.ErrNoRows。
	if err := dimStore.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete custom dimension: %v", err)
	}
	if _, err := dimStore.Get(ctx, created.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("删除后应查不到该维度，实际 err=%v", err)
	}
	if err := dimStore.Delete(ctx, created.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("重复删除应返回 pgx.ErrNoRows，实际 %v", err)
	}
}

// TestEvalDimensionStoreRejectsInvalidInputIntegration 验证非法输入被拒绝而不是静默写入。
func TestEvalDimensionStoreRejectsInvalidInputIntegration(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	dimStore := store.NewEvalDimensionStore(pool)

	cases := []struct {
		name  string
		input model.EvalDimension
	}{
		{"空 key", model.EvalDimension{Name: "N", Category: "long_chain", ScaleMin: 1, ScaleMax: 5, Weight: 1}},
		{"空 name", model.EvalDimension{Key: "l8test_bad_name", Category: "long_chain", ScaleMin: 1, ScaleMax: 5, Weight: 1}},
		{"分值区间倒置", model.EvalDimension{Key: "l8test_bad_scale", Name: "N", Category: "long_chain", ScaleMin: 5, ScaleMax: 1, Weight: 1}},
		{"权重为零", model.EvalDimension{Key: "l8test_bad_weight", Name: "N", Category: "long_chain", ScaleMin: 1, ScaleMax: 5, Weight: 0}},
	}
	for _, tc := range cases {
		if _, err := dimStore.Upsert(ctx, tc.input); err == nil {
			t.Fatalf("%s：必须返回错误而不是静默写入", tc.name)
		}
	}

	// 确认非法输入没有留下任何记录。
	var leftover int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM eval_dimensions WHERE key LIKE $1`, "l8test_bad%").Scan(&leftover); err != nil {
		t.Fatalf("count leftover: %v", err)
	}
	if leftover != 0 {
		t.Fatalf("非法输入不得写入数据库，实际残留 %d 条", leftover)
	}
}

// TestEvalDimensionStoreListByKeysEmptyIntegration 验证空 keys 不发查询、返回空切片。
func TestEvalDimensionStoreListByKeysEmptyIntegration(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	items, err := store.NewEvalDimensionStore(pool).ListByKeys(ctx, nil)
	if err != nil {
		t.Fatalf("ListByKeys(nil): %v", err)
	}
	if items == nil {
		t.Fatal("ListByKeys 必须返回空切片而非 nil，保证 JSON 序列化为 []")
	}
	if len(items) != 0 {
		t.Fatalf("空 keys 应返回 0 条，实际 %d 条", len(items))
	}
}
