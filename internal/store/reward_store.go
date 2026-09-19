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
	return s.upsert(ctx, datasetID, records, true)
}

// UpsertPartial 只落记录、不推进数据集状态。
//
// 用途：调用方在整批尚未跑完时中途失败（例如对象存储写入失败），需要保住
// 已经拿到的记录，同时**不能**声称这一批已经完成。此时用本方法落库，再由
// 调用方把数据集标为 rewards_failed。
func (s *RewardStore) UpsertPartial(ctx context.Context, datasetID int64, records []model.RewardRecord) error {
	return s.upsert(ctx, datasetID, records, false)
}

// upsert 落库一批记录；markGenerated 为 true 时按**本批**统计推进数据集状态。
//
// 调用方必须传入**完整的一批**（整批题目），否则状态会按残缺的一批推断 ——
// 这正是 issue #5 的成因。
func (s *RewardStore) upsert(ctx context.Context, datasetID int64, records []model.RewardRecord, markGenerated bool) error {
	generatedCount := 0
	failedCount := 0
	unavailableCount := 0
	for _, record := range records {
		status := record.Status
		if status == "" {
			status = "generated"
		}
		switch status {
		case "generated":
			generatedCount++
		case "failed":
			failedCount++
		default:
			// invalid（issue #7 的占位内容）以及任何未识别状态都计入「不可用」。
			// 理由同 reasoning_store.go：默认计入 generated 会让状态领先于真正可用的记录数，
			// 那正是 issue #5 的成因；宁可保守停在 partial。
			unavailableCount++
		}
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
			status,
		)
		if err != nil {
			return err
		}
	}
	if !markGenerated {
		return nil
	}
	// 数据集级状态只表达「这一批跑得怎么样」；invalid 与 failed 在此同等看待，
	// 但记录级分开计数以便区分「网络抖动」与「模型摆烂」。
	nextStatus := "rewards_generated"
	if failedCount > 0 || unavailableCount > 0 {
		nextStatus = "rewards_partial"
		if generatedCount == 0 {
			nextStatus = "rewards_failed"
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE datasets SET status = $2, updated_at = NOW() WHERE id = $1`, datasetID, nextStatus)
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
