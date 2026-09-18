package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件用真实 Postgres 锁死 CleaningKeywordStore.Upsert 的更新语义。
//
// 背景（L14 reviewer 实测发现的缺陷，父代理修根因）：
// 原 UPDATE 分支只 SET match_mode/severity/is_active/note，且 WHERE id=$1 AND pattern=$2。
// 后果是两个**假成功**：
//   - PUT {"id":N,"category":"safety"} → 200，但 category 仍是原值（静默丢弃）；
//   - PUT {"id":N,"pattern":"改了"}    → 404（pattern 不匹配定位不到记录）。
//
// 前端只能靠「把输入框置灰」绕过，但那只是掩盖：任何别的调用方（脚本、其他页面、
// 未来的批量编辑）仍会踩同一个坑。所以根因必须在 store 层修掉：身份字段
// （pattern/category）不可就地修改时**必须报错**，绝不能静默成功。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过，避免在无数据库环境里伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestCleaningKeywordUpdate -v"
func TestCleaningKeywordUpdateIntegration(t *testing.T) {
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

	keywords := NewCleaningKeywordStore(pool)

	// 每个用例用独立 pattern，避免与共享库中的既有数据冲突。
	const pattern = "父代理-更新语义-测试词"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM cleaning_keywords WHERE pattern = $1`, pattern)
	}
	cleanup()
	defer cleanup()

	created, err := keywords.Upsert(ctx, model.CleaningKeyword{
		Pattern: pattern, Category: "refusal", MatchMode: "contains",
		Severity: "block", IsActive: true, Note: "初始",
	})
	if err != nil {
		t.Fatalf("insert keyword: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("insert must return an ID")
	}
	if created.IsBuiltin {
		t.Fatal("inserted keyword must not be builtin")
	}

	// 用例 1（核心回归）：改 category 必须报错，而不是 200 静默丢弃。
	// 这正是 L14 reviewer 实测出的假成功控件。
	_, err = keywords.Upsert(ctx, model.CleaningKeyword{
		ID: created.ID, Pattern: pattern, Category: "safety",
		MatchMode: "contains", Severity: "block", IsActive: true,
	})
	if !errors.Is(err, cleaning.ErrKeywordIdentityImmutable) {
		t.Fatalf("changing category must fail with ErrKeywordIdentityImmutable, got %v", err)
	}
	// 并且库里的值必须原封不动。
	after, err := keywords.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get after rejected update: %v", err)
	}
	if after.Category != "refusal" {
		t.Fatalf("category must be unchanged after rejected update, got %q", after.Category)
	}

	// 用例 2：改 pattern 同样报错（原实现是 404，语义更差：像是「记录不存在」）。
	_, err = keywords.Upsert(ctx, model.CleaningKeyword{
		ID: created.ID, Pattern: pattern + "-改了", Category: "refusal",
		MatchMode: "contains", Severity: "block", IsActive: true,
	})
	if !errors.Is(err, cleaning.ErrKeywordIdentityImmutable) {
		t.Fatalf("changing pattern must fail with ErrKeywordIdentityImmutable, got %v", err)
	}

	// 用例 3：可写字段必须真的落库（severity / matchMode / isActive / note）。
	updated, err := keywords.Upsert(ctx, model.CleaningKeyword{
		ID: created.ID, Pattern: pattern, Category: "refusal",
		MatchMode: "regex", Severity: "warn", IsActive: false, Note: "改过了",
	})
	if err != nil {
		t.Fatalf("updating writable fields must succeed: %v", err)
	}
	if updated.MatchMode != "regex" || updated.Severity != "warn" || updated.IsActive || updated.Note != "改过了" {
		t.Fatalf("writable fields not persisted: %+v", updated)
	}
	if updated.Category != "refusal" {
		t.Fatalf("category must stay refusal, got %q", updated.Category)
	}

	// 用例 4：省略字段（空值）语义是「保持原值」，不得被重置成默认值。
	partial, err := keywords.Upsert(ctx, model.CleaningKeyword{
		ID: created.ID, Pattern: pattern, Category: "refusal", Note: "只改备注",
	})
	if err != nil {
		t.Fatalf("partial update must succeed: %v", err)
	}
	if partial.MatchMode != "regex" {
		t.Fatalf("omitted matchMode must keep existing value, got %q", partial.MatchMode)
	}
	if partial.Severity != "warn" {
		t.Fatalf("omitted severity must keep existing value, got %q", partial.Severity)
	}
	if partial.Note != "只改备注" {
		t.Fatalf("note must be updated, got %q", partial.Note)
	}

	// 用例 5：内置关键词改身份要给内置专属错误（提示「只能停用」而非「删除后重建」）。
	var builtinID int64
	var builtinPattern string
	err = pool.QueryRow(ctx,
		`SELECT id, pattern FROM cleaning_keywords WHERE is_builtin = TRUE ORDER BY id LIMIT 1`).
		Scan(&builtinID, &builtinPattern)
	if err != nil {
		t.Skipf("输入缺失: 库中无内置关键词（先调 POST /cleaning/keywords/seed），跳过内置词用例: %v", err)
	}
	_, err = keywords.Upsert(ctx, model.CleaningKeyword{
		ID: builtinID, Pattern: builtinPattern + "-改了", Category: "refusal",
		MatchMode: "contains", Severity: "block", IsActive: true,
	})
	if !errors.Is(err, cleaning.ErrBuiltinKeywordIdentityImmutable) {
		t.Fatalf("builtin identity change must fail with ErrBuiltinKeywordIdentityImmutable, got %v", err)
	}
}
