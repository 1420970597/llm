package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SftStore 读写 SFT 训练样本（问题 → 思维链 → 答案）。
//
// 落点是 sft_records（迁移 0018），刻意不复用 reasoning_records：
// 后者是「推理生成」阶段的旧语义（answer_summary + MinIO 对象键），
// 与 SFT 的结构化样本语义不同，混用会让 SFT 样本被后续推理生成覆盖。
type SftStore struct {
	db *pgxpool.Pool
}

func NewSftStore(db *pgxpool.Pool) *SftStore {
	return &SftStore{db: db}
}

// SftQuestionContext 生成一条 SFT 样本所需的问题上下文。
type SftQuestionContext struct {
	QuestionID    int64
	QuestionText  string
	DirectionID   int64
	DirectionName string
	ChainSteps    []model.ChainStep
}

// ListQuestionContexts 取出数据集内全部问题，并带上所属方向与其长链思维标准步骤。
//
// 方向 id 取 COALESCE(NULLIF(direction_domain_id, 0), domain_id)：
// L3 会把问题挂在方向（level=2）上并写 direction_domain_id，
// 而 L3 之前生成的历史问题只写 domain_id。两种都要能取到。
//
// 长链标准步骤从 chain_standards / chain_standard_versions 读取当前版本；
// 若该方向尚无标准步骤，返回空切片（生成器会据此在提示词里要求模型自行拆解）。
func (s *SftStore) ListQuestionContexts(ctx context.Context, datasetID int64) ([]SftQuestionContext, error) {
	rows, err := s.db.Query(ctx, `
    SELECT q.id,
           q.content,
           COALESCE(NULLIF(q.direction_domain_id, 0), q.domain_id),
           COALESCE(d.name, ''),
           COALESCE(v.steps, '[]'::jsonb)
    FROM questions q
    LEFT JOIN domains d
           ON d.id = COALESCE(NULLIF(q.direction_domain_id, 0), q.domain_id)
    LEFT JOIN chain_standards c
           ON c.dataset_id = q.dataset_id
          AND c.domain_id = COALESCE(NULLIF(q.direction_domain_id, 0), q.domain_id)
    LEFT JOIN chain_standard_versions v
           ON v.standard_id = c.id
          AND v.version = c.current_version
    WHERE q.dataset_id = $1
    ORDER BY q.id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []SftQuestionContext{}
	for rows.Next() {
		var item SftQuestionContext
		var stepsPayload []byte
		if err := rows.Scan(&item.QuestionID, &item.QuestionText, &item.DirectionID, &item.DirectionName, &stepsPayload); err != nil {
			return nil, err
		}
		if len(stepsPayload) > 0 {
			_ = json.Unmarshal(stepsPayload, &item.ChainSteps)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpsertRecords 幂等写入一个数据集的 SFT 样本。
// 同一 (dataset_id, question_id) 重复生成时覆盖旧结果，与 sft_records 的 UNIQUE 约束一致。
func (s *SftStore) UpsertRecords(ctx context.Context, datasetID int64, records []model.SftRecord) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, record := range records {
		stepsPayload, err := json.Marshal(record.ChainSteps)
		if err != nil {
			return err
		}
		status := record.Status
		if status == "" {
			status = "generated"
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO sft_records (dataset_id, question_id, domain_id, chain_of_thought,
                               answer, chain_steps, status)
      VALUES ($1, $2, $3, $4, $5, $6, $7)
      ON CONFLICT (dataset_id, question_id) DO UPDATE SET
        domain_id = EXCLUDED.domain_id,
        chain_of_thought = EXCLUDED.chain_of_thought,
        answer = EXCLUDED.answer,
        chain_steps = EXCLUDED.chain_steps,
        status = EXCLUDED.status,
        updated_at = NOW()`,
			datasetID,
			record.QuestionID,
			record.DomainID,
			record.ChainOfThought,
			record.Answer,
			stepsPayload,
			status,
		); err != nil {
			return fmt.Errorf("upsert sft record question_id=%d: %w", record.QuestionID, err)
		}
	}

	return tx.Commit(ctx)
}

// ListRecords 按数据集读取全部 SFT 样本，并补齐问题文本与方向名。
func (s *SftStore) ListRecords(ctx context.Context, datasetID int64) ([]model.SftRecord, error) {
	rows, err := s.db.Query(ctx, `
    SELECT r.id, r.dataset_id, r.question_id, COALESCE(q.content, ''),
           r.domain_id, COALESCE(d.name, ''),
           r.chain_of_thought, r.answer, r.chain_steps, r.status,
           r.created_at, r.updated_at
    FROM sft_records r
    LEFT JOIN questions q ON q.id = r.question_id
    LEFT JOIN domains d ON d.id = r.domain_id
    WHERE r.dataset_id = $1
    ORDER BY r.question_id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.SftRecord{}
	for rows.Next() {
		var item model.SftRecord
		var stepsPayload []byte
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.QuestionText,
			&item.DomainID, &item.DomainName,
			&item.ChainOfThought, &item.Answer, &stepsPayload, &item.Status,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if len(stepsPayload) > 0 {
			_ = json.Unmarshal(stepsPayload, &item.ChainSteps)
		}
		if item.ChainSteps == nil {
			item.ChainSteps = []model.ChainStep{}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CountRecords 返回该数据集已生成的 SFT 样本数量。
func (s *SftStore) CountRecords(ctx context.Context, datasetID int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM sft_records WHERE dataset_id = $1`, datasetID).Scan(&count)
	return count, err
}
