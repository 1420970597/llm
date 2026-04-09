package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReasoningStore struct {
	db             *pgxpool.Pool
	schemaMu       sync.Mutex
	schemaReady    bool
	schemaCheckErr error
}

func NewReasoningStore(db *pgxpool.Pool) *ReasoningStore {
	return &ReasoningStore{db: db}
}

func (s *ReasoningStore) Insert(ctx context.Context, datasetID int64, records []model.ReasoningRecord) error {
	return s.upsert(ctx, datasetID, records, true)
}

func (s *ReasoningStore) UpsertPartial(ctx context.Context, datasetID int64, records []model.ReasoningRecord) error {
	return s.upsert(ctx, datasetID, records, false)
}

func (s *ReasoningStore) upsert(ctx context.Context, datasetID int64, records []model.ReasoningRecord, markGenerated bool) error {
	if err := s.EnsureSchemaReady(ctx); err != nil {
		return err
	}
	generatedCount := 0
	partialCount := 0
	failedCount := 0
	for _, record := range records {
		status := record.Status
		if status == "" {
			status = "generated"
		}
		switch status {
		case "failed":
			failedCount++
		case "partial":
			partialCount++
		default:
			generatedCount++
		}
		_, err := s.db.Exec(ctx, `
      INSERT INTO reasoning_records (dataset_id, question_id, answer_summary, reasoning, object_key, status)
      VALUES ($1, $2, $3, $4, $5, $6)
      ON CONFLICT (question_id) DO UPDATE SET
        answer_summary = EXCLUDED.answer_summary,
        reasoning = EXCLUDED.reasoning,
        object_key = EXCLUDED.object_key,
        status = EXCLUDED.status,
        updated_at = NOW()`,
			datasetID,
			record.QuestionID,
			record.AnswerSummary,
			record.Reasoning,
			record.ObjectKey,
			status,
		)
		if err != nil {
			return err
		}
	}
	if !markGenerated {
		return nil
	}
	nextStatus := "reasoning_generated"
	if failedCount > 0 || partialCount > 0 {
		nextStatus = "reasoning_partial"
		if generatedCount == 0 && partialCount == 0 {
			nextStatus = "reasoning_failed"
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE datasets SET status = $2, updated_at = NOW() WHERE id = $1`, datasetID, nextStatus)
	return err
}

func (s *ReasoningStore) List(ctx context.Context, datasetID int64) ([]model.ReasoningRecord, error) {
	if err := s.EnsureSchemaReady(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
    SELECT r.id, r.dataset_id, r.question_id, q.content, r.answer_summary, r.reasoning, r.object_key, r.status, r.created_at, r.updated_at
    FROM reasoning_records r
    JOIN questions q ON q.id = r.question_id
    WHERE r.dataset_id = $1
    ORDER BY r.id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ReasoningRecord{}
	for rows.Next() {
		var item model.ReasoningRecord
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.QuestionText, &item.AnswerSummary, &item.Reasoning, &item.ObjectKey, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ReasoningStore) EnsureSchemaReady(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var tableExists bool
	var reasoningColumnExists bool
	err := s.db.QueryRow(checkCtx, `
      SELECT
        EXISTS(
          SELECT 1
          FROM information_schema.tables
          WHERE table_schema = 'public' AND table_name = 'reasoning_records'
        ),
        EXISTS(
          SELECT 1
          FROM information_schema.columns
          WHERE table_schema = 'public' AND table_name = 'reasoning_records' AND column_name = 'reasoning'
        )`).Scan(&tableExists, &reasoningColumnExists)
	if err != nil {
		s.schemaCheckErr = fmt.Errorf("verify reasoning schema readiness: %w", err)
		return s.schemaCheckErr
	}
	if !tableExists {
		s.schemaCheckErr = fmt.Errorf("reasoning schema not ready: missing table reasoning_records; run migrations")
		return s.schemaCheckErr
	}
	if !reasoningColumnExists {
		s.schemaCheckErr = fmt.Errorf("reasoning schema not ready: missing column reasoning_records.reasoning; run migration 0010_reasoning_detail.sql")
		return s.schemaCheckErr
	}
	s.schemaReady = true
	s.schemaCheckErr = nil
	return nil
}
