package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件由 L9 lane 独占：评估运行的存储层（表 eval_runs / eval_items /
// eval_item_scores，迁移 0011）。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 2 节与第 3.9 节。
//
// 与 eval_store_judges.go（L7）的分工：那边管 eval_run_judges，
// 这边管运行本体、被评条目与逐条分数。表不同、生命周期不同，故分文件。

// EvalRunStore 读写评估运行、条目与分数。
type EvalRunStore struct {
	db *pgxpool.Pool
}

func NewEvalRunStore(db *pgxpool.Pool) *EvalRunStore {
	return &EvalRunStore{db: db}
}

// ErrEvalRunNotFound 表示评估运行不存在。
//
// 用哨兵错误而非 pgx.ErrNoRows 直接外泄，使 HTTP 层能稳定映射到 404，
// 不必依赖 pgx 的错误类型（那会把存储实现细节泄漏到 API 层）。
var ErrEvalRunNotFound = errors.New("eval run not found")

// IsEvalRunNotFound 判断错误是否为「运行不存在」。
func IsEvalRunNotFound(err error) bool {
	return errors.Is(err, ErrEvalRunNotFound)
}

const evalRunColumns = `id, dataset_id, name, sampling_mode, sample_ratio, sample_size,
       target_kind, dimension_keys, judge_provider_ids, generator_provider_id,
       status, total_items, scored_items, error_summary, created_by, created_at, updated_at`

func scanEvalRun(row pgx.Row) (model.EvalRun, error) {
	var run model.EvalRun
	err := row.Scan(
		&run.ID,
		&run.DatasetID,
		&run.Name,
		&run.SamplingMode,
		&run.SampleRatio,
		&run.SampleSize,
		&run.TargetKind,
		&run.DimensionKeys,
		&run.JudgeProviderIDs,
		&run.GeneratorProvider,
		&run.Status,
		&run.TotalItems,
		&run.ScoredItems,
		&run.ErrorSummary,
		&run.CreatedBy,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	return run, err
}

// CreateRun 创建一次评估运行，初始状态为 draft。
//
// 刻意不在创建时入队：契约把「创建」与「启动」分成两个接口
// （POST /eval/runs 与 POST /eval/runs/{id}/start），
// 用户可以先创建再配置裁判，最后才启动。
func (s *EvalRunStore) CreateRun(ctx context.Context, input model.EvalRunCreateRequest) (model.EvalRun, error) {
	if input.DatasetID <= 0 {
		return model.EvalRun{}, fmt.Errorf("dataset id must be positive, got %d", input.DatasetID)
	}

	dimensionKeys := input.DimensionKeys
	if dimensionKeys == nil {
		dimensionKeys = []string{}
	}
	judgeProviderIDs := input.JudgeProviderIDs
	if judgeProviderIDs == nil {
		judgeProviderIDs = []int64{}
	}

	row := s.db.QueryRow(ctx, `
    INSERT INTO eval_runs
      (dataset_id, name, sampling_mode, sample_ratio, sample_size, target_kind,
       dimension_keys, judge_provider_ids, generator_provider_id, status)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft')
    RETURNING `+evalRunColumns,
		input.DatasetID,
		input.Name,
		input.SamplingMode,
		input.SampleRatio,
		input.SampleSize,
		input.TargetKind,
		dimensionKeys,
		judgeProviderIDs,
		input.GeneratorProvider,
	)
	return scanEvalRun(row)
}

// GetRun 按 id 取运行。不存在时返回 ErrEvalRunNotFound。
func (s *EvalRunStore) GetRun(ctx context.Context, runID int64) (model.EvalRun, error) {
	row := s.db.QueryRow(ctx, `SELECT `+evalRunColumns+` FROM eval_runs WHERE id = $1`, runID)
	run, err := scanEvalRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.EvalRun{}, ErrEvalRunNotFound
		}
		return model.EvalRun{}, err
	}
	return run, nil
}

// ListRuns 列出评估运行。datasetID > 0 时按数据集过滤。
func (s *EvalRunStore) ListRuns(ctx context.Context, datasetID int64) ([]model.EvalRun, error) {
	query := `SELECT ` + evalRunColumns + ` FROM eval_runs`
	args := []any{}
	if datasetID > 0 {
		query += ` WHERE dataset_id = $1`
		args = append(args, datasetID)
	}
	query += ` ORDER BY id DESC`

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := []model.EvalRun{}
	for rows.Next() {
		run, err := scanEvalRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// UpdateRunStatus 更新运行状态与进度。
//
// 空字符串的 errorSummary 不清空已有摘要（用 COALESCE 语义）：
// 进度回写很频繁，若每次把 errorSummary 传空就覆盖，之前记录的失败原因会丢。
func (s *EvalRunStore) UpdateRunStatus(ctx context.Context, runID int64, status string, totalItems, scoredItems int, errorSummary string) error {
	_, err := s.db.Exec(ctx, `
    UPDATE eval_runs
    SET status = $2,
        total_items = $3,
        scored_items = $4,
        error_summary = CASE WHEN $5 = '' THEN error_summary ELSE $5 END,
        updated_at = NOW()
    WHERE id = $1`,
		runID, status, totalItems, scoredItems, errorSummary)
	return err
}

// EvalItemInput 一条待写入的被评条目。
type EvalItemInput struct {
	QuestionID int64
	ItemIndex  int
	Payload    map[string]any
}

// InsertItems 批量写入被评条目。
//
// ON CONFLICT DO NOTHING 对应 UNIQUE(eval_run_id, question_id)：
// 同一运行重复写入同一问题时保留首次快照，不覆盖。
// 这样断点续跑（重放同一批抽样结果）是幂等的，不会把已有条目改掉。
//
// 返回实际新增的条数。
func (s *EvalRunStore) InsertItems(ctx context.Context, runID, datasetID int64, items []EvalItemInput) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, item := range items {
		payload := item.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		batch.Queue(`
    INSERT INTO eval_items (eval_run_id, dataset_id, question_id, item_index, payload)
    VALUES ($1, $2, $3, $4, $5)
    ON CONFLICT (eval_run_id, question_id) DO NOTHING`,
			runID, datasetID, item.QuestionID, item.ItemIndex, payload)
	}

	results := s.db.SendBatch(ctx, batch)
	defer func() {
		_ = results.Close()
	}()

	inserted := 0
	for range items {
		tag, err := results.Exec()
		if err != nil {
			return inserted, err
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// ListItems 列出运行的被评条目，带分页。
//
// limit <= 0 时用默认 100；上限 1000，避免一次拉爆内存。
func (s *EvalRunStore) ListItems(ctx context.Context, runID int64, limit, offset int) ([]model.EvalItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.Query(ctx, `
    SELECT id, eval_run_id, dataset_id, question_id, item_index, payload, created_at
    FROM eval_items
    WHERE eval_run_id = $1
    ORDER BY item_index ASC, id ASC
    LIMIT $2 OFFSET $3`, runID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.EvalItem{}
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

// EvalScoreInput 一条待写入的分数。
type EvalScoreInput struct {
	EvalItemID      int64
	JudgeProviderID int64
	DimensionKey    string
	Score           float64
	Rationale       string
	RawResponse     string
	Status          string
}

// UpsertScores 批量写入逐条分数。
//
// ON CONFLICT DO UPDATE 对应 UNIQUE(eval_item_id, judge_provider_id, dimension_key)：
// 同一 (条目, 裁判, 维度) 重复评分时覆盖旧值。断点续跑重放时，
// 失败记录被成功记录覆盖正是我们想要的。
func (s *EvalRunStore) UpsertScores(ctx context.Context, runID int64, scores []EvalScoreInput) (int, error) {
	if len(scores) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, score := range scores {
		status := score.Status
		if status == "" {
			status = "scored"
		}
		batch.Queue(`
    INSERT INTO eval_item_scores
      (eval_run_id, eval_item_id, judge_provider_id, dimension_key, score, rationale, raw_response, status)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    ON CONFLICT (eval_item_id, judge_provider_id, dimension_key) DO UPDATE SET
      score        = EXCLUDED.score,
      rationale    = EXCLUDED.rationale,
      raw_response = EXCLUDED.raw_response,
      status       = EXCLUDED.status`,
			runID, score.EvalItemID, score.JudgeProviderID, score.DimensionKey,
			score.Score, score.Rationale, score.RawResponse, status)
	}

	results := s.db.SendBatch(ctx, batch)
	defer func() {
		_ = results.Close()
	}()

	affected := 0
	for range scores {
		tag, err := results.Exec()
		if err != nil {
			return affected, err
		}
		affected += int(tag.RowsAffected())
	}
	return affected, nil
}

// ListScores 列出运行的逐条分数，可按裁判与维度过滤。
// judgeProviderID <= 0 与空 dimensionKey 表示不过滤。
func (s *EvalRunStore) ListScores(ctx context.Context, runID, judgeProviderID int64, dimensionKey string) ([]model.EvalItemScore, error) {
	query := `
    SELECT id, eval_run_id, eval_item_id, judge_provider_id, dimension_key,
           score, rationale, raw_response, status, created_at
    FROM eval_item_scores
    WHERE eval_run_id = $1`
	args := []any{runID}

	if judgeProviderID > 0 {
		args = append(args, judgeProviderID)
		query += fmt.Sprintf(` AND judge_provider_id = $%d`, len(args))
	}
	if dimensionKey != "" {
		args = append(args, dimensionKey)
		query += fmt.Sprintf(` AND dimension_key = $%d`, len(args))
	}
	query += ` ORDER BY eval_item_id ASC, judge_provider_id ASC, dimension_key ASC`

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := []model.EvalItemScore{}
	for rows.Next() {
		var score model.EvalItemScore
		if err := rows.Scan(&score.ID, &score.EvalRunID, &score.EvalItemID, &score.JudgeProviderID,
			&score.DimensionKey, &score.Score, &score.Rationale, &score.RawResponse,
			&score.Status, &score.CreatedAt); err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	return scores, rows.Err()
}

// EvalSourceItem 一条可被评估的数据源（问题 + 思维链 + 答案）。
type EvalSourceItem struct {
	QuestionID int64
	Question   string
	Reasoning  string
	Answer     string
	Difficulty string
	DomainID   int64
}

// LoadEvalSources 读取数据集里可被评估的数据。
//
// 回退链与 L12 的 LoadScanSources 一致：
//   - 思维链：优先 sft_records.chain_of_thought（L5 产出），回退 reasoning_records.reasoning
//   - 答案：  优先 sft_records.answer，回退 reasoning_records.answer_summary
//
// 这样 L5 尚未生成 SFT 数据时，评估仍可基于旧推理数据工作；
// 而一旦 SFT 数据就位，评估自动切到新语义的数据上。
//
// 只取 cleaning_status <> 'dropped' 的问题：已被清洗判定丢弃的数据
// 不应再占用评估资源，否则报告会掺入已知的脏数据。
func (s *EvalRunStore) LoadEvalSources(ctx context.Context, datasetID int64) ([]EvalSourceItem, error) {
	rows, err := s.db.Query(ctx, `
    SELECT q.id, q.content,
           COALESCE(s.chain_of_thought, ''), COALESCE(s.answer, ''),
           COALESCE(r.reasoning, ''), COALESCE(r.answer_summary, ''),
           q.difficulty, q.domain_id
    FROM questions q
    LEFT JOIN sft_records s ON s.dataset_id = q.dataset_id AND s.question_id = q.id
    LEFT JOIN reasoning_records r ON r.question_id = q.id
    WHERE q.dataset_id = $1 AND q.cleaning_status <> 'dropped'
    ORDER BY q.id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []EvalSourceItem{}
	for rows.Next() {
		var item EvalSourceItem
		var sftThought, sftAnswer, legacyThought, legacyAnswer string
		if err := rows.Scan(&item.QuestionID, &item.Question,
			&sftThought, &sftAnswer, &legacyThought, &legacyAnswer,
			&item.Difficulty, &item.DomainID); err != nil {
			return nil, err
		}
		item.Reasoning = firstNonEmpty(sftThought, legacyThought)
		item.Answer = firstNonEmpty(sftAnswer, legacyAnswer)
		items = append(items, item)
	}
	return items, rows.Err()
}

// DatasetName 取数据集名，用于评估报告的展示。
func (s *EvalRunStore) DatasetName(ctx context.Context, datasetID int64) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM datasets WHERE id = $1`, datasetID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}
