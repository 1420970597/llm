package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// eval_runs / eval_items / eval_item_scores 的幂等语义落在 SQL
// （ON CONFLICT DO NOTHING / DO UPDATE、CASE 保留 error_summary），
// 纯函数单测无法证明，因此这里用真实 Postgres 做集成测试。
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过，避免在无数据库环境里伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestEvalRunStoreIntegration -v"
func TestEvalRunStoreIntegration(t *testing.T) {
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

	store := NewEvalRunStore(pool)

	// 造一个临时数据集，测试结束后连同级联数据一起清掉。
	// 注意 root_keyword 是 NOT NULL 且无默认值（见 datasets 表定义）。
	var datasetID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status)
    VALUES ($1, 'L9 集成测试关键词', 'draft')
    RETURNING id`, fmt.Sprintf("l9-integration-%d", time.Now().UnixNano())).Scan(&datasetID); err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE id = $1`, datasetID)
	}()

	run, err := store.CreateRun(ctx, model.EvalRunCreateRequest{
		DatasetID:     datasetID,
		Name:          "集成测试运行",
		SamplingMode:  "count",
		SampleSize:    3,
		TargetKind:    "sft",
		DimensionKeys: []string{"long_chain.depth", "faithfulness.grounded"},
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.ID <= 0 {
		t.Fatalf("CreateRun returned a non-positive id: %d", run.ID)
	}
	if run.Status != "draft" {
		t.Errorf("new run must start as draft, got %q", run.Status)
	}
	if len(run.DimensionKeys) != 2 {
		t.Errorf("dimension_keys round-trip failed, got %v", run.DimensionKeys)
	}

	// 造一个领域：questions.domain_id 是指向 domains 的外键，不能为 0。
	var domainID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name, level, source)
    VALUES ($1, 'L9 集成测试领域', 'l9-integration-domain', 1, 'ai')
    RETURNING id`, datasetID).Scan(&domainID); err != nil {
		t.Fatalf("create domain: %v", err)
	}

	// 造两个问题作为被评条目。canonical_hash 是 NOT NULL 且无默认值。
	questionIDs := make([]int64, 0, 2)
	for index := 0; index < 2; index++ {
		content := fmt.Sprintf("集成测试问题 %d", index)
		var questionID int64
		if err := pool.QueryRow(ctx, `
      INSERT INTO questions (dataset_id, domain_id, content, canonical_hash, status)
      VALUES ($1, $2, $3, $4, 'generated')
      RETURNING id`, datasetID, domainID, content, fmt.Sprintf("l9-hash-%d-%d", datasetID, index)).Scan(&questionID); err != nil {
			t.Fatalf("create question: %v", err)
		}
		questionIDs = append(questionIDs, questionID)
	}

	items := []EvalItemInput{
		{QuestionID: questionIDs[0], ItemIndex: 0, Payload: map[string]any{"question": "q0"}},
		{QuestionID: questionIDs[1], ItemIndex: 1, Payload: map[string]any{"question": "q1"}},
	}
	inserted, err := store.InsertItems(ctx, run.ID, datasetID, items)
	if err != nil {
		t.Fatalf("InsertItems: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("expected 2 inserted items, got %d", inserted)
	}

	// 幂等性：重复写入同一批问题不应新增条目（UNIQUE + DO NOTHING）。
	again, err := store.InsertItems(ctx, run.ID, datasetID, items)
	if err != nil {
		t.Fatalf("InsertItems (replay): %v", err)
	}
	if again != 0 {
		t.Errorf("replaying the same items must insert nothing, got %d", again)
	}

	listed, err := store.ListItems(ctx, run.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 items, got %d", len(listed))
	}
	if listed[0].Payload["question"] != "q0" {
		t.Errorf("payload round-trip failed: %v", listed[0].Payload)
	}

	// 分数 upsert：同一 (条目, 裁判, 维度) 二次写入应覆盖而非新增。
	itemID := listed[0].ID
	first := EvalScoreInput{
		EvalItemID: itemID, JudgeProviderID: 2, DimensionKey: "long_chain.depth",
		Score: 4, Rationale: "第一次", RawResponse: `{"score":4}`, Status: "scored",
	}
	if _, err := store.UpsertScores(ctx, run.ID, []EvalScoreInput{first}); err != nil {
		t.Fatalf("UpsertScores: %v", err)
	}

	second := first
	second.Score = 9
	second.Rationale = "第二次覆盖"
	if _, err := store.UpsertScores(ctx, run.ID, []EvalScoreInput{second}); err != nil {
		t.Fatalf("UpsertScores (overwrite): %v", err)
	}

	scores, err := store.ListScores(ctx, run.ID, 0, "")
	if err != nil {
		t.Fatalf("ListScores: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("upsert must not create a duplicate row, got %d rows", len(scores))
	}
	if scores[0].Score != 9 || scores[0].Rationale != "第二次覆盖" {
		t.Errorf("upsert must overwrite with the newer value, got score=%v rationale=%q",
			scores[0].Score, scores[0].Rationale)
	}

	// 过滤参数必须真的生效。
	filtered, err := store.ListScores(ctx, run.ID, 999, "")
	if err != nil {
		t.Fatalf("ListScores (judge filter): %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("judge filter for an unused provider must return nothing, got %d rows", len(filtered))
	}
	filtered, err = store.ListScores(ctx, run.ID, 0, "nonexistent.dimension")
	if err != nil {
		t.Fatalf("ListScores (dimension filter): %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("dimension filter for an unused key must return nothing, got %d rows", len(filtered))
	}

	// 进度回写：空 errorSummary 不能清掉已记录的失败原因。
	if err := store.UpdateRunStatus(ctx, run.ID, "running", 2, 1, "裁判 2 超时"); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	if err := store.UpdateRunStatus(ctx, run.ID, "completed", 2, 2, ""); err != nil {
		t.Fatalf("UpdateRunStatus (progress): %v", err)
	}
	updated, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if updated.Status != "completed" || updated.TotalItems != 2 || updated.ScoredItems != 2 {
		t.Errorf("progress fields not persisted: %+v", updated)
	}
	if updated.ErrorSummary != "裁判 2 超时" {
		t.Errorf("a blank errorSummary must not erase the recorded failure, got %q", updated.ErrorSummary)
	}

	// 不存在时返回哨兵错误，供 HTTP 层稳定映射 404。
	if _, err := store.GetRun(ctx, -1); !IsEvalRunNotFound(err) {
		t.Errorf("missing run must return ErrEvalRunNotFound, got %v", err)
	}

	// 按数据集过滤。
	runs, err := store.ListRuns(ctx, datasetID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 run for the dataset, got %d", len(runs))
	}
}
