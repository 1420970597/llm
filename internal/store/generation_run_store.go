package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GenerationRunStore 管理生成阶段的运行记录，是断点续跑的基础设施。
// 属于冻结契约（docs/plans/eval-and-cleaning-plan.md 第 2 节 0016）。
type GenerationRunStore struct {
	db *pgxpool.Pool
}

func NewGenerationRunStore(db *pgxpool.Pool) *GenerationRunStore {
	return &GenerationRunStore{db: db}
}

const generationRunColumns = `id, dataset_id, stage, status, cursor, total_units, done_units,
	attempts, error_summary, started_at, finished_at, created_at, updated_at`

func scanGenerationRun(row pgx.Row) (model.GenerationRun, error) {
	var item model.GenerationRun
	var cursorPayload []byte
	err := row.Scan(&item.ID, &item.DatasetID, &item.Stage, &item.Status, &cursorPayload,
		&item.TotalUnits, &item.DoneUnits, &item.Attempts, &item.ErrorSummary,
		&item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.GenerationRun{}, err
	}
	if len(cursorPayload) > 0 {
		_ = json.Unmarshal(cursorPayload, &item.Cursor)
	}
	if item.Cursor == nil {
		item.Cursor = map[string]any{}
	}
	return item, nil
}

// StartRun 开启（或复用）一个阶段的运行记录。已存在未完成记录时返回该记录，实现断点续跑。
func (s *GenerationRunStore) StartRun(ctx context.Context, datasetID int64, stage string, totalUnits int) (model.GenerationRun, error) {
	existing, err := s.ActiveRun(ctx, datasetID, stage)
	if err == nil {
		if _, updateErr := s.db.Exec(ctx, `
      UPDATE generation_runs SET status = 'running', attempts = attempts + 1, updated_at = NOW()
      WHERE id = $1`, existing.ID); updateErr != nil {
			return model.GenerationRun{}, updateErr
		}
		return s.GetRun(ctx, existing.ID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.GenerationRun{}, err
	}

	now := time.Now()
	return scanGenerationRun(s.db.QueryRow(ctx, `
    INSERT INTO generation_runs (dataset_id, stage, status, cursor, total_units, done_units, attempts, started_at, updated_at)
    VALUES ($1, $2, 'running', '{}'::jsonb, $3, 0, 1, $4, NOW())
    RETURNING `+generationRunColumns, datasetID, stage, totalUnits, now))
}

// ActiveRun 查询某阶段未结束的运行记录。
func (s *GenerationRunStore) ActiveRun(ctx context.Context, datasetID int64, stage string) (model.GenerationRun, error) {
	return scanGenerationRun(s.db.QueryRow(ctx, `
    SELECT `+generationRunColumns+` FROM generation_runs
    WHERE dataset_id = $1 AND stage = $2 AND status IN ('pending', 'running')
    ORDER BY id DESC LIMIT 1`, datasetID, stage))
}

// GetRun 按 ID 查询运行记录。
func (s *GenerationRunStore) GetRun(ctx context.Context, id int64) (model.GenerationRun, error) {
	return scanGenerationRun(s.db.QueryRow(ctx, `SELECT `+generationRunColumns+` FROM generation_runs WHERE id = $1`, id))
}

// ListRuns 列出数据集下全部运行记录，前端据此展示阶段进度。
func (s *GenerationRunStore) ListRuns(ctx context.Context, datasetID int64) ([]model.GenerationRun, error) {
	rows, err := s.db.Query(ctx, `
    SELECT `+generationRunColumns+` FROM generation_runs
    WHERE dataset_id = $1 ORDER BY id DESC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.GenerationRun{}
	for rows.Next() {
		item, err := scanGenerationRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SaveCursor 持久化进度游标，供断点续跑使用。
func (s *GenerationRunStore) SaveCursor(ctx context.Context, id int64, cursor map[string]any, doneUnits, totalUnits int) error {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
    UPDATE generation_runs
    SET cursor = $2, done_units = $3, total_units = $4, updated_at = NOW()
    WHERE id = $1`, id, payload, doneUnits, totalUnits)
	return err
}

// FinishRun 结束运行记录。status: completed / failed / partial_failed。
func (s *GenerationRunStore) FinishRun(ctx context.Context, id int64, status, errorSummary string) error {
	_, err := s.db.Exec(ctx, `
    UPDATE generation_runs
    SET status = $2, error_summary = $3, finished_at = NOW(), updated_at = NOW()
    WHERE id = $1`, id, status, errorSummary)
	return err
}

// ResumeTarget 返回某阶段断点续跑应使用的运行记录；不存在则返回 pgx.ErrNoRows。
func (s *GenerationRunStore) ResumeTarget(ctx context.Context, datasetID int64, stage string) (model.GenerationRun, error) {
	run, err := s.ActiveRun(ctx, datasetID, stage)
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.GenerationRun{}, err
	}
	return scanGenerationRun(s.db.QueryRow(ctx, `
    SELECT `+generationRunColumns+` FROM generation_runs
    WHERE dataset_id = $1 AND stage = $2 AND status IN ('failed', 'partial_failed')
    ORDER BY id DESC LIMIT 1`, datasetID, stage))
}
