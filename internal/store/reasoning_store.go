package store

import (
  "context"

  "github.com/1420970597/llm/internal/model"
  "github.com/jackc/pgx/v5/pgxpool"
)

type ReasoningStore struct {
  db *pgxpool.Pool
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
  for _, record := range records {
    _, err := s.db.Exec(ctx, `
      INSERT INTO reasoning_records (dataset_id, question_id, answer_summary, object_key, status)
      VALUES ($1, $2, $3, $4, $5)
      ON CONFLICT (question_id) DO UPDATE SET
        answer_summary = EXCLUDED.answer_summary,
        object_key = EXCLUDED.object_key,
        status = EXCLUDED.status,
        updated_at = NOW()`,
      datasetID,
      record.QuestionID,
      record.AnswerSummary,
      record.ObjectKey,
      "generated",
    )
    if err != nil {
      return err
    }
  }
  if !markGenerated {
    return nil
  }
  _, err := s.db.Exec(ctx, `UPDATE datasets SET status = 'reasoning_generated', updated_at = NOW() WHERE id = $1`, datasetID)
  return err
}

func (s *ReasoningStore) List(ctx context.Context, datasetID int64) ([]model.ReasoningRecord, error) {
  rows, err := s.db.Query(ctx, `
    SELECT r.id, r.dataset_id, r.question_id, q.content, r.answer_summary, r.object_key, r.status, r.created_at, r.updated_at
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
    if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.QuestionText, &item.AnswerSummary, &item.ObjectKey, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
      return nil, err
    }
    items = append(items, item)
  }
  return items, rows.Err()
}
