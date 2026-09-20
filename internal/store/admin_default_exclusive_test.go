package store

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDefaultFlagsAreMutuallyExclusive 锁定 issue #138 的修复。
//
// 背景：storage_profiles 与 generation_strategies 的 `is_default` 此前**不互斥** ——
// 「设为默认」开关在弹窗里默认打开，保存时又不清掉别人，实测库里
// **20 条 storage_profiles 同时 is_default=true**、4 条策略同时为默认。
//
// 后果：
//  1. 界面把 20 条都标成「默认」，用户无法判断哪条真正生效；
//     实际生效的是 `ORDER BY is_default DESC, id DESC LIMIT 1` 选出的**最新一条**，
//     这条规则在界面上完全不可见；
//  2. 它放大了 #136（下载用「当前默认存储」的 bucket）—— 用户以为默认没变，
//     实际已被新建记录悄悄顶掉。
//
// 修复：写入前在**同一事务内**清掉同类的默认标记（与 export_mapping_store.go 的
// 既有做法一致，但用事务包住「清 + 写」，避免中途失败留下零个默认的中间态）。
//
// 连真实 Postgres；未设置 LLM_TEST_POSTGRES_DSN 时按仓库惯例 skip。
func TestDefaultFlagsAreMutuallyExclusive(t *testing.T) {
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

	box, err := crypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	store := NewAdminStore(pool, box)

	prefix := fmt.Sprintf("l15-default-exclusive-%d-", os.Getpid())

	// 两个都设为默认，断言最终只剩一个。
	mkProfile := func(name string) model.StorageProfile {
		return model.StorageProfile{
			Name: name, Provider: "minio", Endpoint: "http://minio:9000",
			Region: "us-east-1", Bucket: "llm-factory-dev",
			AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin",
			UsePathStyle: true, IsActive: true, IsDefault: true,
		}
	}
	p1, err := store.UpsertStorageProfile(ctx, mkProfile(prefix+"a"))
	if err != nil {
		t.Fatalf("create profile a: %v", err)
	}
	p2, err := store.UpsertStorageProfile(ctx, mkProfile(prefix+"b"))
	if err != nil {
		t.Fatalf("create profile b: %v", err)
	}
	t.Cleanup(func() {
		// 精确条件清理自己创建的两行。
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM storage_profiles WHERE id = ANY($1::bigint[])`, []int64{p1.ID, p2.ID})
	})

	var defaultProfiles int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM storage_profiles WHERE is_default = TRUE`).Scan(&defaultProfiles); err != nil {
		t.Fatalf("count default profiles: %v", err)
	}
	if defaultProfiles != 1 {
		t.Errorf("storage_profiles 应恰好 1 条默认（issue #138），实际 %d —— "+
			"界面会把多条都标成「默认」，用户无法判断哪条真正生效", defaultProfiles)
	}
	// 最新的那条必须是默认（与 ResolveStorageProfile 的 ORDER BY id DESC 语义一致）。
	var latestDefault bool
	if err := pool.QueryRow(ctx,
		`SELECT is_default FROM storage_profiles WHERE id = $1`, p2.ID).Scan(&latestDefault); err != nil {
		t.Fatalf("read newest profile: %v", err)
	}
	if !latestDefault {
		t.Errorf("最新设为默认的那条应是唯一默认（issue #138）")
	}

	// 策略同理。
	mkStrategy := func(name string) model.GenerationStrategy {
		return model.GenerationStrategy{
			Name: name, Description: "互斥验证", DomainCount: 5, QuestionsPerDomain: 10,
			AnswerVariants: 1, RewardVariants: 1, PlanningMode: "balanced", IsDefault: true,
		}
	}
	s1, err := store.UpsertStrategy(ctx, mkStrategy(prefix+"s1"))
	if err != nil {
		t.Fatalf("create strategy 1: %v", err)
	}
	s2, err := store.UpsertStrategy(ctx, mkStrategy(prefix+"s2"))
	if err != nil {
		t.Fatalf("create strategy 2: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM generation_strategies WHERE id = ANY($1::bigint[])`, []int64{s1.ID, s2.ID})
	})

	var defaultStrategies int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM generation_strategies WHERE is_default = TRUE`).Scan(&defaultStrategies); err != nil {
		t.Fatalf("count default strategies: %v", err)
	}
	if defaultStrategies != 1 {
		t.Errorf("generation_strategies 应恰好 1 条默认（issue #138），实际 %d", defaultStrategies)
	}
}
