package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EvalDimensionStore 管理评估维度目录（表见 sql/migrations/0011_eval_core.sql）。
// 属于冻结契约（docs/plans/eval-and-cleaning-plan.md 第 2 节 0011、第 3.8 节）。
type EvalDimensionStore struct {
	db *pgxpool.Pool
}

func NewEvalDimensionStore(db *pgxpool.Pool) *EvalDimensionStore {
	return &EvalDimensionStore{db: db}
}

// ErrBuiltinDimensionProtected 表示试图删除内置维度。
// 内置维度是评估口径的基准，允许停用（is_active=false）但不允许删除，
// 否则历史评估结果会失去维度定义、无法复现。
var ErrBuiltinDimensionProtected = errors.New("内置维度不允许删除，如需停用请设置 isActive=false")

const evalDimensionColumns = `id, key, name, category, description, rubric,
	scale_min, scale_max, is_builtin, is_active, weight, created_at, updated_at`

func scanEvalDimension(row pgx.Row) (model.EvalDimension, error) {
	var item model.EvalDimension
	err := row.Scan(&item.ID, &item.Key, &item.Name, &item.Category, &item.Description, &item.Rubric,
		&item.ScaleMin, &item.ScaleMax, &item.IsBuiltin, &item.IsActive, &item.Weight,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.EvalDimension{}, err
	}
	return item, nil
}

// List 按分类与「是否内置」筛选维度。两个参数都可为空，为空即不限制。
// 排序把内置维度排在前面、同组按 key 升序，保证界面顺序稳定。
func (s *EvalDimensionStore) List(ctx context.Context, category string, builtin *bool) ([]model.EvalDimension, error) {
	rows, err := s.db.Query(ctx, `
    SELECT `+evalDimensionColumns+` FROM eval_dimensions
    WHERE ($1 = '' OR category = $1)
      AND ($2::boolean IS NULL OR is_builtin = $2)
    ORDER BY is_builtin DESC, category ASC, key ASC`,
		strings.TrimSpace(category), builtin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.EvalDimension, 0, 64)
	for rows.Next() {
		item, err := scanEvalDimension(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Get 按 ID 读取维度。不存在时返回 pgx.ErrNoRows。
func (s *EvalDimensionStore) Get(ctx context.Context, id int64) (model.EvalDimension, error) {
	return scanEvalDimension(s.db.QueryRow(ctx,
		`SELECT `+evalDimensionColumns+` FROM eval_dimensions WHERE id = $1`, id))
}

// ListByKeys 按 key 批量读取，供评估运行时把 dimensionKeys 解析成完整维度定义。
// 返回顺序与传入 keys 一致，便于前端按用户选定顺序展示；缺失的 key 直接跳过。
func (s *EvalDimensionStore) ListByKeys(ctx context.Context, keys []string) ([]model.EvalDimension, error) {
	if len(keys) == 0 {
		return []model.EvalDimension{}, nil
	}
	rows, err := s.db.Query(ctx, `
    SELECT `+evalDimensionColumns+` FROM eval_dimensions WHERE key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]model.EvalDimension{}
	for rows.Next() {
		item, err := scanEvalDimension(rows)
		if err != nil {
			return nil, err
		}
		byKey[item.Key] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ordered := make([]model.EvalDimension, 0, len(keys))
	for _, key := range keys {
		if item, ok := byKey[key]; ok {
			ordered = append(ordered, item)
		}
	}
	return ordered, nil
}

// Categories 返回库中实际存在的分类，含内置维度的分类排在前面。
func (s *EvalDimensionStore) Categories(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `
    SELECT category FROM eval_dimensions
    GROUP BY category
    ORDER BY bool_or(is_builtin) DESC, category ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	categories := []string{}
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, err
		}
		categories = append(categories, category)
	}
	return categories, rows.Err()
}

// UpsertBuiltin 幂等写入内置维度（ON CONFLICT (key) DO NOTHING）。
//
// 刻意不更新已存在的行：用户可能调整过内置维度的权重或停用过它，
// 重跑 seed 不应把用户的调整悄悄还原。
func (s *EvalDimensionStore) UpsertBuiltin(ctx context.Context, dimensions []model.EvalDimension) (int, error) {
	if len(dimensions) == 0 {
		return 0, nil
	}
	inserted := 0
	for _, dim := range dimensions {
		tag, err := s.db.Exec(ctx, `
      INSERT INTO eval_dimensions
        (key, name, category, description, rubric, scale_min, scale_max, is_builtin, is_active, weight)
      VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, TRUE, $8)
      ON CONFLICT (key) DO NOTHING`,
			dim.Key, dim.Name, dim.Category, dim.Description, dim.Rubric,
			dim.ScaleMin, dim.ScaleMax, dim.Weight)
		if err != nil {
			return inserted, err
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// Count 返回维度总数（含已停用），用于 seed 响应里的 total。
func (s *EvalDimensionStore) Count(ctx context.Context) (int, error) {
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM eval_dimensions`).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// Upsert 新增或更新用户自定义维度。
//
// 更新语义：带 ID 按 ID 更新；不带 ID 但 key 已存在时按 key 更新（key 有唯一约束），
// 否则插入。内置维度允许编辑权重与启停，但 is_builtin 标记保持不变——否则
// 内置维度会被降级成普通维度，下次 seed 又插一份同 key 记录（key 唯一，实际会失败）。
func (s *EvalDimensionStore) Upsert(ctx context.Context, input model.EvalDimension) (model.EvalDimension, error) {
	key := strings.TrimSpace(input.Key)
	if key == "" {
		return model.EvalDimension{}, errors.New("维度 key 不能为空")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return model.EvalDimension{}, errors.New("维度名称不能为空")
	}
	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = "long_chain"
	}
	if input.ScaleMax <= input.ScaleMin {
		return model.EvalDimension{}, fmt.Errorf("维度 %s 的分值区间非法：%d~%d", key, input.ScaleMin, input.ScaleMax)
	}
	if input.Weight <= 0 {
		return model.EvalDimension{}, fmt.Errorf("维度 %s 的权重必须为正数", key)
	}

	if input.ID > 0 {
		row := s.db.QueryRow(ctx, `
      UPDATE eval_dimensions SET
        key = $2, name = $3, category = $4, description = $5, rubric = $6,
        scale_min = $7, scale_max = $8, is_active = $9, weight = $10, updated_at = NOW()
      WHERE id = $1
      RETURNING `+evalDimensionColumns,
			input.ID, key, name, category, input.Description, input.Rubric,
			input.ScaleMin, input.ScaleMax, input.IsActive, input.Weight)
		return scanEvalDimension(row)
	}

	// 不带 ID：按 key upsert。已存在时保留 is_builtin，避免把内置维度降级。
	row := s.db.QueryRow(ctx, `
    INSERT INTO eval_dimensions
      (key, name, category, description, rubric, scale_min, scale_max, is_builtin, is_active, weight)
    VALUES ($1, $2, $3, $4, $5, $6, $7, FALSE, $8, $9)
    ON CONFLICT (key) DO UPDATE SET
      name = EXCLUDED.name,
      category = EXCLUDED.category,
      description = EXCLUDED.description,
      rubric = EXCLUDED.rubric,
      scale_min = EXCLUDED.scale_min,
      scale_max = EXCLUDED.scale_max,
      is_active = EXCLUDED.is_active,
      weight = EXCLUDED.weight,
      updated_at = NOW()
    RETURNING `+evalDimensionColumns,
		key, name, category, input.Description, input.Rubric,
		input.ScaleMin, input.ScaleMax, input.IsActive, input.Weight)
	return scanEvalDimension(row)
}

// Delete 删除用户自定义维度；内置维度拒绝删除并返回 ErrBuiltinDimensionProtected。
func (s *EvalDimensionStore) Delete(ctx context.Context, id int64) error {
	var isBuiltin bool
	err := s.db.QueryRow(ctx, `SELECT is_builtin FROM eval_dimensions WHERE id = $1`, id).Scan(&isBuiltin)
	if err != nil {
		return err
	}
	if isBuiltin {
		return ErrBuiltinDimensionProtected
	}
	_, err = s.db.Exec(ctx, `DELETE FROM eval_dimensions WHERE id = $1`, id)
	return err
}
