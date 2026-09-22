package store

import (
	"context"
	"os"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 这些测试需要真实 Postgres。未配置时跳过并明确标注「输入缺失」，
// 不伪造通过。
//
// 跑法（在 lane worktree 里，借 compose 网络）：
//
//	docker run --rm --network llm_default \
//	  -e POSTGRES_DSN='postgres://llm_factory:llm_factory_dev@postgres:5432/llm_factory?sslmode=disable' \
//	  -v <worktree>:/w -w /w golang:1.24-alpine go test ./internal/store/ -v
//
// testPool 返回一个连到真实 Postgres 的连接池。
//
// DSN 优先读 **LLM_TEST_POSTGRES_DSN**（仓库其余集成测试的统一变量，
// 也是 scripts/go-test-postgres.sh 与 CI integration job 设置的变量），
// 再回退到旧的 POSTGRES_DSN。
//
// 为什么要统一（T32 发现的真实漏洞）：本文件此前只读 POSTGRES_DSN，
// 于是 CI 里即使提供了 LLM_TEST_POSTGRES_DSN，本文件的 6 个用例也会
// 静默 Skip —— 它们从未在 CI 跑过，而 `go test` 仍然全绿。
// 「集成测试全部 Skip 也绿」正是 T32 验收项要拦的形态。
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("输入缺失：未设置 LLM_TEST_POSTGRES_DSN（或 POSTGRES_DSN），跳过需要真实 Postgres 的测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fixtureDataset 建一个隔离的测试数据集（含 1 个 level=2 方向），
// 返回数据集 id 并在测试结束时清理。
func fixtureDataset(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	var datasetID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, target_size, status, provider_id)
    VALUES ($1, $2, 1, 'draft', 1) RETURNING id`,
		"store-v2-test", "测试主题").Scan(&datasetID); err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	// questions.domain_id 有外键约束，需要真实 domain 行。
	if _, err := pool.Exec(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name, level, source, review_status)
    VALUES ($1, '测试方向', '测试方向', 2, 'ai', 'draft')`, datasetID); err != nil {
		t.Fatalf("create domain: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM datasets WHERE id = $1`, datasetID)
	})
	return datasetID
}

func domainIDOf(t *testing.T, pool *pgxpool.Pool, datasetID int64) int64 {
	t.Helper()
	var domainID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM domains WHERE dataset_id = $1 AND level = 2 LIMIT 1`, datasetID).Scan(&domainID); err != nil {
		t.Fatalf("lookup domain: %v", err)
	}
	return domainID
}

// newQuestion 构造一条测试问题。
//
// DedupeKey 对 store 层是不透明的：它由 llm 包的 DedupeKey 计算后随
// model.Question 传入。store 无法 import llm（llm 已 import store，会成环），
// 因此测试直接给定显式键值 —— 这恰好能精确验证「store 是否按 dedupe_key 去重」，
// 而不必复制算法。
func newQuestion(datasetID, domainID int64, content, dedupeKey string) model.Question {
	return model.Question{
		DatasetID:         datasetID,
		DomainID:          domainID,
		DirectionDomainID: domainID,
		Content:           content,
		CanonicalHash:     CanonicalHash(content),
		DedupeKey:         dedupeKey,
		Difficulty:        "medium",
		DifficultyScore:   2,
		Source:            "ai",
	}
}

// 跨批次的近重复必须被应用层拦下。
//
// 这是本 lane 发现并修复的真实缺口：`idx_questions_dataset_dedupe` 是
// **非唯一**索引，只有 `(dataset_id, canonical_hash)` 是 UNIQUE。所以
// 「同一问题、不同标点」这种近重复在第二次写入时不会触发 DB 冲突，
// 必须靠 ExistingDedupeKeys 的应用层先查拦截。
//
// 本用例刻意让两条记录的 canonical_hash 不同、dedupe_key 相同，
// 从而精确复现该场景。
func TestInsertQuestionsSkipsCrossBatchNearDuplicates(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	const sharedDedupeKey = "shared-dedupe-key-for-near-duplicate"

	first := newQuestion(datasetID, domainID, "在东海海域有巡逻编队遇到不明目标，请做出规划。", sharedDedupeKey)
	got, err := store.InsertQuestions(ctx, datasetID, []model.Question{first})
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if got.Inserted != 1 || got.Skipped != 0 {
		t.Fatalf("first insert = %+v, want inserted=1 skipped=0", got)
	}

	// 同一问题的不同标点写法：内容不同 → canonical_hash 不同（不会触发 DB UNIQUE），
	// 但 dedupe_key 相同 —— 必须被应用层拦下。
	nearDuplicate := newQuestion(datasetID, domainID, "在东海海域有巡逻编队遇到不明目标, 请做出规划!", sharedDedupeKey)
	if nearDuplicate.CanonicalHash == first.CanonicalHash {
		t.Fatal("precondition failed: variants must have different canonical_hash, otherwise the DB constraint would mask the app-layer check")
	}

	got, err = store.InsertQuestions(ctx, datasetID, []model.Question{nearDuplicate})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if got.Inserted != 0 || got.Skipped != 1 {
		t.Fatalf("near-duplicate insert = %+v, want inserted=0 skipped=1 (app-layer pre-check must catch it)", got)
	}

	total, err := store.CountQuestions(ctx, datasetID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 1 {
		t.Fatalf("question count = %d, want 1", total)
	}
}

// 精确重复由 DB UNIQUE(dataset_id, canonical_hash) 兜底。
func TestInsertQuestionsSkipsExactDuplicates(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	question := newQuestion(datasetID, domainID, "完全相同的文本", "exact-dup-key")
	if _, err := store.InsertQuestions(ctx, datasetID, []model.Question{question}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	got, err := store.InsertQuestions(ctx, datasetID, []model.Question{question})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if got.Inserted != 0 || got.Skipped != 1 {
		t.Fatalf("exact duplicate = %+v, want inserted=0 skipped=1", got)
	}
}

// 批内重复只保留第一条，且不同 dedupe_key 的正常问题都要写入。
func TestInsertQuestionsDeduplicatesWithinBatch(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	first := newQuestion(datasetID, domainID, "批内重复测试", "batch-key-1")
	second := newQuestion(datasetID, domainID, "批内重复测试的副本", "batch-key-1") // 同键，应被跳过
	third := newQuestion(datasetID, domainID, "另一个不同的问题", "batch-key-2")

	got, err := store.InsertQuestions(ctx, datasetID, []model.Question{first, second, third})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got.Inserted != 2 || got.Skipped != 1 {
		t.Fatalf("batch dedupe = %+v, want inserted=2 skipped=1", got)
	}
}

// 不同 dedupe_key 的问题必须全部写入（防止过度去重）。
func TestInsertQuestionsKeepsDistinctContent(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	questions := []model.Question{
		newQuestion(datasetID, domainID, "问题甲", "key-a"),
		newQuestion(datasetID, domainID, "问题乙", "key-b"),
		newQuestion(datasetID, domainID, "问题丙", "key-c"),
	}
	got, err := store.InsertQuestions(ctx, datasetID, []model.Question(questions))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got.Inserted != 3 || got.Skipped != 0 {
		t.Fatalf("distinct insert = %+v, want inserted=3 skipped=0", got)
	}
}

// DifficultyStats 必须恒含三档（缺档补 0），便于前端直接渲染。
func TestDifficultyStatsAlwaysIncludesThreeLevels(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	question := newQuestion(datasetID, domainID, "只有困难档的数据", "hard-only-key")
	question.Difficulty = "hard"
	question.DifficultyScore = 3
	if _, err := store.InsertQuestions(ctx, datasetID, []model.Question{question}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	stats, err := store.DifficultyStats(ctx, datasetID)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Total != 1 {
		t.Fatalf("total = %d, want 1", stats.Total)
	}
	for _, level := range []string{"easy", "medium", "hard"} {
		if _, exists := stats.Levels[level]; !exists {
			t.Errorf("levels missing %q: %v", level, stats.Levels)
		}
	}
	if stats.Levels["hard"] != 1 || stats.Levels["easy"] != 0 {
		t.Errorf("unexpected levels: %v", stats.Levels)
	}
}

// ListDirections 只返回 level=2 方向；未生成长链标准步骤时返回空步骤而不报错。
//
// 这条用例锁定了本 lane 修复的一个真实缺陷：steps 存在
// chain_standard_versions 而非 chain_standards，早期查询直接 SQL 报错
// （column cs.steps does not exist）。
func TestListDirectionsReadsOnlyLevelTwoAndToleratesMissingChainStandards(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()

	// fixture 已建 1 个 level=2 方向；再加一个 level=1 领域，它不应出现在结果里。
	if _, err := pool.Exec(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name, level, source, review_status)
    VALUES ($1, '测试领域', '测试领域', 1, 'ai', 'draft')`, datasetID); err != nil {
		t.Fatalf("create level-1 domain: %v", err)
	}

	directions, err := store.ListDirections(ctx, datasetID)
	if err != nil {
		t.Fatalf("ListDirections: %v", err)
	}
	if len(directions) != 1 {
		t.Fatalf("directions = %d, want 1 (only level=2)", len(directions))
	}
	if directions[0].DomainName != "测试方向" {
		t.Errorf("unexpected direction: %+v", directions[0])
	}
	if len(directions[0].ChainSteps) != 0 {
		t.Errorf("expected empty chain steps when no standards exist, got %+v", directions[0].ChainSteps)
	}
}

// 已生成长链标准步骤时，ListDirections 必须取到 current_version 对应的 steps。
func TestListDirectionsReadsCurrentChainStandardVersion(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	var standardID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO chain_standards (dataset_id, domain_id, direction_key, current_version, status)
    VALUES ($1, $2, '测试方向', 2, 'generated') RETURNING id`, datasetID, domainID).Scan(&standardID); err != nil {
		t.Fatalf("create chain standard: %v", err)
	}
	// 版本 1 是旧版本，版本 2 才是 current —— 必须取到版本 2 而不是版本 1。
	if _, err := pool.Exec(ctx, `
    INSERT INTO chain_standard_versions (standard_id, version, steps, source) VALUES
    ($1, 1, '[{"index":1,"title":"旧步骤"}]'::jsonb, 'ai'),
    ($1, 2, '[{"index":1,"title":"当前步骤","description":"描述","checkpoint":"判断点"}]'::jsonb, 'user')`,
		standardID); err != nil {
		t.Fatalf("create chain standard versions: %v", err)
	}

	directions, err := store.ListDirections(ctx, datasetID)
	if err != nil {
		t.Fatalf("ListDirections: %v", err)
	}
	if len(directions) != 1 {
		t.Fatalf("directions = %d, want 1", len(directions))
	}
	steps := directions[0].ChainSteps
	if len(steps) != 1 || steps[0].Title != "当前步骤" {
		t.Fatalf("expected current version step, got %+v", steps)
	}
	if steps[0].Checkpoint != "判断点" {
		t.Errorf("checkpoint not decoded: %+v", steps[0])
	}
}

// ExistingDedupeKeys 只返回确实存在的键。
func TestExistingDedupeKeysReturnsOnlyPresentKeys(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)
	ctx := context.Background()
	domainID := domainIDOf(t, pool, datasetID)

	if _, err := store.InsertQuestions(ctx, datasetID,
		[]model.Question{newQuestion(datasetID, domainID, "已存在问题", "present-key")}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	existing, err := store.ExistingDedupeKeys(ctx, datasetID, []string{"present-key", "absent-key"})
	if err != nil {
		t.Fatalf("ExistingDedupeKeys: %v", err)
	}
	if _, ok := existing["present-key"]; !ok {
		t.Errorf("expected present-key to exist, got %v", existing)
	}
	if _, ok := existing["absent-key"]; ok {
		t.Errorf("absent-key must not be reported as existing, got %v", existing)
	}
}

// 空键列表不应触发查询，也不应报错。
func TestExistingDedupeKeysHandlesEmptyInput(t *testing.T) {
	pool := testPool(t)
	datasetID := fixtureDataset(t, pool)
	store := NewQuestionStoreV2(pool)

	existing, err := store.ExistingDedupeKeys(context.Background(), datasetID, nil)
	if err != nil {
		t.Fatalf("ExistingDedupeKeys: %v", err)
	}
	if len(existing) != 0 {
		t.Fatalf("expected empty map, got %v", existing)
	}
}
