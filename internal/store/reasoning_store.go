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

// UpsertPartial 只落记录、不推进数据集状态。
//
// 用途：调用方在整批尚未跑完时中途失败（例如对象存储写入失败），需要保住
// 已经拿到的记录，同时**不能**声称这一批已经完成。此时用本方法落库，再由
// 调用方把数据集标为 reasoning_failed。
func (s *ReasoningStore) UpsertPartial(ctx context.Context, datasetID int64, records []model.ReasoningRecord) error {
	return s.upsert(ctx, datasetID, records, false)
}

// upsert 落库一批记录；markGenerated 为 true 时按**本批**统计推进数据集状态。
//
// 调用方必须传入**完整的一批**（整批题目），否则状态会按残缺的一批推断 ——
// 这正是 issue #5 的成因：调用点在逐题循环内，每转一圈都用「当次迭代的那一条」
// 推算整批终态，第一条写完就把状态写死了。
func (s *ReasoningStore) upsert(ctx context.Context, datasetID int64, records []model.ReasoningRecord, markGenerated bool) error {
	if err := s.EnsureSchemaReady(ctx); err != nil {
		return err
	}
	generatedCount := 0
	partialCount := 0
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
		case "partial":
			partialCount++
		case "failed":
			failedCount++
		default:
			// invalid（issue #7 的占位内容）以及任何未识别状态都计入「不可用」，
			// 而不是默认计入 generated。
			//
			// 为什么默认不是 generated：那正是 issue #5 的成因 —— 让数据集状态领先于
			// 真正可用的记录数。反过来（未知状态按不可用计）是**保守**的失败方向：
			// 宁可让数据集停在 partial 等人工复核，也不要声称一批占位数据已完成。
			//
			// 这里用字面量而不是引用 internal/llm 的 ContentStatusInvalid：
			// internal/llm 依赖 internal/store，反向引用会构成导入环。
			unavailableCount++
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
	// 数据集级状态只表达「这一批跑得怎么样」，不表达单个记录的原因；
	// invalid 与 failed 在此同等看待，但记录级分开计数以便区分「网络抖动」与「模型摆烂」。
	nextStatus := "reasoning_generated"
	if failedCount > 0 || partialCount > 0 || unavailableCount > 0 {
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
