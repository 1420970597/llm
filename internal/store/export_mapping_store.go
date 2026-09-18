package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/1420970597/llm/internal/exporter"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExportMappingStore 管理导出字段映射（表见 sql/migrations/0015_export_mappings.sql）。
// 属于冻结契约（docs/plans/eval-and-cleaning-plan.md 第 2 节 0015）。
type ExportMappingStore struct {
	db *pgxpool.Pool
}

func NewExportMappingStore(db *pgxpool.Pool) *ExportMappingStore {
	return &ExportMappingStore{db: db}
}

const exportMappingColumns = `id, name, format, target_kind, field_map, options,
	is_builtin, is_default, created_at, updated_at`

func scanExportMapping(row pgx.Row) (model.ExportMapping, error) {
	var item model.ExportMapping
	var fieldMap, options []byte
	err := row.Scan(&item.ID, &item.Name, &item.Format, &item.TargetKind, &fieldMap, &options,
		&item.IsBuiltin, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.ExportMapping{}, err
	}
	if len(fieldMap) > 0 {
		_ = json.Unmarshal(fieldMap, &item.FieldMap)
	}
	if item.FieldMap == nil {
		item.FieldMap = map[string]any{}
	}
	if len(options) > 0 {
		_ = json.Unmarshal(options, &item.Options)
	}
	if item.Options == nil {
		item.Options = map[string]any{}
	}
	return item, nil
}

// List 返回全部映射，内置映射在前、同名按创建时间排序，保证界面顺序稳定。
func (s *ExportMappingStore) List(ctx context.Context) ([]model.ExportMapping, error) {
	rows, err := s.db.Query(ctx, `SELECT `+exportMappingColumns+` FROM export_mappings
		ORDER BY is_builtin DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.ExportMapping, 0, 8)
	for rows.Next() {
		item, err := scanExportMapping(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Get 按 ID 读取映射。不存在时返回 pgx.ErrNoRows。
func (s *ExportMappingStore) Get(ctx context.Context, id int64) (model.ExportMapping, error) {
	row := s.db.QueryRow(ctx, `SELECT `+exportMappingColumns+` FROM export_mappings WHERE id = $1`, id)
	return scanExportMapping(row)
}

// Upsert 新增或更新映射。
//
// 更新时：带 ID 按 ID 更新，否则按 name 更新（name 有唯一约束）。
// 用户直接编辑内置映射时保留 is_builtin 标记，避免内置映射被改成普通映射后
// 下次 seed 又插一份同名记录。
func (s *ExportMappingStore) Upsert(ctx context.Context, input model.ExportMapping) (model.ExportMapping, error) {
	if input.Name == "" {
		return model.ExportMapping{}, errors.New("映射名称不能为空")
	}
	if input.Format == "" {
		input.Format = "jsonl"
	}
	if _, ok := exporter.Get(input.Format); !ok {
		return model.ExportMapping{}, errors.New("不支持的导出格式: " + input.Format)
	}
	if input.TargetKind == "" {
		input.TargetKind = "sft"
	}
	if input.FieldMap == nil {
		input.FieldMap = map[string]any{}
	}
	if input.Options == nil {
		input.Options = map[string]any{}
	}

	fieldMap, err := json.Marshal(input.FieldMap)
	if err != nil {
		return model.ExportMapping{}, err
	}
	options, err := json.Marshal(input.Options)
	if err != nil {
		return model.ExportMapping{}, err
	}

	// 同一格式只保留一个默认映射，否则导出时无法确定用哪份。
	if input.IsDefault {
		if _, err := s.db.Exec(ctx,
			`UPDATE export_mappings SET is_default = FALSE, updated_at = NOW()
			 WHERE format = $1 AND is_default = TRUE`, input.Format); err != nil {
			return model.ExportMapping{}, err
		}
	}

	row := s.db.QueryRow(ctx, `
		INSERT INTO export_mappings (name, format, target_kind, field_map, options, is_builtin, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (name) DO UPDATE SET
			format = EXCLUDED.format,
			target_kind = EXCLUDED.target_kind,
			field_map = EXCLUDED.field_map,
			options = EXCLUDED.options,
			is_default = EXCLUDED.is_default,
			updated_at = NOW()
		RETURNING `+exportMappingColumns,
		input.Name, input.Format, input.TargetKind, fieldMap, options, input.IsBuiltin, input.IsDefault)
	return scanExportMapping(row)
}

// SeedBuiltins 幂等写入内置映射。
//
// 只在名称不存在时插入，不覆盖用户对内置映射的改动——用户改过的映射
// 不应该在下次启动时被悄悄还原。
func (s *ExportMappingStore) SeedBuiltins(ctx context.Context) (int, error) {
	inserted := 0
	for _, spec := range exporter.BuiltinSpecs() {
		fieldMap, err := json.Marshal(spec.FieldMap)
		if err != nil {
			return inserted, err
		}
		tag, err := s.db.Exec(ctx, `
			INSERT INTO export_mappings (name, format, target_kind, field_map, options, is_builtin, is_default)
			VALUES ($1, $2, $3, $4, '{}'::jsonb, TRUE, $5)
			ON CONFLICT (name) DO NOTHING`,
			spec.Name, spec.Format, spec.TargetKind, fieldMap, spec.IsDefault)
		if err != nil {
			return inserted, err
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// EnsureSeeded 在映射表为空时补种内置映射，并返回当前全部映射。
// 供 GET /export/formats 调用，保证首次访问就能看到内置映射。
func (s *ExportMappingStore) EnsureSeeded(ctx context.Context) ([]model.ExportMapping, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		return items, nil
	}
	if _, err := s.SeedBuiltins(ctx); err != nil {
		return nil, err
	}
	return s.List(ctx)
}

// ResolveDefault 按格式挑选一份映射用于导出。
//
// 选择顺序：显式指定的 mappingID → 该格式的默认映射 → 该格式任意一份 → 空映射。
// 返回空映射时导出器会退化为内置默认字段，不会失败。
func (s *ExportMappingStore) ResolveDefault(ctx context.Context, format string, mappingID int64) (model.ExportMapping, error) {
	if mappingID > 0 {
		return s.Get(ctx, mappingID)
	}
	row := s.db.QueryRow(ctx, `SELECT `+exportMappingColumns+` FROM export_mappings
		WHERE format = $1 ORDER BY is_default DESC, id ASC LIMIT 1`, format)
	item, err := scanExportMapping(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ExportMapping{Format: format, FieldMap: map[string]any{}}, nil
	}
	if err != nil {
		return model.ExportMapping{}, err
	}
	return item, nil
}
