package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CleaningKeywordStore 管理清洗关键词库与清洗规则。
// 表结构见 sql/migrations/0012_cleaning_core.sql（冻结契约，不得新增迁移）。
type CleaningKeywordStore struct {
	db *pgxpool.Pool
}

func NewCleaningKeywordStore(db *pgxpool.Pool) *CleaningKeywordStore {
	return &CleaningKeywordStore{db: db}
}

const cleaningKeywordColumns = `id, pattern, category, match_mode, severity,
	is_builtin, is_active, note, created_at, updated_at`

func scanCleaningKeyword(row pgx.Row) (model.CleaningKeyword, error) {
	var item model.CleaningKeyword
	err := row.Scan(&item.ID, &item.Pattern, &item.Category, &item.MatchMode, &item.Severity,
		&item.IsBuiltin, &item.IsActive, &item.Note, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.CleaningKeyword{}, err
	}
	return item, nil
}

// List 按分类与启用状态过滤关键词。category 为空表示不限分类，
// active 为 nil 表示不限启用状态（true/false 则精确过滤）。
func (s *CleaningKeywordStore) List(ctx context.Context, category string, active *bool) ([]model.CleaningKeyword, error) {
	rows, err := s.db.Query(ctx, `
    SELECT `+cleaningKeywordColumns+` FROM cleaning_keywords
    WHERE ($1 = '' OR category = $1)
      AND ($2::boolean IS NULL OR is_active = $2)
    ORDER BY category, id`, category, active)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.CleaningKeyword{}
	for rows.Next() {
		item, err := scanCleaningKeyword(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Get 按 ID 读取单条关键词。
func (s *CleaningKeywordStore) Get(ctx context.Context, id int64) (model.CleaningKeyword, error) {
	return scanCleaningKeyword(s.db.QueryRow(ctx,
		`SELECT `+cleaningKeywordColumns+` FROM cleaning_keywords WHERE id = $1`, id))
}

// Upsert 新增或更新关键词。ID 为 0 时按 (pattern, category) 冲突更新，
// 否则按 ID 更新。返回落库后的完整记录。
func (s *CleaningKeywordStore) Upsert(ctx context.Context, input model.CleaningKeyword) (model.CleaningKeyword, error) {
	if input.ID == 0 {
		return s.insertKeyword(ctx, input)
	}
	return s.updateKeyword(ctx, input)
}

// insertKeyword 新增自定义关键词（is_builtin 恒为 FALSE）。
// 空值走默认：category=refusal、match_mode=contains、severity=block。
func (s *CleaningKeywordStore) insertKeyword(ctx context.Context, input model.CleaningKeyword) (model.CleaningKeyword, error) {
	if input.Pattern == "" {
		return model.CleaningKeyword{}, fmt.Errorf("pattern 不能为空")
	}
	if input.Category == "" {
		input.Category = "refusal"
	}
	if input.MatchMode == "" {
		input.MatchMode = "contains"
	}
	if input.Severity == "" {
		input.Severity = "block"
	}

	row := s.db.QueryRow(ctx, `
      INSERT INTO cleaning_keywords (pattern, category, match_mode, severity, is_builtin, is_active, note)
      VALUES ($1, $2, $3, $4, FALSE, $5, $6)
      ON CONFLICT (pattern, category) DO UPDATE SET
        match_mode = EXCLUDED.match_mode,
        severity = EXCLUDED.severity,
        is_active = EXCLUDED.is_active,
        note = EXCLUDED.note,
        updated_at = NOW()
      RETURNING `+cleaningKeywordColumns,
		input.Pattern, input.Category, input.MatchMode, input.Severity, input.IsActive, input.Note)
	return scanCleaningKeyword(row)
}

// updateKeyword 按 ID 更新已有关键词。
//
// 身份字段（pattern / category）不允许就地修改：与库中现值不一致时**报错**，
// 而不是静默丢弃。原实现只 SET match_mode/severity/is_active/note，且 WHERE id=$1
// AND pattern=$2，于是「改分类」返回 200 但值不变、「改内容」直接 404 —— 两者都是
// 假成功：用户看到「已保存」却什么也没发生。
//
// 空值语义为「保持原值」，因此调用方可以只提交要改的字段。
func (s *CleaningKeywordStore) updateKeyword(ctx context.Context, input model.CleaningKeyword) (model.CleaningKeyword, error) {
	var current model.CleaningKeyword
	err := s.db.QueryRow(ctx,
		`SELECT `+cleaningKeywordColumns+` FROM cleaning_keywords WHERE id = $1`,
		input.ID).Scan(&current.ID, &current.Pattern, &current.Category, &current.MatchMode,
		&current.Severity, &current.IsBuiltin, &current.IsActive, &current.Note,
		&current.CreatedAt, &current.UpdatedAt)
	if err != nil {
		return model.CleaningKeyword{}, err
	}

	if input.Pattern != "" && input.Pattern != current.Pattern {
		return model.CleaningKeyword{}, s.identityError(current.IsBuiltin)
	}
	if input.Category != "" && input.Category != current.Category {
		return model.CleaningKeyword{}, s.identityError(current.IsBuiltin)
	}

	// 未提交的字段沿用库中现值，避免把用户的设置重置回默认值。
	if input.MatchMode == "" {
		input.MatchMode = current.MatchMode
	}
	if input.Severity == "" {
		input.Severity = current.Severity
	}

	row := s.db.QueryRow(ctx, `
    UPDATE cleaning_keywords
    SET match_mode = $2, severity = $3, is_active = $4, note = $5, updated_at = NOW()
    WHERE id = $1
    RETURNING `+cleaningKeywordColumns,
		input.ID, input.MatchMode, input.Severity, input.IsActive, input.Note)
	return scanCleaningKeyword(row)
}

// identityError 按是否内置返回对应的身份不可变错误，
// 让接口能提示「删除后重建」（自定义）还是「只能停用」（内置）。
func (s *CleaningKeywordStore) identityError(isBuiltin bool) error {
	if isBuiltin {
		return cleaning.ErrBuiltinKeywordIdentityImmutable
	}
	return cleaning.ErrKeywordIdentityImmutable
}

// Delete 删除关键词。内置关键词拒绝删除（返回 cleaning.ErrBuiltinKeywordImmutable），
// 调用方应引导用户改为停用。返回 pgx.ErrNoRows 表示 ID 不存在。
func (s *CleaningKeywordStore) Delete(ctx context.Context, id int64) error {
	var isBuiltin bool
	err := s.db.QueryRow(ctx,
		`SELECT is_builtin FROM cleaning_keywords WHERE id = $1`, id).Scan(&isBuiltin)
	if err != nil {
		return err
	}
	if isBuiltin {
		return cleaning.ErrBuiltinKeywordImmutable
	}
	_, err = s.db.Exec(ctx, `DELETE FROM cleaning_keywords WHERE id = $1`, id)
	return err
}

// Import 批量导入关键词，按 UNIQUE(pattern, category) 幂等。
// 返回真正插入的数量与因重复被跳过的数量。
func (s *CleaningKeywordStore) Import(ctx context.Context, patterns []string, category, severity string) (inserted int, skipped int, err error) {
	if category == "" {
		category = "refusal"
	}
	if severity == "" {
		severity = "block"
	}

	seen := map[string]bool{}
	for _, pattern := range patterns {
		if pattern == "" {
			skipped++
			continue
		}
		// 同一批次内的重复也要去重，否则会多算 inserted。
		if seen[pattern] {
			skipped++
			continue
		}
		seen[pattern] = true

		tag, execErr := s.db.Exec(ctx, `
      INSERT INTO cleaning_keywords (pattern, category, match_mode, severity, is_builtin, is_active, note)
      VALUES ($1, $2, 'contains', $3, FALSE, TRUE, '')
      ON CONFLICT (pattern, category) DO NOTHING`, pattern, category, severity)
		if execErr != nil {
			return inserted, skipped, execErr
		}
		if tag.RowsAffected() == 0 {
			skipped++
			continue
		}
		inserted++
	}
	return inserted, skipped, nil
}

// SeedBuiltin 幂等地写入内置关键词库，返回本次**新增**的条数（已存在的只刷新元数据，不计入）。
//
// 已存在的 pattern 只刷新 match_mode / severity / note，不覆盖用户的 is_active ——
// 用户主动停用过的内置词，不该被下一次 seed 重新启用。
func (s *CleaningKeywordStore) SeedBuiltin(ctx context.Context) (int, error) {
	inserted := 0
	for _, kw := range cleaning.BuiltinKeywords() {
		if kw.Pattern == "" {
			continue
		}
		category := kw.Category
		if category == "" {
			category = "refusal"
		}
		mode := kw.MatchMode
		if mode == "" {
			mode = "contains"
		}
		severity := kw.Severity
		if severity == "" {
			severity = "block"
		}

		// xmax = 0 表示这行是新插入的；DO UPDATE 命中时 xmax != 0。
		// 用它可以区分「真新增」与「仅刷新」，让接口返回的 inserted 不虚报。
		var isNew bool
		err := s.db.QueryRow(ctx, `
      INSERT INTO cleaning_keywords (pattern, category, match_mode, severity, is_builtin, is_active, note)
      VALUES ($1, $2, $3, $4, TRUE, TRUE, $5)
      ON CONFLICT (pattern, category) DO UPDATE SET
        match_mode = EXCLUDED.match_mode,
        severity = EXCLUDED.severity,
        note = EXCLUDED.note,
        is_builtin = TRUE,
        updated_at = NOW()
      RETURNING (xmax = 0)`, kw.Pattern, category, mode, severity, kw.Note).Scan(&isNew)
		if err != nil {
			return inserted, err
		}
		if isNew {
			inserted++
		}
	}
	return inserted, nil
}

// EnsureSeeded 在内置关键词缺失时补种，幂等。
//
// 为什么需要它（父代理代码级评审发现的缺陷）：
// `cleaning_keywords` 表由迁移 0012 创建，但迁移里**没有任何 INSERT**
// —— 41 条内置词只存在于 Go 代码（internal/cleaning/keywords.go 的 BuiltinKeywords()），
// 而补种只挂在 `POST /api/v1/cleaning/keywords/seed` 上，**前端完全没有这个入口**。
//
// 于是全新部署（或清空过关键词库的环境）会出现：
//  1. 清洗页关键词库为空，空状态文案只说「点新增关键词或批量粘贴」，
//     用户不可能知道工具本应自带 41 个内置词；
//  2. 更严重的是**清洗静默失效**：apps/worker/job_cleaning.go 只加载
//     `is_active = TRUE` 的词，库为空 -> 匹配不到任何东西 ->
//     internal/cleaning/scanner.go 的 `if len(matches) == 0 { return ActionClean }`
//     -> 所有拒答内容都被判为「干净」，而界面显示「清洗完成、命中 0 条」。
//     用户会误以为数据质量良好。
//
// 这与 model_providers / storage_profiles 的启动引导是**同一类问题**：
// 内置目录类数据缺了「首次可用即自动就绪」这条路径。而 export_mappings 早就这么做了
// （internal/store/export_mapping_store.go 的 EnsureSeeded 挂在 GET 上），
// 本方法与之对称：**幂等、只在缺失时补种、不覆盖用户改动**
// （SeedBuiltin 本身就不覆盖 is_active，用户停用过的内置词不会被重新启用）。
func (s *CleaningKeywordStore) EnsureSeeded(ctx context.Context) error {
	total, err := s.CountBuiltin(ctx)
	if err != nil {
		return err
	}
	if total > 0 {
		return nil
	}
	_, err = s.SeedBuiltin(ctx)
	return err
}

// CountBuiltinInserted 统计库中内置关键词总数，供 seed 接口返回 total。
func (s *CleaningKeywordStore) CountBuiltin(ctx context.Context) (int, error) {
	var total int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM cleaning_keywords WHERE is_builtin = TRUE`).Scan(&total)
	return total, err
}

const cleaningRuleColumns = `id, name, stage_scope, min_hits, action, priority,
	is_active, config, created_at, updated_at`

func scanCleaningRule(row pgx.Row) (model.CleaningRule, error) {
	var item model.CleaningRule
	var stageScope, config []byte
	err := row.Scan(&item.ID, &item.Name, &stageScope, &item.MinHits, &item.Action,
		&item.Priority, &item.IsActive, &config, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.CleaningRule{}, err
	}
	if len(stageScope) > 0 {
		_ = json.Unmarshal(stageScope, &item.StageScope)
	}
	if item.StageScope == nil {
		item.StageScope = []string{}
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &item.Config)
	}
	if item.Config == nil {
		item.Config = map[string]any{}
	}
	return item, nil
}

// ListRules 列出清洗规则，按优先级升序（与判定顺序一致）。
func (s *CleaningKeywordStore) ListRules(ctx context.Context) ([]model.CleaningRule, error) {
	rows, err := s.db.Query(ctx, `
    SELECT `+cleaningRuleColumns+` FROM cleaning_rules
    ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.CleaningRule{}
	for rows.Next() {
		item, err := scanCleaningRule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpsertRule 新增或更新清洗规则（按 name 唯一）。
func (s *CleaningKeywordStore) UpsertRule(ctx context.Context, input model.CleaningRule) (model.CleaningRule, error) {
	if input.Name == "" {
		return model.CleaningRule{}, fmt.Errorf("rule name 不能为空")
	}
	if input.Action == "" {
		input.Action = "flag"
	}
	if input.MinHits < 1 {
		input.MinHits = 1
	}
	stageScope := input.StageScope
	if stageScope == nil {
		stageScope = []string{"question", "reasoning", "answer"}
	}
	config := input.Config
	if config == nil {
		config = map[string]any{}
	}
	scopePayload, err := json.Marshal(stageScope)
	if err != nil {
		return model.CleaningRule{}, err
	}
	configPayload, err := json.Marshal(config)
	if err != nil {
		return model.CleaningRule{}, err
	}

	row := s.db.QueryRow(ctx, `
    INSERT INTO cleaning_rules (name, stage_scope, min_hits, action, priority, is_active, config)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    ON CONFLICT (name) DO UPDATE SET
      stage_scope = EXCLUDED.stage_scope,
      min_hits = EXCLUDED.min_hits,
      action = EXCLUDED.action,
      priority = EXCLUDED.priority,
      is_active = EXCLUDED.is_active,
      config = EXCLUDED.config,
      updated_at = NOW()
    RETURNING `+cleaningRuleColumns,
		input.Name, scopePayload, input.MinHits, input.Action, input.Priority, input.IsActive, configPayload)
	return scanCleaningRule(row)
}
