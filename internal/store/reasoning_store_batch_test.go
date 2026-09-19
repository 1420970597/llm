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

// 这组测试锁定 issue #5 的**状态语义**：Insert 按整批记录推进 datasets.status。
//
// 语义本身落在 SQL（按 status 计数 → 决定 nextStatus），纯函数单测无法证明，
// 因此用真实 Postgres 做集成测试。未设置 LLM_TEST_POSTGRES_DSN 时跳过，
// 避免在无数据库环境里伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestBatchStatus -v"
//
// 反污染：所有行都用唯一前缀 l15-r2-<pid>- 命名，结束时按精确 id 级联清理
// （datasets 删除会 CASCADE 掉 questions / reasoning_records / reward_records）。
func TestBatchStatusInsertAdvancesDatasetStatusFromWholeBatch(t *testing.T) {
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

	fixture := newBatchStatusFixture(t, ctx, pool)
	defer fixture.cleanup(t, ctx, pool)

	reasoning := NewReasoningStore(pool)

	// 一批 3 条：1 条 generated、1 条 failed、1 条 invalid。
	// 关键断言：只要**整批里任何一条**不可用，数据集就不能被标成 reasoning_generated。
	// 这正是 issue #5 的判定点——原实现按「当次迭代那一条」推算，会写出错误的终态。
	records := []model.ReasoningRecord{
		{QuestionID: fixture.questionIDs[0], AnswerSummary: "答案一", Reasoning: "推理一", Status: "generated"},
		{QuestionID: fixture.questionIDs[1], AnswerSummary: "答案二", Reasoning: "推理二", Status: "failed"},
		{QuestionID: fixture.questionIDs[2], AnswerSummary: "答案三", Reasoning: "推理三", Status: "invalid"},
	}
	if err := reasoning.Insert(ctx, fixture.datasetID, records); err != nil {
		t.Fatalf("Insert 失败: %v", err)
	}

	status := fixture.datasetStatus(t, ctx, pool)
	if status != "reasoning_partial" {
		t.Fatalf("整批含 failed+invalid 时应为 reasoning_partial，实际 %q（状态领先于可用记录数即是 issue #5）", status)
	}

	// 记录数必须与状态一致：3 条记录全部落库，其中只有 1 条是 generated。
	var total, generated int
	if err := pool.QueryRow(ctx, `
    SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'generated')
    FROM reasoning_records WHERE dataset_id = $1`, fixture.datasetID).Scan(&total, &generated); err != nil {
		t.Fatalf("统计 reasoning_records 失败: %v", err)
	}
	if total != 3 || generated != 1 {
		t.Fatalf("期望落库 3 条且其中 1 条 generated，实际 total=%d generated=%d", total, generated)
	}
}

// invalid 不得被计入 generated：整批都是占位内容时数据集必须是 reasoning_failed，
// 而不是 reasoning_generated。这是 issue #7（invalid 状态）与 #5（状态领先）的交界处。
func TestBatchStatusAllInvalidIsFailedNotGenerated(t *testing.T) {
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

	fixture := newBatchStatusFixture(t, ctx, pool)
	defer fixture.cleanup(t, ctx, pool)

	reasoning := NewReasoningStore(pool)
	records := []model.ReasoningRecord{
		{QuestionID: fixture.questionIDs[0], AnswerSummary: "...", Reasoning: "...", Status: "invalid"},
		{QuestionID: fixture.questionIDs[1], AnswerSummary: "...", Reasoning: "...", Status: "invalid"},
		{QuestionID: fixture.questionIDs[2], AnswerSummary: "...", Reasoning: "...", Status: "invalid"},
	}
	if err := reasoning.Insert(ctx, fixture.datasetID, records); err != nil {
		t.Fatalf("Insert 失败: %v", err)
	}

	if status := fixture.datasetStatus(t, ctx, pool); status != "reasoning_failed" {
		t.Fatalf("整批 invalid 时应为 reasoning_failed（invalid 不可计入 generated），实际 %q", status)
	}
}

// 未识别状态必须按「不可用」处理，而不是默认计入 generated。
//
// 这是防回归的保险：将来若有人新增状态值却忘了更新这里的统计，默认分支会保守地
// 把数据集停在 partial，而不是声称整批已生成。
func TestBatchStatusUnknownStatusCountsAsUnavailable(t *testing.T) {
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

	fixture := newBatchStatusFixture(t, ctx, pool)
	defer fixture.cleanup(t, ctx, pool)

	reasoning := NewReasoningStore(pool)
	records := []model.ReasoningRecord{
		{QuestionID: fixture.questionIDs[0], AnswerSummary: "答案", Reasoning: "推理", Status: "generated"},
		{QuestionID: fixture.questionIDs[1], AnswerSummary: "答案", Reasoning: "推理", Status: "some-future-status"},
		{QuestionID: fixture.questionIDs[2], AnswerSummary: "答案", Reasoning: "推理", Status: "generated"},
	}
	if err := reasoning.Insert(ctx, fixture.datasetID, records); err != nil {
		t.Fatalf("Insert 失败: %v", err)
	}

	if status := fixture.datasetStatus(t, ctx, pool); status != "reasoning_partial" {
		t.Fatalf("含未识别状态时应保守停在 reasoning_partial，实际 %q", status)
	}
}

// UpsertPartial 只落记录、不推进数据集状态。
//
// 用途：worker 在整批跑不完时（例如对象存储写入失败）保住已拿到的记录，
// 同时**不能**声称这一批已完成。调用方随后会把数据集标成 *_failed。
func TestBatchStatusUpsertPartialDoesNotAdvanceDatasetStatus(t *testing.T) {
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

	fixture := newBatchStatusFixture(t, ctx, pool)
	defer fixture.cleanup(t, ctx, pool)

	before := fixture.datasetStatus(t, ctx, pool)

	reasoning := NewReasoningStore(pool)
	if err := reasoning.UpsertPartial(ctx, fixture.datasetID, []model.ReasoningRecord{
		{QuestionID: fixture.questionIDs[0], AnswerSummary: "答案", Reasoning: "推理", Status: "generated"},
	}); err != nil {
		t.Fatalf("UpsertPartial 失败: %v", err)
	}

	after := fixture.datasetStatus(t, ctx, pool)
	if after != before {
		t.Fatalf("UpsertPartial 不得推进数据集状态：before=%q after=%q", before, after)
	}

	// 但记录必须已经落库（否则「保住已完成的工作」这个目的就落空了）。
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM reasoning_records WHERE dataset_id = $1`, fixture.datasetID).Scan(&count); err != nil {
		t.Fatalf("统计 reasoning_records 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("UpsertPartial 应落库 1 条记录，实际 %d", count)
	}
}

// RewardStore 的整批语义与 ReasoningStore 对称。
func TestBatchStatusRewardInsertCountsInvalidAsUnavailable(t *testing.T) {
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

	fixture := newBatchStatusFixture(t, ctx, pool)
	defer fixture.cleanup(t, ctx, pool)

	rewards := NewRewardStore(pool)
	records := []model.RewardRecord{
		{QuestionID: fixture.questionIDs[0], Score: 0.9, Status: "generated"},
		{QuestionID: fixture.questionIDs[1], Score: 0, Status: "invalid"},
		{QuestionID: fixture.questionIDs[2], Score: 0.8, Status: "generated"},
	}
	if err := rewards.Insert(ctx, fixture.datasetID, records); err != nil {
		t.Fatalf("Insert 失败: %v", err)
	}

	if status := fixture.datasetStatus(t, ctx, pool); status != "rewards_partial" {
		t.Fatalf("整批含 invalid 时应为 rewards_partial，实际 %q", status)
	}
}

// batchStatusFixture 是一组临时数据集 + 题目的测试夹具。
type batchStatusFixture struct {
	datasetID   int64
	questionIDs []int64
}

// newBatchStatusFixture 建 1 个数据集 + 3 道题。
// 名字带 l15-r2-<pid>- 唯一前缀，便于在共享库里辨认与清理。
func newBatchStatusFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) batchStatusFixture {
	t.Helper()

	// 先把所有写入值算好，再执行只含 $N 占位符的语句。
	// 这样「值」与「SQL 文本」在代码上完全分离：SQL 永远是常量字面量，
	// 不存在任何拼接路径。
	prefix := batchStatusPrefix()
	datasetName := prefix + "dataset"
	domainName := prefix + "领域"
	domainCanonical := prefix + "domain"

	var datasetID int64
	// root_keyword 是 NOT NULL 且无默认值（见 datasets 表定义）。
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status)
    VALUES ($1, $2, $3)
    RETURNING id`, datasetName, prefix+"批处理状态测试", "questions_generated").Scan(&datasetID); err != nil {
		t.Fatalf("创建数据集失败: %v", err)
	}

	var domainID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name, level)
    VALUES ($1, $2, $3, $4)
    RETURNING id`, datasetID, domainName, domainCanonical, 1).Scan(&domainID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE id = $1`, datasetID)
		t.Fatalf("创建领域失败: %v", err)
	}

	questionIDs := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		content := fmt.Sprintf("%s测试题目 %d", prefix, i)
		hash := fmt.Sprintf("%shash-%d", prefix, i)

		var questionID int64
		if err := pool.QueryRow(ctx, `
      INSERT INTO questions (dataset_id, domain_id, content, canonical_hash, status)
      VALUES ($1, $2, $3, $4, $5)
      RETURNING id`, datasetID, domainID, content, hash, "generated").Scan(&questionID); err != nil {
			_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE id = $1`, datasetID)
			t.Fatalf("创建题目失败: %v", err)
		}
		questionIDs = append(questionIDs, questionID)
	}

	return batchStatusFixture{datasetID: datasetID, questionIDs: questionIDs}
}

// batchStatusPrefix 返回本测试创建行的唯一前缀（契约 §6.2）。
// 带 pid 与纳秒时间戳，便于在共享开发库里辨认归属并手工清理残留。
func batchStatusPrefix() string {
	return fmt.Sprintf("l15-r2-%d-%d-", os.Getpid(), time.Now().UnixNano())
}

func (f batchStatusFixture) datasetStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM datasets WHERE id = $1`, f.datasetID).Scan(&status); err != nil {
		t.Fatalf("读取数据集状态失败: %v", err)
	}
	return status
}

// cleanup 按精确 id 删除，级联清掉本夹具创建的 domains / questions / 记录。
// 绝不使用无 WHERE 的批量删除（契约 §6.2）。
func (f batchStatusFixture) cleanup(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if f.datasetID == 0 {
		return
	}
	if _, err := pool.Exec(ctx, `DELETE FROM datasets WHERE id = $1`, f.datasetID); err != nil {
		t.Errorf("清理夹具数据集 %d 失败（请手工清理）: %v", f.datasetID, err)
	}
}
