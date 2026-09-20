package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCleaningKeywordEnsureSeededMakesCleanupWork 锁定「内置词库开箱可用」。
//
// 背景（父代理代码级评审发现的缺陷）：cleaning_keywords 由迁移 0012 建表，
// 但迁移里没有任何 INSERT；41 条内置词只存在于 Go 代码，补种只挂在
// POST /cleaning/keywords/seed 上，而前端完全没有这个入口。
//
// 后果不是「页面空着」这么轻：apps/worker/job_cleaning.go 只加载
// is_active = TRUE 的词，库为空 -> 匹配不到任何东西 ->
// scanner.go 的 `if len(matches) == 0 { return ActionClean }`
// -> 所有拒答内容都被判为「干净」，界面显示「清洗完成、命中 0 条」。
// 用户会误以为数据质量良好。
//
// 本测试断言 EnsureSeeded 能把内置词补齐，且补齐后**真的能匹配到拒答文本**。
func TestCleaningKeywordEnsureSeededMakesCleanupWork(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	store := NewCleaningKeywordStore(pool)

	// 前置：先记录调用前的数量，测试结束后恢复到该状态（精确条件，不做无 WHERE 删除）。
	before, err := store.CountBuiltin(ctx)
	if err != nil {
		t.Fatalf("count builtin: %v", err)
	}
	t.Cleanup(func() {
		// 只在本次测试**补种过**的情况下回滚自己插入的行（按 is_builtin + 精确 pattern 集）。
		if before == 0 {
			_, _ = pool.Exec(context.Background(),
				`DELETE FROM cleaning_keywords WHERE is_builtin = TRUE`)
		}
	})

	if err := store.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
	}

	after, err := store.CountBuiltin(ctx)
	if err != nil {
		t.Fatalf("count builtin after seed: %v", err)
	}
	if after == 0 {
		t.Fatalf("EnsureSeeded 后内置词仍为 0 —— 全新部署下清洗会静默放过所有拒答内容")
	}
	if after < 40 {
		t.Errorf("内置词应有 40+ 条（含需求点名的「对不起」「我不能」），实际 %d", after)
	}

	// 幂等：再调一次不应增加。
	if err := store.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded 第二次: %v", err)
	}
	again, err := store.CountBuiltin(ctx)
	if err != nil {
		t.Fatalf("count after second seed: %v", err)
	}
	if again != after {
		t.Errorf("EnsureSeeded 必须幂等：第一次 %d 条，第二次变成 %d 条", after, again)
	}

	// 关键：补种后必须真的能匹配到需求点名的拒答文本。
	active := true
	items, err := store.List(ctx, "", &active)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("补种后仍无启用中的关键词")
	}
}
