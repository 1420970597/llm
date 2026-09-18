package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EvalSummaryStore 负责评估报告的持久化读取与汇总行写入。
//
// 表定义见 sql/migrations/0011_eval_core.sql（eval_summaries / eval_runs /
// eval_item_scores）。契约：docs/plans/eval-and-cleaning-plan.md 第 3.10 节。
//
// 职责边界（重要）：L9 负责 eval_items / eval_item_scores 的**写入**，
// 其存储层文件是 eval_store_items.go。本文件只**读**这两张表，
// 不提供任何写接口 —— 两条 lane 并行开发时避免争抢同一份写入逻辑。
type EvalSummaryStore struct {
	db *pgxpool.Pool
}

func NewEvalSummaryStore(db *pgxpool.Pool) *EvalSummaryStore {
	return &EvalSummaryStore{db: db}
}

// ErrEvalRunNotFound 表示评估运行不存在。
var ErrEvalRunNotFound = errors.New("评估运行不存在")

// IsEvalRunNotFound 判断错误是否为「评估运行不存在」。
func IsEvalRunNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrEvalRunNotFound)
}

// evalRunColumns 与 0011_eval_core.sql 的 eval_runs 列一一对应。
const evalRunColumns = `id, dataset_id, name, sampling_mode, sample_ratio, sample_size,
	target_kind, dimension_keys, judge_provider_ids, generator_provider_id,
	status, total_items, scored_items, error_summary, created_by, created_at, updated_at`

// scanEvalRun 扫描一行 eval_runs。
func scanEvalRun(row pgx.Row) (model.EvalRun, error) {
	var run model.EvalRun
	err := row.Scan(&run.ID, &run.DatasetID, &run.Name, &run.SamplingMode, &run.SampleRatio,
		&run.SampleSize, &run.TargetKind, &run.DimensionKeys, &run.JudgeProviderIDs,
		&run.GeneratorProvider, &run.Status, &run.TotalItems, &run.ScoredItems,
		&run.ErrorSummary, &run.CreatedBy, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return model.EvalRun{}, err
	}
	return run, nil
}

// GetRun 按 id 取评估运行。不存在时返回 pgx.ErrNoRows。
func (s *EvalSummaryStore) GetRun(ctx context.Context, runID int64) (model.EvalRun, error) {
	return scanEvalRun(s.db.QueryRow(ctx,
		`SELECT `+evalRunColumns+` FROM eval_runs WHERE id = $1`, runID))
}

// DatasetName 取数据集名称，供报告展示。
// 数据集已被删除时返回空字符串而不报错 —— 报告仍应能渲染，
// 只是名字位显示为空，不该因为名字取不到就整份报告 500。
func (s *EvalSummaryStore) DatasetName(ctx context.Context, datasetID int64) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM datasets WHERE id = $1`, datasetID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// LoadRunScores 读取某次评估的打分明细。
//
// judgeProviderID <= 0 表示不按裁判过滤；dimensionKey 为空表示不按维度过滤。
// 按 (judge_provider_id, dimension_key, eval_item_id) 升序返回，保证同一请求
// 多次调用结果一致 —— 报告是对外可见的产物，顺序不稳定会让前后两次比对失真。
func (s *EvalSummaryStore) LoadRunScores(
	ctx context.Context, runID int64, judgeProviderID int64, dimensionKey string,
) ([]model.EvalItemScore, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, eval_run_id, eval_item_id, judge_provider_id, dimension_key,
           score, rationale, raw_response, status, created_at
    FROM eval_item_scores
    WHERE eval_run_id = $1
      AND ($2::bigint = 0 OR judge_provider_id = $2)
      AND ($3 = '' OR dimension_key = $3)
    ORDER BY judge_provider_id ASC, dimension_key ASC, eval_item_id ASC`,
		runID, judgeProviderID, strings.TrimSpace(dimensionKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := make([]model.EvalItemScore, 0, 128)
	for rows.Next() {
		var score model.EvalItemScore
		if err := rows.Scan(&score.ID, &score.EvalRunID, &score.EvalItemID,
			&score.JudgeProviderID, &score.DimensionKey, &score.Score,
			&score.Rationale, &score.RawResponse, &score.Status, &score.CreatedAt); err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	return scores, rows.Err()
}

// LoadRunItems 读取某次评估的条目快照（只读，写入由 L9 负责）。
//
// 报告需要 item_index 与 question_id 才能把分数还原成「第几条数据」。
func (s *EvalSummaryStore) LoadRunItems(ctx context.Context, runID int64) ([]model.EvalItem, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, eval_run_id, dataset_id, question_id, item_index, payload, created_at
    FROM eval_items
    WHERE eval_run_id = $1
    ORDER BY item_index ASC, id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.EvalItem, 0, 128)
	for rows.Next() {
		var item model.EvalItem
		if err := rows.Scan(&item.ID, &item.EvalRunID, &item.DatasetID, &item.QuestionID,
			&item.ItemIndex, &item.Payload, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// LoadRunJudges 读取参与该次评估的裁判（含被排除者）。
//
// 被排除的裁判也要返回：报告需要解释「为什么某个模型没参与评分」。
func (s *EvalSummaryStore) LoadRunJudges(ctx context.Context, runID int64) ([]model.EvalRunJudge, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, eval_run_id, provider_id, provider_name, model,
           excluded, exclude_reason, status, scored_items, error_summary, created_at
    FROM eval_run_judges
    WHERE eval_run_id = $1
    ORDER BY provider_id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	judges := make([]model.EvalRunJudge, 0, 8)
	for rows.Next() {
		var judge model.EvalRunJudge
		if err := rows.Scan(&judge.ID, &judge.EvalRunID, &judge.ProviderID,
			&judge.ProviderName, &judge.Model, &judge.Excluded, &judge.ExcludeReason,
			&judge.Status, &judge.ScoredItems, &judge.ErrorSummary, &judge.CreatedAt); err != nil {
			return nil, err
		}
		judges = append(judges, judge)
	}
	return judges, rows.Err()
}

// UpsertSummaries 写入汇总行。
//
// 幂等：同一 (eval_run_id, scope, ref_key) 重复写入时覆盖分数与明细。
// 重复评估同一 run（例如补齐失败条目后重算）必须能安全重跑，
// 因此这里不用「先删后插」，而是靠唯一约束做 upsert。
func (s *EvalSummaryStore) UpsertSummaries(
	ctx context.Context, runID int64, summaries []model.EvalSummary,
) error {
	if len(summaries) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, summary := range summaries {
		detail := summary.Detail
		if detail == nil {
			detail = map[string]any{}
		}
		batch.Queue(`
      INSERT INTO eval_summaries (eval_run_id, scope, ref_key, score, sample_count, detail)
      VALUES ($1, $2, $3, $4, $5, $6)
      ON CONFLICT (eval_run_id, scope, ref_key)
      DO UPDATE SET score = EXCLUDED.score,
                    sample_count = EXCLUDED.sample_count,
                    detail = EXCLUDED.detail`,
			runID, summary.Scope, summary.RefKey, summary.Score, summary.SampleCount, detail)
	}

	results := s.db.SendBatch(ctx, batch)
	defer results.Close()

	for index := range summaries {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("写入汇总行 %s/%s 失败: %w",
				summaries[index].Scope, summaries[index].RefKey, err)
		}
	}
	return nil
}

// ListSummaries 读取某次评估的全部汇总行。
func (s *EvalSummaryStore) ListSummaries(ctx context.Context, runID int64) ([]model.EvalSummary, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, eval_run_id, scope, ref_key, score, sample_count, detail, created_at
    FROM eval_summaries
    WHERE eval_run_id = $1
    ORDER BY scope ASC, ref_key ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := make([]model.EvalSummary, 0, 64)
	for rows.Next() {
		var summary model.EvalSummary
		if err := rows.Scan(&summary.ID, &summary.EvalRunID, &summary.Scope,
			&summary.RefKey, &summary.Score, &summary.SampleCount,
			&summary.Detail, &summary.CreatedAt); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}
