package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CleaningRunStore 管理清洗运行与命中明细，是 L12「分步清洗 + 报告」的存储层。
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.12 节；表见 0012_cleaning_core.sql。
type CleaningRunStore struct {
	db *pgxpool.Pool
}

func NewCleaningRunStore(db *pgxpool.Pool) *CleaningRunStore {
	return &CleaningRunStore{db: db}
}

const cleaningRunColumns = `id, dataset_id, stages, status, scanned_items, flagged_items,
	dropped_items, report, error_summary, created_at, updated_at`

func scanCleaningRun(row pgx.Row) (model.CleaningRun, error) {
	var item model.CleaningRun
	var stagesPayload, reportPayload []byte
	err := row.Scan(&item.ID, &item.DatasetID, &stagesPayload, &item.Status, &item.ScannedItems,
		&item.FlaggedItems, &item.DroppedItems, &reportPayload, &item.ErrorSummary,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.CleaningRun{}, err
	}
	if len(stagesPayload) > 0 {
		_ = json.Unmarshal(stagesPayload, &item.Stages)
	}
	if item.Stages == nil {
		item.Stages = []string{}
	}
	if len(reportPayload) > 0 {
		_ = json.Unmarshal(reportPayload, &item.Report)
	}
	if item.Report == nil {
		item.Report = map[string]any{}
	}
	return item, nil
}

// Create 新建一次清洗运行，状态为 queued。
func (s *CleaningRunStore) Create(ctx context.Context, datasetID int64, stages []string) (model.CleaningRun, error) {
	if len(stages) == 0 {
		stages = []string{"question", "reasoning", "answer"}
	}
	payload, err := json.Marshal(stages)
	if err != nil {
		return model.CleaningRun{}, err
	}
	return scanCleaningRun(s.db.QueryRow(ctx, `
    INSERT INTO cleaning_runs (dataset_id, stages, status)
    VALUES ($1, $2, 'queued')
    RETURNING `+cleaningRunColumns, datasetID, payload))
}

// GetRun 按 ID 查询运行记录。
func (s *CleaningRunStore) GetRun(ctx context.Context, runID int64) (model.CleaningRun, error) {
	return scanCleaningRun(s.db.QueryRow(ctx, `SELECT `+cleaningRunColumns+` FROM cleaning_runs WHERE id = $1`, runID))
}

// ListByDataset 列出数据集下全部清洗运行，前端据此展示历史。
func (s *CleaningRunStore) ListByDataset(ctx context.Context, datasetID int64) ([]model.CleaningRun, error) {
	rows, err := s.db.Query(ctx, `
    SELECT `+cleaningRunColumns+` FROM cleaning_runs
    WHERE dataset_id = $1 ORDER BY id DESC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.CleaningRun{}
	for rows.Next() {
		item, err := scanCleaningRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateProgress 把运行标记为 running 并写入当前累计进度，供前端轮询。
func (s *CleaningRunStore) UpdateProgress(ctx context.Context, runID int64, scanned, flagged, dropped int) error {
	_, err := s.db.Exec(ctx, `
    UPDATE cleaning_runs
    SET status = 'running', scanned_items = $2, flagged_items = $3, dropped_items = $4, updated_at = NOW()
    WHERE id = $1`, runID, scanned, flagged, dropped)
	return err
}

// MarkDone 结束运行并持久化清洗报告。
func (s *CleaningRunStore) MarkDone(ctx context.Context, runID int64, scanned, flagged, dropped int, report model.CleaningReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
    UPDATE cleaning_runs
    SET status = 'completed', scanned_items = $2, flagged_items = $3, dropped_items = $4,
        report = $5, updated_at = NOW()
    WHERE id = $1`, runID, scanned, flagged, dropped, payload)
	return err
}

// MarkFailed 记录失败原因，保留已扫出的进度便于排查。
func (s *CleaningRunStore) MarkFailed(ctx context.Context, runID int64, errorSummary string) error {
	_, err := s.db.Exec(ctx, `
    UPDATE cleaning_runs SET status = 'failed', error_summary = $2, updated_at = NOW()
    WHERE id = $1`, runID, errorSummary)
	return err
}

// InsertFindings 批量写入命中明细，每条命中一行。
func (s *CleaningRunStore) InsertFindings(ctx context.Context, runID, datasetID int64, findings []model.CleaningFinding) error {
	if len(findings) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, finding := range findings {
		batch.Queue(`
      INSERT INTO cleaning_findings (cleaning_run_id, dataset_id, question_id, stage, keyword_id, matched_text, snippet, action)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			runID, datasetID, finding.QuestionID, finding.Stage, finding.KeywordID,
			finding.MatchedText, finding.Snippet, finding.Action)
	}
	results := s.db.SendBatch(ctx, batch)
	defer results.Close()
	for range findings {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// ListFindings 查询某次运行的命中明细，stage 为空表示不过滤。
func (s *CleaningRunStore) ListFindings(ctx context.Context, runID int64, stage string, limit int) ([]model.CleaningFinding, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, cleaning_run_id, dataset_id, question_id, stage, keyword_id, matched_text, snippet, action, created_at
    FROM cleaning_findings
    WHERE cleaning_run_id = $1 AND ($2 = '' OR stage = $2)
    ORDER BY id ASC LIMIT $3`, runID, stage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.CleaningFinding{}
	for rows.Next() {
		var item model.CleaningFinding
		if err := rows.Scan(&item.ID, &item.CleaningRunID, &item.DatasetID, &item.QuestionID, &item.Stage,
			&item.KeywordID, &item.MatchedText, &item.Snippet, &item.Action, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ScanSources 一次待扫描的内容集合：问题正文、思维链、答案。
type ScanSources struct {
	Questions []model.Question
	// Reasoning 按 question_id 索引的思维链正文。
	Reasoning map[int64]string
	// Answers 按 question_id 索引的答案正文。
	Answers map[int64]string
}

// LoadScanSources 读取三阶段扫描数据源。
//
// 回退链（L5 与本 lane 并行开发，sft_records 可能还没有数据）：
//   - question  取 questions.content
//   - reasoning 优先取 sft_records.chain_of_thought，缺失时回退 reasoning_records.reasoning
//   - answer    优先取 sft_records.answer，缺失时回退 reasoning_records.answer_summary
func (s *CleaningRunStore) LoadScanSources(ctx context.Context, datasetID int64) (ScanSources, error) {
	sources := ScanSources{Reasoning: map[int64]string{}, Answers: map[int64]string{}}

	rows, err := s.db.Query(ctx, `
    SELECT q.id, q.dataset_id, q.domain_id, q.direction_domain_id, q.content, q.canonical_hash,
           q.dedupe_key, q.difficulty, q.difficulty_score, q.source, q.cleaning_status,
           q.status, q.created_at, q.updated_at,
           COALESCE(s.chain_of_thought, ''), COALESCE(s.answer, ''),
           COALESCE(r.reasoning, ''), COALESCE(r.answer_summary, '')
    FROM questions q
    LEFT JOIN sft_records s ON s.dataset_id = q.dataset_id AND s.question_id = q.id
    LEFT JOIN reasoning_records r ON r.question_id = q.id
    WHERE q.dataset_id = $1 AND q.cleaning_status <> 'dropped'
    ORDER BY q.id ASC`, datasetID)
	if err != nil {
		return ScanSources{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var question model.Question
		var sftThought, sftAnswer, legacyReasoning, legacyAnswer string
		if err := rows.Scan(&question.ID, &question.DatasetID, &question.DomainID, &question.DirectionDomainID,
			&question.Content, &question.CanonicalHash, &question.DedupeKey, &question.Difficulty,
			&question.DifficultyScore, &question.Source, &question.CleaningStatus, &question.Status,
			&question.CreatedAt, &question.UpdatedAt,
			&sftThought, &sftAnswer, &legacyReasoning, &legacyAnswer); err != nil {
			return ScanSources{}, err
		}
		sources.Questions = append(sources.Questions, question)
		if thought := firstNonEmpty(sftThought, legacyReasoning); thought != "" {
			sources.Reasoning[question.ID] = thought
		}
		if answer := firstNonEmpty(sftAnswer, legacyAnswer); answer != "" {
			sources.Answers[question.ID] = answer
		}
	}
	if err := rows.Err(); err != nil {
		return ScanSources{}, err
	}
	return sources, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ApplyCleaningStatus 把扫描结论写回 questions.cleaning_status（drop / flagged）。
// 只更新非 clean 的问题，避免无谓写入。
func (s *CleaningRunStore) ApplyCleaningStatus(ctx context.Context, datasetID int64, updates map[int64]string) (int, error) {
	if len(updates) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	ids := make([]int64, 0, len(updates))
	for questionID, status := range updates {
		if status != "drop" && status != "flag" {
			continue
		}
		ids = append(ids, questionID)
		batch.Queue(`
      UPDATE questions SET cleaning_status = $3, updated_at = NOW()
      WHERE dataset_id = $1 AND id = $2`, datasetID, questionID, status)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	results := s.db.SendBatch(ctx, batch)
	defer results.Close()
	updated := 0
	for range ids {
		tag, err := results.Exec()
		if err != nil {
			return updated, err
		}
		updated += int(tag.RowsAffected())
	}
	return updated, nil
}

// GetReport 读取运行记录里持久化的清洗报告。
// 未完成时 report 列为空对象，返回零值报告（由调用方决定如何展示）。
func (s *CleaningRunStore) GetReport(ctx context.Context, runID int64) (model.CleaningReport, error) {
	var payload []byte
	if err := s.db.QueryRow(ctx, `SELECT report FROM cleaning_runs WHERE id = $1`, runID).Scan(&payload); err != nil {
		return model.CleaningReport{}, err
	}
	report := model.CleaningReport{}
	if len(payload) > 0 {
		// 空对象 '{}' 反序列化后各字段为零值，符合「尚未完成」语义。
		if err := json.Unmarshal(payload, &report); err != nil {
			return model.CleaningReport{}, err
		}
	}
	return report, nil
}

// ActiveRun 查询数据集下未结束的清洗运行，用于避免重复入队。
func (s *CleaningRunStore) ActiveRun(ctx context.Context, datasetID int64) (model.CleaningRun, error) {
	return scanCleaningRun(s.db.QueryRow(ctx, `
    SELECT `+cleaningRunColumns+` FROM cleaning_runs
    WHERE dataset_id = $1 AND status IN ('queued', 'running')
    ORDER BY id DESC LIMIT 1`, datasetID))
}

// IsCleaningRunNotFound 判断错误是否为「清洗运行不存在」。
func IsCleaningRunNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
