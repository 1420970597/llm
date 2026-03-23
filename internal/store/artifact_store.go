package store

import (
  "context"
  "fmt"

  "github.com/1420970597/llm/internal/model"
  "github.com/jackc/pgx/v5/pgxpool"
  "github.com/redis/go-redis/v9"
)

type ArtifactStore struct {
  db    *pgxpool.Pool
  redis *redis.Client
  queue string
}

func NewArtifactStore(db *pgxpool.Pool, redisClient *redis.Client, queue string) *ArtifactStore {
  return &ArtifactStore{db: db, redis: redisClient, queue: queue}
}

func (s *ArtifactStore) Insert(ctx context.Context, artifact model.Artifact) (model.Artifact, error) {
  var item model.Artifact
  err := s.db.QueryRow(ctx, `
    INSERT INTO artifacts (dataset_id, artifact_type, object_key, content_type)
    VALUES ($1, $2, $3, $4)
    RETURNING id, dataset_id, artifact_type, object_key, content_type, created_at`,
    artifact.DatasetID,
    artifact.ArtifactType,
    artifact.ObjectKey,
    artifact.ContentType,
  ).Scan(&item.ID, &item.DatasetID, &item.ArtifactType, &item.ObjectKey, &item.ContentType, &item.CreatedAt)
  return item, err
}

func (s *ArtifactStore) List(ctx context.Context, datasetID int64) ([]model.Artifact, error) {
  rows, err := s.db.Query(ctx, `
    SELECT id, dataset_id, artifact_type, object_key, content_type, created_at
    FROM artifacts WHERE dataset_id = $1 ORDER BY id ASC`, datasetID)
  if err != nil {
    return nil, err
  }
  defer rows.Close()

  items := []model.Artifact{}
  for rows.Next() {
    var item model.Artifact
    if err := rows.Scan(&item.ID, &item.DatasetID, &item.ArtifactType, &item.ObjectKey, &item.ContentType, &item.CreatedAt); err != nil {
      return nil, err
    }
    items = append(items, item)
  }
  return items, rows.Err()
}

func (s *ArtifactStore) Runtime(ctx context.Context) (model.RuntimeStatus, error) {
  counts := []struct {
    query string
    dest  *int
  }{}
  status := model.RuntimeStatus{}
  counts = append(counts,
    struct{ query string; dest *int }{`SELECT COUNT(*) FROM datasets`, &status.DatasetCount},
    struct{ query string; dest *int }{`SELECT COUNT(*) FROM questions`, &status.QuestionCount},
    struct{ query string; dest *int }{`SELECT COUNT(*) FROM reasoning_records`, &status.ReasoningCount},
    struct{ query string; dest *int }{`SELECT COUNT(*) FROM reward_records`, &status.RewardCount},
    struct{ query string; dest *int }{`SELECT COUNT(*) FROM artifacts`, &status.ArtifactCount},
  )
  for _, item := range counts {
    if err := s.db.QueryRow(ctx, item.query).Scan(item.dest); err != nil {
      return model.RuntimeStatus{}, fmt.Errorf("runtime query failed: %w", err)
    }
  }
  if s.redis != nil {
    depth, err := s.redis.LLen(ctx, s.queue).Result()
    if err == nil {
      status.QueueDepth = int(depth)
    }
  }
  return status, nil
}
