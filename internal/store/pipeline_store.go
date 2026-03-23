package store

import (
  "context"
  "crypto/sha256"
  "encoding/hex"
  "strings"

  "github.com/1420970597/llm/internal/model"
  "github.com/jackc/pgx/v5/pgxpool"
)

type PipelineStore struct {
  db *pgxpool.Pool
}

func NewPipelineStore(db *pgxpool.Pool) *PipelineStore {
  return &PipelineStore{db: db}
}

func (s *PipelineStore) InsertQuestions(ctx context.Context, datasetID int64, questions []model.Question) error {
  for _, question := range questions {
    _, err := s.db.Exec(ctx, `
      INSERT INTO questions (dataset_id, domain_id, content, canonical_hash, status)
      VALUES ($1, $2, $3, $4, $5)
      ON CONFLICT (dataset_id, canonical_hash) DO NOTHING`,
      datasetID,
      question.DomainID,
      question.Content,
      question.CanonicalHash,
      "generated",
    )
    if err != nil {
      return err
    }
  }
  _, err := s.db.Exec(ctx, `UPDATE datasets SET status = 'questions_generated', updated_at = NOW() WHERE id = $1`, datasetID)
  return err
}

func (s *PipelineStore) ListQuestions(ctx context.Context, datasetID int64) ([]model.Question, error) {
  rows, err := s.db.Query(ctx, `
    SELECT q.id, q.dataset_id, q.domain_id, d.name, q.content, q.canonical_hash, q.status, q.created_at, q.updated_at
    FROM questions q
    JOIN domains d ON d.id = q.domain_id
    WHERE q.dataset_id = $1
    ORDER BY q.id ASC`, datasetID)
  if err != nil {
    return nil, err
  }
  defer rows.Close()

  items := []model.Question{}
  for rows.Next() {
    var item model.Question
    if err := rows.Scan(&item.ID, &item.DatasetID, &item.DomainID, &item.DomainName, &item.Content, &item.CanonicalHash, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
      return nil, err
    }
    items = append(items, item)
  }
  return items, rows.Err()
}

func CanonicalHash(input string) string {
  normalized := strings.ToLower(strings.TrimSpace(input))
  normalized = strings.Join(strings.Fields(normalized), " ")
  digest := sha256.Sum256([]byte(normalized))
  return hex.EncodeToString(digest[:])
}
