package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现冻结质量实验的读写与报告（Issue #160 T14）。
//
// 契约：docs/plans/atelier-implementation.md §2.3（质量分母）、§2.6（缺分语义）、
// §4.1；sql/migrations/0028_studio_experiments.sql 的文件头（表结构取舍）。
//
// 本文件的三条核心主张：
//
//  1. **创建即冻结**：CreateExperiment 在一个事务里写死样本版本清单、量表与
//     裁判快照。此后的任何配置变更（维度/provider/样本新版本）都不影响它 ——
//     报告因此永远能复算。
//  2. **缺分不落成 0**：写入评分时，缺口写 NULL + score_state，而不是 0。
//     0 会被聚合当成最差分，把「没评」显示成「评得很差」。
//  3. **分母只来自 experiment_items**：报告的分母永远是那张表的行数，
//     任何筛选/隔离都不参与分母计算。

// ErrExperimentNotRunnable 表示该实验按当前状态/目标类型不能执行。
var ErrExperimentNotRunnable = errors.New("该实验当前不可执行")

// ExperimentStore 提供冻结实验的读写与报告。
type ExperimentStore struct {
	db *pgxpool.Pool
}

// NewExperimentStore 构造实验 store。
func NewExperimentStore(db *pgxpool.Pool) *ExperimentStore {
	return &ExperimentStore{db: db}
}

// CreateExperimentInput 是创建实验的请求。
type CreateExperimentInput struct {
	ProjectID    int64
	BatchID      *int64
	TargetKind   string
	Purpose      string
	SamplingSeed int64

	// SampleVersionIDs 是**创建时固定**的待评范围（契约 §2.5）。
	// 之后修改样本当前指针不会改变它。
	SampleVersionIDs []int64

	Judges []model.JudgeSpec
	Rubric model.RubricSpec

	MissingScorePolicy string
	CreatedBy          *int64
}

// CreateExperiment 冻结一个实验。
//
// 顺序刻意是「校验 → 推导生成来源 → 独立性检查 → 写入」：
// 生成来源**由样本推导**（不接受客户端传入），因为请求体里带一个
// generatorConnectionId 就能让「生成者 == 裁判」看起来成立，从而绕过
// 独立性检查（T14 验收项明确禁止）。
func (s *ExperimentStore) CreateExperiment(ctx context.Context, input CreateExperimentInput) (model.Experiment, error) {
	if len(input.SampleVersionIDs) == 0 {
		return model.Experiment{}, &apiStoreError{Message: "实验范围不能为空，请先选择要评估的样本版本"}
	}
	if len(input.Judges) == 0 {
		return model.Experiment{}, &apiStoreError{Message: "实验至少需要一名裁判"}
	}
	if err := input.Rubric.Validate(); err != nil {
		return model.Experiment{}, err
	}
	if input.MissingScorePolicy == "" {
		input.MissingScorePolicy = model.MissingScoreExclude
	}
	if input.Purpose == "" {
		input.Purpose = model.ExperimentPurposeQuality
	}
	if runnable, reason := model.ExperimentRunnable(input.TargetKind, "T14"); !runnable {
		return model.Experiment{}, &apiStoreError{Message: reason}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Experiment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	items, sources, err := deriveExperimentItemsTx(ctx, tx, input.ProjectID, input.SampleVersionIDs)
	if err != nil {
		return model.Experiment{}, err
	}

	// 独立性检查在**写入之前**：拒绝时不该留下一个半成品实验。
	independent, coverage := model.CheckJudgeIndependence(input.Judges, sources)
	if !independent {
		return model.Experiment{}, &apiStoreError{Message: "没有独立裁判：全部裁判都与生成来源同源（同一接入点），无法自评"}
	}

	judgesJSON, err := json.Marshal(input.Judges)
	if err != nil {
		return model.Experiment{}, err
	}
	rubricJSON, err := json.Marshal(input.Rubric)
	if err != nil {
		return model.Experiment{}, err
	}
	sourcesJSON, err := json.Marshal(sources)
	if err != nil {
		return model.Experiment{}, err
	}

	var experimentID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO experiments
      (project_id, batch_id, target_kind, status, purpose, sampling_seed,
       judges, rubric, generator_sources, missing_score_policy, created_by)
    VALUES ($1, $2, $3, 'queued', $4, $5, $6, $7, $8, $9, $10)
    RETURNING id`,
		input.ProjectID, input.BatchID, input.TargetKind, input.Purpose, input.SamplingSeed,
		judgesJSON, rubricJSON, sourcesJSON, input.MissingScorePolicy, input.CreatedBy,
	).Scan(&experimentID); err != nil {
		return model.Experiment{}, err
	}

	for _, item := range items {
		if _, err := tx.Exec(ctx, `
      INSERT INTO experiment_items
        (experiment_id, project_id, sample_id, sample_version_id, content_hash,
         generator_source, generator_fingerprint, status)
      VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')`,
			experimentID, input.ProjectID, item.SampleID, item.SampleVersionID, item.ContentHash,
			item.GeneratorSource, item.GeneratorFingerprint); err != nil {
			return model.Experiment{}, err
		}
	}

	// 分母在创建时就写下来：它等于 items 行数，且此后不再变化。
	if _, err := tx.Exec(ctx, `
    UPDATE experiments SET inspected_count = $2, updated_at = NOW() WHERE id = $1`,
		experimentID, len(items)); err != nil {
		return model.Experiment{}, err
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:    derefInt64(input.CreatedBy),
		Action:     "experiment_create",
		Resource:   "experiment",
		ResourceID: fmt.Sprint(experimentID),
		ProjectID:  input.ProjectID,
		Reason:     fmt.Sprintf("purpose=%s inspected=%d judges=%d", input.Purpose, len(items), len(input.Judges)),
	}); err != nil {
		return model.Experiment{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Experiment{}, err
	}

	experiment, err := s.GetExperiment(ctx, experimentID)
	if err != nil {
		return model.Experiment{}, err
	}
	// 覆盖情况随返回值一起给出，界面据此显示「哪个来源只有 1 名独立裁判」。
	experiment.IndependenceCoverage = coverage
	return experiment, nil
}

// derivedItem 是「从样本版本推导出的待评项」。
type derivedItem struct {
	SampleID             int64
	SampleVersionID      int64
	ContentHash          string
	GeneratorSource      string
	GeneratorFingerprint string
}

// deriveExperimentItemsTx 从样本版本推导待评项与生成来源。
//
// 生成来源的推导链：sample_version → batch → batch.generation_config.modelConnectionId
// → model_providers.base_url → EndpointFingerprint。
//
// 为什么不接受客户端传入：见 CreateExperiment 的说明。
// 为什么指纹取自 **batch 快照里的连接**而不是连接的「当前」base_url：
// 连接可以被改（换接入点），而「这条内容当时是谁生成的」是历史事实。
func deriveExperimentItemsTx(ctx context.Context, tx pgx.Tx, projectID int64, versionIDs []int64) ([]derivedItem, []model.GeneratorSource, error) {
	rows, err := tx.Query(ctx, `
    SELECT sv.id, sv.sample_id, sv.content_hash, sv.batch_id,
           COALESCE(b.generation_config->>'modelConnectionId', '') AS connection_id,
           COALESCE(mp.base_url, '') AS base_url
    FROM sample_versions sv
    LEFT JOIN batches b ON b.id = sv.batch_id
    LEFT JOIN model_providers mp ON mp.id = NULLIF(b.generation_config->>'modelConnectionId', '')::bigint
    WHERE sv.project_id = $1 AND sv.id = ANY($2::bigint[])
    ORDER BY sv.id`, projectID, versionIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	items := []derivedItem{}
	sourceByFingerprint := map[string]*model.GeneratorSource{}
	for rows.Next() {
		var item derivedItem
		var batchID *int64
		var connectionID string
		var baseURL string
		if err := rows.Scan(&item.SampleVersionID, &item.SampleID, &item.ContentHash, &batchID,
			&connectionID, &baseURL); err != nil {
			return nil, nil, err
		}
		fingerprint := model.EndpointFingerprint(baseURL)
		item.GeneratorSource = connectionID
		item.GeneratorFingerprint = fingerprint
		items = append(items, item)

		source := sourceByFingerprint[fingerprint]
		if source == nil {
			source = &model.GeneratorSource{
				EndpointFingerprint: fingerprint,
				SampleVersionIDs:    []int64{},
			}
			if connectionID != "" {
				var parsed int64
				if _, err := fmt.Sscan(connectionID, &parsed); err == nil {
					source.ConnectionID = parsed
				}
			}
			sourceByFingerprint[fingerprint] = source
		}
		source.SampleVersionIDs = append(source.SampleVersionIDs, item.SampleVersionID)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) != len(versionIDs) {
		// 范围里有不属于本项目/不存在的版本：必须报错，否则分母会静默变小
		//（请求 10 个、实际只有 8 个存在），而用户以为评了 10 个。
		return nil, nil, &apiStoreError{Message: fmt.Sprintf(
			"实验范围里有 %d 个样本版本不存在或不属于本项目，已拒绝创建（避免分母静默变小）",
			len(versionIDs)-len(items))}
	}

	sources := make([]model.GeneratorSource, 0, len(sourceByFingerprint))
	for _, source := range sourceByFingerprint {
		sources = append(sources, *source)
	}
	return items, sources, nil
}

// GetExperiment 读取实验（含快照）。
func (s *ExperimentStore) GetExperiment(ctx context.Context, experimentID int64) (model.Experiment, error) {
	var experiment model.Experiment
	var judgesJSON, rubricJSON, sourcesJSON []byte
	err := s.db.QueryRow(ctx, `
    SELECT id, project_id, batch_id, target_kind, status, purpose, sampling_seed,
           judges, rubric, generator_sources, missing_score_policy,
           inspected_count, scored_count, missing_count, error_count,
           created_by, started_at, finished_at, created_at, updated_at
    FROM experiments WHERE id = $1`, experimentID,
	).Scan(&experiment.ID, &experiment.ProjectID, &experiment.BatchID, &experiment.TargetKind,
		&experiment.Status, &experiment.Purpose, &experiment.SamplingSeed,
		&judgesJSON, &rubricJSON, &sourcesJSON, &experiment.MissingScorePolicy,
		&experiment.InspectedCount, &experiment.ScoredCount, &experiment.MissingCount,
		&experiment.ErrorCount, &experiment.CreatedBy, &experiment.StartedAt,
		&experiment.FinishedAt, &experiment.CreatedAt, &experiment.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Experiment{}, ErrExperimentNotFound
	}
	if err != nil {
		return model.Experiment{}, err
	}
	if err := json.Unmarshal(judgesJSON, &experiment.Judges); err != nil {
		return model.Experiment{}, err
	}
	if err := json.Unmarshal(rubricJSON, &experiment.Rubric); err != nil {
		return model.Experiment{}, err
	}
	if err := json.Unmarshal(sourcesJSON, &experiment.GeneratorSources); err != nil {
		return model.Experiment{}, err
	}
	return experiment, nil
}

// ErrExperimentNotFound 表示实验不存在。
var ErrExperimentNotFound = errors.New("实验不存在")

// ListExperiments 列出项目的实验。
func (s *ExperimentStore) ListExperiments(ctx context.Context, projectID int64, limit int) ([]model.Experiment, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
    SELECT id FROM experiments WHERE project_id = $1
    ORDER BY created_at DESC, id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	experiments := make([]model.Experiment, 0, len(ids))
	for _, id := range ids {
		experiment, err := s.GetExperiment(ctx, id)
		if err != nil {
			return nil, err
		}
		experiments = append(experiments, experiment)
	}
	return experiments, nil
}

// ListExperimentItems 列出实验项。
func (s *ExperimentStore) ListExperimentItems(ctx context.Context, experimentID int64, status string, limit int) ([]model.ExperimentItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, experiment_id, project_id, sample_id, sample_version_id, content_hash,
           generator_source, generator_fingerprint, status, attempts, error_class, error_message
    FROM experiment_items
    WHERE experiment_id = $1 AND ($2 = '' OR status = $2)
    ORDER BY id LIMIT $3`, experimentID, strings.TrimSpace(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ExperimentItem{}
	for rows.Next() {
		var item model.ExperimentItem
		if err := rows.Scan(&item.ID, &item.ExperimentID, &item.ProjectID, &item.SampleID,
			&item.SampleVersionID, &item.ContentHash, &item.GeneratorSource,
			&item.GeneratorFingerprint, &item.Status, &item.Attempts,
			&item.ErrorClass, &item.ErrorMessage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecordScoreInput 是写入一格评分的请求。
type RecordScoreInput struct {
	ExperimentID      int64
	ExperimentItemID  int64
	JudgeConnectionID int64
	JudgeIndex        int
	IsIndependent     bool
	Dimension         string
	// RawScore 为 nil 表示**缺分**（不是 0）。
	RawScore   *float64
	ScoreState string
	Rationale  string
	ErrorClass string
}

// RecordScore 追加一格评分（只追加，更正通过 supersede）。
//
// 幂等：同一 (项, 裁判, 维度) 重复写入时，先把旧行标记为被取代，
// 再插入新行。部分唯一索引只约束 superseded_by IS NULL 的行，
// 因此更正不会撞唯一约束。
//
// 为什么必须保留旧行：它是判断「评分是否稳定」的唯一依据。
// 原地 UPDATE 会让「第一次给了什么分」永久消失（与 T16 的人工判断同一原则）。
func (s *ExperimentStore) RecordScore(ctx context.Context, input RecordScoreInput) (model.ExperimentScore, error) {
	if strings.TrimSpace(input.Dimension) == "" {
		return model.ExperimentScore{}, &apiStoreError{Message: "评分必须指定维度"}
	}
	if input.ScoreState == "" {
		if input.RawScore == nil {
			input.ScoreState = model.ScoreStateMissing
		} else {
			input.ScoreState = model.ScoreStateScored
		}
	}
	// 一致性：有分必须是 scored，缺分必须没有分。
	// 允许「有分却标 missing」会让报告里的分子分母对不上，
	// 而那种不一致在界面上表现为「覆盖 90% 但平均分算不出来」。
	switch input.ScoreState {
	case model.ScoreStateScored:
		if input.RawScore == nil {
			return model.ExperimentScore{}, &apiStoreError{Message: "标记为已评分时必须给出分数"}
		}
	case model.ScoreStateMissing, model.ScoreStateError, model.ScoreStateNotApplicable:
		if input.RawScore != nil {
			return model.ExperimentScore{}, &apiStoreError{Message: "缺分/出错/不适用时不得给出分数"}
		}
	default:
		return model.ExperimentScore{}, &apiStoreError{Message: "评分状态不合法"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.ExperimentScore{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingID int64
	err = tx.QueryRow(ctx, `
    SELECT id FROM experiment_scores
    WHERE experiment_item_id = $1 AND judge_connection_id = $2 AND dimension = $3
      AND superseded_by IS NULL
    FOR UPDATE`, input.ExperimentItemID, input.JudgeConnectionID, input.Dimension).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return model.ExperimentScore{}, err
	}

	// 顺序很关键（这是一次真实缺陷的修复，由测试发现）：
	// `uniq_experiment_score_current` 是**部分**唯一索引（只约束
	// superseded_by IS NULL 的行）。若先 INSERT 新行再标记旧行，
	// 两条行在那一刻都是 NULL，索引立刻报 23505 → 更正永远失败。
	//
	// 因此先取下一个序列值、用**显式 id** 插入，并在插入前就把旧行标成
	// 被它取代。整个过程在同一事务与行锁下，不存在竞争。
	newID := existingID
	if existingID != 0 {
		if err := tx.QueryRow(ctx, `
      SELECT nextval(pg_get_serial_sequence('experiment_scores', 'id'))`).Scan(&newID); err != nil {
			return model.ExperimentScore{}, err
		}
		if _, err := tx.Exec(ctx, `
      UPDATE experiment_scores SET superseded_by = $2 WHERE id = $1`, existingID, newID); err != nil {
			return model.ExperimentScore{}, err
		}
	}

	if existingID == 0 {
		if err := tx.QueryRow(ctx, `
      INSERT INTO experiment_scores
        (experiment_id, experiment_item_id, judge_connection_id, judge_index, is_independent,
         dimension, raw_score, score_state, rationale, error_class)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
      RETURNING id`,
			input.ExperimentID, input.ExperimentItemID, input.JudgeConnectionID, input.JudgeIndex,
			input.IsIndependent, input.Dimension, input.RawScore, input.ScoreState,
			input.Rationale, input.ErrorClass,
		).Scan(&newID); err != nil {
			return model.ExperimentScore{}, err
		}
	} else {
		if _, err := tx.Exec(ctx, `
      INSERT INTO experiment_scores
        (id, experiment_id, experiment_item_id, judge_connection_id, judge_index, is_independent,
         dimension, raw_score, score_state, rationale, error_class)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			newID, input.ExperimentID, input.ExperimentItemID, input.JudgeConnectionID,
			input.JudgeIndex, input.IsIndependent, input.Dimension, input.RawScore,
			input.ScoreState, input.Rationale, input.ErrorClass); err != nil {
			return model.ExperimentScore{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return model.ExperimentScore{}, err
	}
	return model.ExperimentScore{
		ID: newID, ExperimentID: input.ExperimentID, ExperimentItemID: input.ExperimentItemID,
		JudgeConnectionID: input.JudgeConnectionID, JudgeIndex: input.JudgeIndex,
		IsIndependent: input.IsIndependent, Dimension: input.Dimension,
		RawScore: input.RawScore, ScoreState: input.ScoreState,
		Rationale: input.Rationale, ErrorClass: input.ErrorClass,
	}, nil
}

// MarkExperimentItemStatus 更新实验项的判定状态。
//
// 幂等且只前进：已是 scored 的项**不会**被迟到的 missing/error 覆盖 ——
// 与 T05 的「迟到的失败不覆盖成功」同一原则。否则 worker 被杀后消息重投
// 会把已经拿到评分的项变成缺分，而那会让报告的分子变小。
func (s *ExperimentStore) MarkExperimentItemStatus(ctx context.Context, itemID int64, status, errorClass, message string) (bool, error) {
	switch status {
	case model.ExperimentItemScored, model.ExperimentItemMissing, model.ExperimentItemError,
		model.ExperimentItemNotApplicable, model.ExperimentItemPending:
	default:
		return false, &apiStoreError{Message: "实验项状态不合法"}
	}
	tag, err := s.db.Exec(ctx, `
    UPDATE experiment_items
    SET status = $2,
        attempts = attempts + 1,
        error_class = $3,
        error_message = $4,
        updated_at = NOW()
    WHERE id = $1
      AND status <> 'scored'`, itemID, status, strings.TrimSpace(errorClass), strings.TrimSpace(message))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RefreshExperimentCounts 从 experiment_items 事实重算计数并推导终态。
//
// 为什么从事实重算而不是增量加减：增量在重放/并发/部分失败下都会漂移，
// 而这些计数正是「分母 / 分子 / 缺分覆盖」的唯一依据。
func (s *ExperimentStore) RefreshExperimentCounts(ctx context.Context, experimentID int64) (model.Experiment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Experiment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var inspected, scored, missing, errCount, notApplicable, pending int
	err = tx.QueryRow(ctx, `
    SELECT COUNT(*),
           COUNT(*) FILTER (WHERE status = 'scored'),
           COUNT(*) FILTER (WHERE status = 'missing'),
           COUNT(*) FILTER (WHERE status = 'error'),
           COUNT(*) FILTER (WHERE status = 'not_applicable'),
           COUNT(*) FILTER (WHERE status = 'pending')
    FROM experiment_items WHERE experiment_id = $1`, experimentID,
	).Scan(&inspected, &scored, &missing, &errCount, &notApplicable, &pending)
	if err != nil {
		return model.Experiment{}, err
	}

	// 终态判定（§4.2 的实验状态机 + #160「失败不能伪装为 completed 100%」）：
	//   pending>0 且无终态问题 → running（尚未跑完）
	//   有 error 或 missing     → partial_failed（有结果但覆盖不完整）
	//   全部 scored             → completed
	//
	// **missing 必须计入 partial_failed**（由测试发现的真实缺陷）：
	// 只看出错会让「3 项全部缺分」显示成 completed —— 而缺分意味着某个维度
	// 根本没有覆盖，把它标成「已完成」正是 issue 明确禁止的
	// 「失败伪装成 completed 100%」：用户会以为质量检查已经做完。
	status := model.ExperimentStatusQueued
	switch {
	case errCount > 0 || missing > 0:
		status = model.ExperimentStatusPartialFailed
	case pending > 0:
		status = model.ExperimentStatusRunning
	default:
		status = model.ExperimentStatusCompleted
	}

	if _, err := tx.Exec(ctx, `
    UPDATE experiments
    SET inspected_count = $2, scored_count = $3, missing_count = $4, error_count = $5,
        status = $6,
        finished_at = CASE WHEN $7 = 0 THEN COALESCE(finished_at, NOW()) ELSE finished_at END,
        updated_at = NOW()
    WHERE id = $1`,
		experimentID, inspected, scored, missing, errCount, status, pending); err != nil {
		return model.Experiment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Experiment{}, err
	}
	return s.GetExperiment(ctx, experimentID)
}

// ExperimentDimensionStat 是一个维度的聚合结果。
type ExperimentDimensionStat struct {
	Dimension string  `json:"dimension"`
	Weight    float64 `json:"weight"`
	// Mean 是**归一化后**（0–1）的平均分，只对 scored 的格取平均。
	// 缺分不参与，也不被当成 0（那会把缺分算成最差分）。
	Mean float64 `json:"mean"`
	// ScoredCount 是该维度**实际**参与计算的格数。
	// 与分母（inspected × 裁判数）一起构成覆盖，报告必须同时给出两者。
	ScoredCount  int `json:"scoredCount"`
	MissingCount int `json:"missingCount"`
	// Covered 表示参与该维度计算的样本版本数，用于说明抽样范围与覆盖。
	Covered int `json:"covered"`
}

// ExperimentReport 是实验报告（固定分母 + 完成覆盖）。
type ExperimentReport struct {
	ExperimentID int64                     `json:"experimentId"`
	Status       string                    `json:"status"`
	TargetKind   string                    `json:"targetKind"`
	Stats        model.ExperimentStats     `json:"stats"`
	Dimensions   []ExperimentDimensionStat `json:"dimensions"`
	// JudgeAgreement 是裁判两两一致的说明（分歧必须可见，见 §3）。
	JudgeNotes []string `json:"judgeNotes"`
	// IndependenceCoverage 是「每个生成来源有哪些独立裁判」。
	IndependenceCoverage map[string][]int64 `json:"independenceCoverage,omitempty"`
}

// BuildExperimentReport 计算报告。
//
// 三条与 §3.1/§2.3 直接相关的规则：
//
//  1. **分母是冻结的 inspected_count**，不随缺分/出错变小；
//  2. **平均分只用 scored 的格**，且缺分**不补 0**（补 0 = 把没评算成最差）；
//  3. **覆盖必须显式给出**（每维度的实际参与格数 + 样本版本覆盖），
//     因为「抽样结论」与「全量结论」的可信度不同。
func (s *ExperimentStore) BuildExperimentReport(ctx context.Context, experimentID int64) (ExperimentReport, error) {
	experiment, err := s.GetExperiment(ctx, experimentID)
	if err != nil {
		return ExperimentReport{}, err
	}

	rows, err := s.db.Query(ctx, `
    WITH normalized AS (
      SELECT es.dimension,
             -- 归一化用**冻结的量表**范围（不查当前维度表）：报告必须能复算。
             (es.raw_score - COALESCE(rd.min, 0)) / NULLIF(COALESCE(rd.max, 0) - COALESCE(rd.min, 0), 0) AS normalized,
             es.raw_score, es.score_state, es.experiment_item_id
      FROM experiment_scores es
      LEFT JOIN LATERAL (
        SELECT (elem->>'min')::double precision AS min, (elem->>'max')::double precision AS max
        FROM jsonb_array_elements($2::jsonb) elem
        WHERE elem->>'key' = es.dimension
        LIMIT 1
      ) rd ON TRUE
      WHERE es.experiment_id = $1 AND es.superseded_by IS NULL
    )
    SELECT dimension,
           COUNT(*) FILTER (WHERE score_state = 'scored' AND normalized IS NOT NULL) AS scored_count,
           COUNT(*) FILTER (WHERE score_state <> 'scored') AS missing_count,
           COUNT(DISTINCT experiment_item_id) FILTER (WHERE score_state = 'scored') AS covered,
           AVG(normalized) FILTER (WHERE score_state = 'scored' AND normalized IS NOT NULL) AS mean
    FROM normalized
    GROUP BY dimension
    ORDER BY dimension`,
		experimentID, mustJSON(mustJSONArray(experiment.Rubric.Dimensions)))
	if err != nil {
		return ExperimentReport{}, err
	}
	defer rows.Close()

	report := ExperimentReport{
		ExperimentID: experiment.ID,
		Status:       experiment.Status,
		TargetKind:   experiment.TargetKind,
		JudgeNotes:   []string{},
	}
	stats := model.BuildExperimentStats(experiment.InspectedCount, experiment.ScoredCount,
		experiment.MissingCount, experiment.ErrorCount, 0, 0, 0)
	report.Stats = stats

	weightByKey := map[string]float64{}
	for _, dimension := range experiment.Rubric.Dimensions {
		weightByKey[dimension.Key] = dimension.Weight
	}

	for rows.Next() {
		var stat ExperimentDimensionStat
		var mean *float64
		if err := rows.Scan(&stat.Dimension, &stat.ScoredCount, &stat.MissingCount, &stat.Covered, &mean); err != nil {
			return ExperimentReport{}, err
		}
		stat.Weight = weightByKey[stat.Dimension]
		if mean != nil {
			stat.Mean = *mean
		}
		report.Dimensions = append(report.Dimensions, stat)
	}
	if err := rows.Err(); err != nil {
		return ExperimentReport{}, err
	}
	if report.Dimensions == nil {
		report.Dimensions = []ExperimentDimensionStat{}
	}

	// 裁判分歧提示：分母固定时，分歧是唯一能说明「结论是否稳」的信号。
	// 这里只给事实（不同维度的覆盖差异），不做显著性断言（§2.3 禁止无依据结论）。
	for _, dimension := range report.Dimensions {
		expected := experiment.InspectedCount * len(experiment.Judges)
		if dimension.ScoredCount < expected {
			report.JudgeNotes = append(report.JudgeNotes, fmt.Sprintf(
				"维度 %s 的评分覆盖为 %d/%d，未覆盖部分不计入该维度平均分（不要与全量通过混为一谈）",
				dimension.Dimension, dimension.ScoredCount, expected))
		}
	}
	return report, nil
}

// ListPendingExperimentItems 返回尚未完成（或失败可重试）的实验项。
//
// 「续跑只处理未完成项」的实现：成功项的 status 是 scored，
// 而 MarkExperimentItemStatus 的 `status <> 'scored'` 条件使它们不会被改写。
func (s *ExperimentStore) ListPendingExperimentItems(ctx context.Context, experimentID int64, limit int) ([]model.ExperimentItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, experiment_id, project_id, sample_id, sample_version_id, content_hash,
           generator_source, generator_fingerprint, status, attempts, error_class, error_message
    FROM experiment_items
    WHERE experiment_id = $1 AND status IN ('pending', 'error')
    ORDER BY id LIMIT $2`, experimentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ExperimentItem{}
	for rows.Next() {
		var item model.ExperimentItem
		if err := rows.Scan(&item.ID, &item.ExperimentID, &item.ProjectID, &item.SampleID,
			&item.SampleVersionID, &item.ContentHash, &item.GeneratorSource,
			&item.GeneratorFingerprint, &item.Status, &item.Attempts,
			&item.ErrorClass, &item.ErrorMessage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		// 输入全是本地构造的基础类型，Marshal 不会失败；
		// 真失败时返回空数组而不是崩溃，让查询退化成「没有维度」而不是 500。
		return []byte("[]")
	}
	return raw
}

func mustJSONArray(value any) json.RawMessage {
	return json.RawMessage(mustJSON(value))
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

var _ = time.Now
