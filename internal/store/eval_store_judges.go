package store

import (
	"context"
	"fmt"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EvalJudgeStore 读写某次评估运行的裁判配置（表 eval_run_judges，迁移 0011）。
//
// 表上 UNIQUE(eval_run_id, provider_id)：同一 provider 在一次运行里只能有一条记录。
// UpsertRunJudges 依赖这个约束做覆盖写，因此重复调用是幂等的。
type EvalJudgeStore struct {
	db *pgxpool.Pool
}

func NewEvalJudgeStore(db *pgxpool.Pool) *EvalJudgeStore {
	return &EvalJudgeStore{db: db}
}

// ListProviderOptions 返回可作为裁判的 provider 选项。
//
// 只取启用中的 provider：未启用的模型即使被选中也无法调用。
// excluded 字段此处不判定（需要知道生成者是谁），由 eval.ResolveJudges 填充；
// 这里统一返回 false，前端拿到运行上下文后再标灰。
func (s *EvalJudgeStore) ListProviderOptions(ctx context.Context) ([]model.EvalJudgeOption, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, name, model, is_active
    FROM model_providers
    ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	options := []model.EvalJudgeOption{}
	for rows.Next() {
		var item model.EvalJudgeOption
		if err := rows.Scan(&item.ProviderID, &item.ProviderName, &item.Model, &item.IsActive); err != nil {
			return nil, err
		}
		options = append(options, item)
	}
	return options, rows.Err()
}

// GeneratorProviderID 返回该次评估所属数据集的生成者 provider id。
//
// 生成者模型必须被排除在裁判之外（需求：多 LLM 互评，禁止自评）。
//
// 优先取 eval_runs.generator_provider_id（创建运行时冻结的值，L9 写入）；
// 为 0 时回退到 datasets.provider_id —— 数据集实际用哪个模型生成，
// 权威来源就是 datasets 表，早期创建的运行可能没填这一列。
// 运行不存在时返回 pgx.ErrNoRows。
func (s *EvalJudgeStore) GeneratorProviderID(ctx context.Context, runID int64) (int64, error) {
	var providerID int64
	err := s.db.QueryRow(ctx, `
    SELECT COALESCE(NULLIF(r.generator_provider_id, 0), d.provider_id, 0)
    FROM eval_runs r
    JOIN datasets d ON d.id = r.dataset_id
    WHERE r.id = $1`, runID).Scan(&providerID)
	if err != nil {
		return 0, err
	}
	return providerID, nil
}

// ListRunJudges 返回某次评估运行的裁判记录（含被剔除项，便于前端解释）。
func (s *EvalJudgeStore) ListRunJudges(ctx context.Context, runID int64) ([]model.EvalRunJudge, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, eval_run_id, provider_id, provider_name, model,
           excluded, exclude_reason, status, scored_items, error_summary, created_at
    FROM eval_run_judges
    WHERE eval_run_id = $1
    ORDER BY provider_id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	judges := []model.EvalRunJudge{}
	for rows.Next() {
		var item model.EvalRunJudge
		if err := rows.Scan(
			&item.ID,
			&item.EvalRunID,
			&item.ProviderID,
			&item.ProviderName,
			&item.Model,
			&item.Excluded,
			&item.ExcludeReason,
			&item.Status,
			&item.ScoredItems,
			&item.ErrorSummary,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		judges = append(judges, item)
	}
	return judges, rows.Err()
}

// UpsertRunJudges 覆盖写入某次运行的裁判列表。
//
// 语义：先删除该运行下不在入参中的 provider，再逐条 upsert。
// 删除是必要的——用户把某个裁判取消勾选后，旧记录若不删就会继续参与评分。
//
// 整个操作在一个事务里，避免「删了旧的、新的没写进去」的中间态。
func (s *EvalJudgeStore) UpsertRunJudges(ctx context.Context, runID int64, judges []model.EvalRunJudge) ([]model.EvalRunJudge, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("评估运行 ID 必须为正整数，当前为 %d", runID)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	providerIDs := make([]int64, 0, len(judges))
	seen := make(map[int64]struct{}, len(judges))
	for _, judge := range judges {
		if judge.ProviderID <= 0 {
			return nil, fmt.Errorf("评估裁判 ID 必须为正整数，当前为 %d", judge.ProviderID)
		}
		if _, duplicated := seen[judge.ProviderID]; duplicated {
			return nil, fmt.Errorf("评估裁判重复：ID %d 出现了多次", judge.ProviderID)
		}
		seen[judge.ProviderID] = struct{}{}
		providerIDs = append(providerIDs, judge.ProviderID)
	}

	// 先清理不再入选的裁判。空列表时清空全部。
	if _, err := tx.Exec(ctx, `
    DELETE FROM eval_run_judges
    WHERE eval_run_id = $1 AND NOT (provider_id = ANY($2::bigint[]))`,
		runID, providerIDs,
	); err != nil {
		return nil, err
	}

	for _, judge := range judges {
		status := judge.Status
		if status == "" {
			status = "pending"
		}
		if _, err := tx.Exec(ctx, `
    INSERT INTO eval_run_judges
      (eval_run_id, provider_id, provider_name, model, excluded, exclude_reason, status)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    ON CONFLICT (eval_run_id, provider_id) DO UPDATE SET
      provider_name  = EXCLUDED.provider_name,
      model          = EXCLUDED.model,
      excluded       = EXCLUDED.excluded,
      exclude_reason = EXCLUDED.exclude_reason,
      status         = EXCLUDED.status`,
			runID,
			judge.ProviderID,
			judge.ProviderName,
			judge.Model,
			judge.Excluded,
			judge.ExcludeReason,
			status,
		); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ListRunJudges(ctx, runID)
}

// UpdateJudgeStatus 记录某裁判的评分进度。
//
// 供 L9 评分任务在每条打分后回写；单独成方法是因为进度更新频次高，
// 不应触发 UpsertRunJudges 的「删除未入选项」逻辑。
func (s *EvalJudgeStore) UpdateJudgeStatus(ctx context.Context, runID, providerID int64, status string, scoredItems int, errorSummary string) error {
	_, err := s.db.Exec(ctx, `
    UPDATE eval_run_judges
    SET status = $3, scored_items = $4, error_summary = $5
    WHERE eval_run_id = $1 AND provider_id = $2`,
		runID, providerID, status, scoredItems, errorSummary)
	return err
}
