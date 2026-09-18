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
      INSERT INTO questions (dataset_id, domain_id, direction_domain_id, content, canonical_hash,
                             dedupe_key, difficulty, difficulty_score, source, status)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
      ON CONFLICT (dataset_id, canonical_hash) DO NOTHING`,
			datasetID,
			question.DomainID,
			question.DirectionDomainID,
			question.Content,
			question.CanonicalHash,
			question.DedupeKey,
			question.Difficulty,
			question.DifficultyScore,
			question.Source,
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
    SELECT q.id, q.dataset_id, q.domain_id, d.name, q.direction_domain_id, q.content, q.canonical_hash,
           q.dedupe_key, q.difficulty, q.difficulty_score, q.source, q.cleaning_status,
           q.status, q.created_at, q.updated_at
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
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.DomainID, &item.DomainName, &item.DirectionDomainID,
			&item.Content, &item.CanonicalHash, &item.DedupeKey, &item.Difficulty, &item.DifficultyScore,
			&item.Source, &item.CleaningStatus, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DifficultyStats 统计数据集内问题的难度分布。
func (s *PipelineStore) DifficultyStats(ctx context.Context, datasetID int64) (model.DifficultyStats, error) {
	rows, err := s.db.Query(ctx, `SELECT difficulty, COUNT(*) FROM questions WHERE dataset_id = $1 GROUP BY difficulty`, datasetID)
	if err != nil {
		return model.DifficultyStats{}, err
	}
	defer rows.Close()

	stats := model.DifficultyStats{Levels: map[string]int{}}
	for rows.Next() {
		var level string
		var count int
		if err := rows.Scan(&level, &count); err != nil {
			return model.DifficultyStats{}, err
		}
		stats.Levels[level] = count
		stats.Total += count
	}
	return stats, rows.Err()
}

func CanonicalHash(input string) string {
	normalized := strings.ToLower(strings.TrimSpace(input))
	normalized = strings.Join(strings.Fields(normalized), " ")
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:])
}
