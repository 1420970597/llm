package store

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 版本化语义落在 SQL（ON CONFLICT 自增 + 版本表插入），纯函数单测无法证明，
// 因此这里用真实 Postgres 做集成测试。未设置 LLM_TEST_POSTGRES_DSN 时跳过，
// 避免在无数据库环境里伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestChainStandardVersioning -v"
func TestChainStandardVersioningIntegration(t *testing.T) {
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

	fixture, err := seedChainStandardFixture(ctx, pool)
	if err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE id = $1`, fixture.datasetID)
	}()

	chainStore := NewChainStandardStore(pool)
	domainID := fixture.domainID

	// 第 1 步：AI 生成 → version=1，版本表 1 行。
	aiSteps := []model.ChainStep{
		{Index: 1, Title: "澄清目标", Description: "确认任务边界", Checkpoint: "目标可复述"},
		{Index: 2, Title: "识别约束", Description: "列出硬约束", Checkpoint: "约束清单非空"},
	}
	created, err := chainStore.UpsertFromAI(ctx, fixture.datasetID, domainID, "海上巡逻", aiSteps)
	if err != nil {
		t.Fatalf("upsert from ai: %v", err)
	}
	if created.CurrentVersion != 1 {
		t.Fatalf("first AI generation must be version 1, got %d", created.CurrentVersion)
	}
	if len(created.Steps) != 2 {
		t.Fatalf("expected 2 steps persisted, got %d", len(created.Steps))
	}
	if created.DomainName != "海上巡逻" {
		t.Fatalf("domain name must be joined from domains, got %q", created.DomainName)
	}

	versions, err := chainStore.ListVersions(ctx, fixture.datasetID, domainID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Source != "ai" {
		t.Fatalf("expected 1 ai version, got %+v", versions)
	}

	// 第 2 步：用户编辑两次 → current_version=3，版本表 3 行。
	// 每轮基于上一轮结果追加一个步骤（不能基于 aiSteps 反复 append，否则会因切片复用而不累加）。
	currentSteps := aiSteps
	for edit := 1; edit <= 2; edit++ {
		grown := make([]model.ChainStep, 0, len(currentSteps)+1)
		grown = append(grown, currentSteps...)
		grown = append(grown, model.ChainStep{
			Index:       len(currentSteps) + 1,
			Title:       fmt.Sprintf("补充步骤%d", edit),
			Description: "编辑轮次",
			Checkpoint:  "已人工复核",
		})
		currentSteps = grown

		updated, err := chainStore.UpdateStepsWithVersion(ctx, fixture.datasetID, domainID, currentSteps, "第 N 次调整", 7)
		if err != nil {
			t.Fatalf("edit %d failed: %v", edit, err)
		}
		if updated.CurrentVersion != edit+1 {
			t.Fatalf("after edit %d current_version must be %d, got %d", edit, edit+1, updated.CurrentVersion)
		}
		if len(updated.Steps) != len(currentSteps) {
			t.Fatalf("after edit %d expected %d steps, got %d", edit, len(currentSteps), len(updated.Steps))
		}
	}

	final, err := chainStore.GetByDomain(ctx, fixture.datasetID, domainID)
	if err != nil {
		t.Fatalf("get by domain: %v", err)
	}
	if final.CurrentVersion != 3 {
		t.Fatalf("after two edits current_version must be 3, got %d", final.CurrentVersion)
	}
	if final.Status != "edited" {
		t.Fatalf("status must become edited, got %q", final.Status)
	}
	// 关键：返回的 steps 必须是当前版本的内容，而不是首个版本。
	// 每次编辑都追加一个步骤，因此两轮编辑后应为 2+2=4 步。
	if len(final.Steps) != 4 {
		t.Fatalf("GetByDomain must return current version steps (4), got %d: %+v", len(final.Steps), final.Steps)
	}
	if final.Steps[3].Title != "补充步骤2" {
		t.Fatalf("current steps must include the latest edit, got %+v", final.Steps)
	}
	if final.Steps[0].Index != 1 || final.Steps[3].Index != 4 {
		t.Fatalf("steps must stay indexed 1..4, got %+v", final.Steps)
	}

	versions, err = chainStore.ListVersions(ctx, fixture.datasetID, domainID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions after generate + 2 edits, got %d", len(versions))
	}
	if versions[0].Version != 3 || versions[2].Version != 1 {
		t.Fatalf("versions must be ordered DESC, got %d..%d", versions[0].Version, versions[2].Version)
	}
	if versions[0].Source != "user" || versions[2].Source != "ai" {
		t.Fatalf("version sources wrong: newest=%q oldest=%q", versions[0].Source, versions[2].Source)
	}
	if len(versions[0].Steps) != 4 {
		t.Fatalf("newest version must carry the 4 edited steps, got %d", len(versions[0].Steps))
	}
	if len(versions[2].Steps) != 2 {
		t.Fatalf("oldest version must keep the original 2 steps, got %d", len(versions[2].Steps))
	}
	if versions[0].CreatedBy != 7 {
		t.Fatalf("user version must record created_by, got %d", versions[0].CreatedBy)
	}
	if versions[0].ChangeNote != "第 N 次调整" {
		t.Fatalf("change note must be persisted, got %q", versions[0].ChangeNote)
	}

	// 第 3 步：再次 AI 生成（重跑）→ 版本继续自增，不覆盖历史。
	regenerated, err := chainStore.UpsertFromAI(ctx, fixture.datasetID, domainID, "海上巡逻", aiSteps)
	if err != nil {
		t.Fatalf("regenerate failed: %v", err)
	}
	if regenerated.CurrentVersion != 4 {
		t.Fatalf("regeneration must bump to 4, got %d", regenerated.CurrentVersion)
	}
	versions, err = chainStore.ListVersions(ctx, fixture.datasetID, domainID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 4 {
		t.Fatalf("expected 4 versions, got %d", len(versions))
	}
}

// TestChainStandardVersioningRejectsUnknownDomainIntegration 验证对不存在的方向编辑会报错。
func TestChainStandardVersioningRejectsUnknownDomainIntegration(t *testing.T) {
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

	fixture, err := seedChainStandardFixture(ctx, pool)
	if err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE id = $1`, fixture.datasetID)
	}()

	chainStore := NewChainStandardStore(pool)
	// 未生成过标准步骤的方向，编辑必须失败而不是静默创建。
	if _, err := chainStore.UpdateStepsWithVersion(ctx, fixture.datasetID, fixture.domainID,
		[]model.ChainStep{{Title: "步骤"}}, "note", 0); err == nil {
		t.Fatal("expected error when editing a direction without an existing standard")
	}

	if _, err := chainStore.UpsertFromAI(ctx, fixture.datasetID, fixture.domainID, "x", nil); err == nil {
		t.Fatal("expected error when persisting empty steps")
	}
}

type chainStandardFixture struct {
	datasetID int64
	domainID  int64
}

// seedChainStandardFixture 建一个临时数据集与方向，供版本化测试使用。
func seedChainStandardFixture(ctx context.Context, pool *pgxpool.Pool) (chainStandardFixture, error) {
	var fixture chainStandardFixture
	err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status)
    VALUES ('l2-chain-standard-test', '测试关键词', 'draft')
    RETURNING id`).Scan(&fixture.datasetID)
	if err != nil {
		return fixture, err
	}
	err = pool.QueryRow(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name, level, source, review_status)
    VALUES ($1, '海上巡逻', '海上巡逻', 2, 'ai', 'draft')
    RETURNING id`, fixture.datasetID).Scan(&fixture.domainID)
	if err != nil {
		return fixture, err
	}
	return fixture, nil
}
