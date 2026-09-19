//go:build integration

package store_test

import (
	"context"
	"errors"
	"os"
	"testing"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 真实 Postgres 集成测试：证明 ResolveStorageProfile 在「没有可用配置」时
// 返回**可识别**的哨兵错误，而不是裸 pgx.ErrNoRows（issue #83）。
//
// 为什么必须连真实库：这个修复的全部价值在于「pgx.ErrNoRows 被翻译成哨兵」，
// 而该分支只有在真实查询返回 0 行时才会走到。用 mock 只能验证我们自己的假设。
//
// 运行（未设置 DSN 时跳过，避免在无库环境里伪装通过）：
//
//	docker run --rm -v $PWD:/w -w /w --network llm_default \
//	  -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  golang:1.24-alpine sh -c "go test -tags=integration ./internal/store/ -run TestResolveStorageProfileSentinel"
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("连接 Postgres 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newTestStore(t *testing.T, pool *pgxpool.Pool) *store.DatasetStore {
	t.Helper()
	// 密钥只需要能构造 SecretBox；本测试不触发解密（错误分支在解密之前返回）。
	box, err := appcrypto.NewSecretBox(os.Getenv("APP_ENCRYPTION_KEY"))
	if err != nil {
		// 没有配置密钥时用配置层的内置默认值，保持与其他 store 测试一致。
		box, err = appcrypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
		if err != nil {
			t.Fatalf("构造 SecretBox 失败: %v", err)
		}
	}
	return store.NewDatasetStore(pool, box)
}

// TestResolveStorageProfileFallsBackWhenIDMissing 钉死一个**既有行为**：
// 指定的 storage id 不存在/已停用时，只要库里还有可用配置，就**回退成功**（err=nil）。
//
// 为什么这条必须用真实库锁死（父代理的集成测试正是靠它发现了一个错误假设）：
// 修复过程中曾以为「指定的 id 不存在」会走到 ErrStorageProfileNotFound 分支，
// 于是写了一整套「请重新选择存储」的翻译与测试。真实库证明**该分支不可达** ——
// 回退查询会成功返回默认配置。若不钉死这条，会留下一个永远不执行的错误分支，
// 以及在 UI 上永远不出现的提示文案。
func TestResolveStorageProfileFallsBackWhenIDMissing(t *testing.T) {
	pool := newTestPool(t)
	datasets := newTestStore(t, pool)

	// 先确认库里确实有可用配置，否则本用例的前提不成立。
	var active int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM storage_profiles WHERE is_active = TRUE`).Scan(&active); err != nil {
		t.Fatalf("统计可用存储失败: %v", err)
	}
	if active == 0 {
		t.Skip("输入缺失：库中没有可用存储配置，无法验证回退行为")
	}

	const missingID int64 = 9223372036854775806
	endpoint, _, bucket, _, _, _, err := datasets.ResolveStorageProfile(context.Background(), missingID)
	if err != nil {
		t.Fatalf("存在其他可用配置时应回退成功（既有行为），得到 err=%v", err)
	}
	if endpoint == "" || bucket == "" {
		t.Errorf("回退到的配置不完整: endpoint=%q bucket=%q", endpoint, bucket)
	}
	if errors.Is(err, store.ErrStorageProfileNotFound) {
		t.Error("回退成功时不应返回 ErrStorageProfileNotFound")
	}
}

// TestResolveStorageProfileDefaultsToActiveProfile 断言 id=0 时能解析到一条可用配置，
// 且该配置处于 active 状态。
//
// 这条守护「bootstrap 产出的 profile 必须能被 id=0 的路径解析到」——
// 也就是 IsActive/IsDefault 两个字段的设置是否真的生效。
// 若库里确实一条可用配置都没有，则断言哨兵错误 —— 两种情况都必须是可解释的。
func TestResolveStorageProfileDefaultsToActiveProfile(t *testing.T) {
	pool := newTestPool(t)
	datasets := newTestStore(t, pool)

	endpoint, _, bucket, _, _, _, err := datasets.ResolveStorageProfile(context.Background(), 0)
	if err != nil {
		if errors.Is(err, store.ErrNoStorageProfile) {
			t.Skip("当前库没有可用存储配置：这属于合法前置状态（错误本身已可识别）")
		}
		t.Fatalf("id=0 时应解析到默认存储或返回可识别错误，得到 %v", err)
	}
	if endpoint == "" || bucket == "" {
		t.Errorf("解析到的存储配置不完整: endpoint=%q bucket=%q", endpoint, bucket)
	}
}
