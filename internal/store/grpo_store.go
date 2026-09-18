package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GrpoStore 读写 GRPO 教师模型评判提示词。
//
// 落点是 grpo_prompts（迁移 0017），刻意不复用 reward_records：
// reward_records.question_id 带 UNIQUE 且导出流程在 reward 行数上设了硬门禁，
// 两种语义叠加会互相覆盖并让门禁误判通过。
type GrpoStore struct {
	db *pgxpool.Pool
}

func NewGrpoStore(db *pgxpool.Pool) *GrpoStore {
	return &GrpoStore{db: db}
}

// GrpoQuestionContext 生成评判提示词所需的问题上下文。
type GrpoQuestionContext struct {
	QuestionID    int64
	QuestionText  string
	DirectionID   int64
	DirectionName string
	ChainSteps    []model.ChainStep
}

// ListQuestionContexts 取出数据集内全部问题，并带上所属方向与其长链思维标准步骤。
//
// 方向 id 取 COALESCE(NULLIF(direction_domain_id,0), domain_id)：
// L3 会把问题挂在方向（level=2）上并写 direction_domain_id，
// 而 L3 之前生成的历史问题只写 domain_id。两种都要能取到。
//
// 长链标准步骤从 chain_standards / chain_standard_versions 读取当前版本；
// 若该方向尚无标准步骤，返回空切片（生成器会据此在提示词里声明缺少框架）。
func (s *GrpoStore) ListQuestionContexts(ctx context.Context, datasetID int64) ([]GrpoQuestionContext, error) {
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

	items := []GrpoQuestionContext{}
	for rows.Next() {
		var item GrpoQuestionContext
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

// UpsertPrompts 幂等写入一个数据集的 GRPO 提示词。
// 同一 (dataset_id, question_id) 重复生成时覆盖旧结果，与 grpo_prompts 的 UNIQUE 约束一致。
func (s *GrpoStore) UpsertPrompts(ctx context.Context, datasetID int64, prompts []model.GrpoPrompt) error {
	if len(prompts) == 0 {
		return nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, prompt := range prompts {
		levelsPayload, err := json.Marshal(prompt.Levels)
		if err != nil {
			return err
		}
		rubricsPayload, err := json.Marshal(prompt.LevelRubrics)
		if err != nil {
			return err
		}
		status := prompt.Status
		if status == "" {
			status = "generated"
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO grpo_prompts (dataset_id, question_id, domain_id, levels, judge_prompt,
                                level_rubrics, framework_ref, status)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
      ON CONFLICT (dataset_id, question_id) DO UPDATE SET
        domain_id = EXCLUDED.domain_id,
        levels = EXCLUDED.levels,
        judge_prompt = EXCLUDED.judge_prompt,
        level_rubrics = EXCLUDED.level_rubrics,
        framework_ref = EXCLUDED.framework_ref,
        status = EXCLUDED.status,
        updated_at = NOW()`,
			datasetID,
			prompt.QuestionID,
			prompt.DomainID,
			levelsPayload,
			prompt.JudgePrompt,
			rubricsPayload,
			prompt.FrameworkRef,
			status,
		); err != nil {
			return fmt.Errorf("upsert grpo prompt question_id=%d: %w", prompt.QuestionID, err)
		}
	}

	return tx.Commit(ctx)
}

// ListPrompts 按数据集读取全部 GRPO 提示词，并补齐问题文本与方向名。
func (s *GrpoStore) ListPrompts(ctx context.Context, datasetID int64) ([]model.GrpoPrompt, error) {
	rows, err := s.db.Query(ctx, `
    SELECT g.id, g.dataset_id, g.question_id, COALESCE(q.content, ''),
           g.domain_id, COALESCE(d.name, ''),
           g.levels, g.judge_prompt, g.level_rubrics, g.framework_ref, g.status,
           g.created_at, g.updated_at
    FROM grpo_prompts g
    LEFT JOIN questions q ON q.id = g.question_id
    LEFT JOIN domains d ON d.id = g.domain_id
    WHERE g.dataset_id = $1
    ORDER BY g.question_id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.GrpoPrompt{}
	for rows.Next() {
		var item model.GrpoPrompt
		var levelsPayload, rubricsPayload []byte
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.QuestionText,
			&item.DomainID, &item.DomainName,
			&levelsPayload, &item.JudgePrompt, &rubricsPayload, &item.FrameworkRef, &item.Status,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if len(levelsPayload) > 0 {
			_ = json.Unmarshal(levelsPayload, &item.Levels)
		}
		if len(rubricsPayload) > 0 {
			_ = json.Unmarshal(rubricsPayload, &item.LevelRubrics)
		}
		if item.Levels == nil {
			item.Levels = []string{}
		}
		if item.LevelRubrics == nil {
			item.LevelRubrics = []model.GrpoLevelRubric{}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CountPrompts 返回该数据集已生成的提示词数量。
func (s *GrpoStore) CountPrompts(ctx context.Context, datasetID int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM grpo_prompts WHERE dataset_id = $1`, datasetID).Scan(&count)
	return count, err
}
