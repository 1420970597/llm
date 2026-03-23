package store

import (
  "context"

  "github.com/1420970597/llm/internal/model"
  "github.com/jackc/pgx/v5/pgxpool"
)

type RewardStore struct {
  db *pgxpool.Pool
}

func NewRewardStore(db *pgxpool.Pool) *RewardStore {
  return &RewardStore{db: db}
}

func (s *RewardStore) Insert(ctx context.Context, datasetID int64, records []model.RewardRecord) error {
  for _, record := range records {
    _, err := s.db.Exec(ctx, `
      INSERT INTO reward_records (dataset_id, question_id, score, object_key, status)
      VALUES ($1, $2, $3, $4, $5)
      ON CONFLICT (question_id) DO UPDATE SET
        score = EXCLUDED.score,
        object_key = EXCLUDED.object_key,
        status = EXCLUDED.status,
        updated_at = NOW()`,
      datasetID,
      record.QuestionID,
      record.Score,
      record.ObjectKey,
      "generated",
    )
    if err != nil {
      return err
    }
  }
  _, err := s.db.Exec(ctx, `UPDATE datasets SET status = 'rewards_generated', updated_at = NOW() WHERE id = $1`, datasetID)
  return err
}

func (s *RewardStore) List(ctx context.Context, datasetID int64) ([]model.RewardRecord, error) {
  rows, err := s.db.Query(ctx, `
    SELECT r.id, r.dataset_id, r.question_id, q.content, r.score, r.object_key, r.status, r.created_at, r.updated_at
    FROM reward_records r
    JOIN questions q ON q.id = r.question_id
    WHERE r.dataset_id = $1
    ORDER BY r.id ASC`, datasetID)
  if err != nil {
    return nil, err
  }
  defer rows.Close()

  items := []model.RewardRecord{}
  for rows.Next() {
    var item model.RewardRecord
    if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.QuestionText, &item.Score, &item.ObjectKey, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
      return nil, err
    }
    items = append(items, item)
  }
  return items, rows.Err()
}
