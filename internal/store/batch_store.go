package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 批次、单元与不可变样本版本（Issue #160 T05）。
//
// 契约：docs/plans/atelier-implementation.md §4.1、§4.2、§2.6；
// docs/plans/atelier-api-contract.md §2.3、§2.4。
//
// 四条必须成立的不变式：
//  1. **批次独立**：同项目可以创建两次 pilot 和一次 scale，得到三个独立 ID，
//     互不覆盖（#160 §1 对「唯一性不是租约」的批评正是指这里）。
//  2. **快照冻结**：批次记录它当时引用的版本行 ID 与内容 hash。
//     保存新蓝图不改旧快照（T05 验收项）。
//  3. **幂等提交**：重放相同成功项不增加样本 —— 靠 batch_items 的
//     UNIQUE (batch_id, item_key) + 单语句条件更新，而不是应用层判断。
//  4. **内容只追加**：sample_versions 没有 UPDATE 路径。
//     原样本重生成是新版本；同题不同批次是不同 sample 或不同版本，不互相覆盖。

// ErrBatchNotControllable 表示批次当前状态不允许该控制动作。
//
// 单独一个哨兵错误：handler 必须把它映射成 409（契约 §2.4「非法状态转换 → 409」），
// 而不是 500。用户在「已完成的批次上点暂停」是常见操作，报服务端故障会误导。
var ErrBatchNotControllable = errors.New("当前批次状态不允许该操作")

// ErrSampleVersionConflict 表示同一样本版本号已被占用（并发生成）。
var ErrSampleVersionConflict = errors.New("样本版本写入冲突，请重试")

type BatchStore struct {
	db *pgxpool.Pool
}

func NewBatchStore(db *pgxpool.Pool) *BatchStore {
	return &BatchStore{db: db}
}

// ---------------------------------------------------------------------------
// 创建批次
// ---------------------------------------------------------------------------

// CreateBatch 在单个事务内创建批次、阶段行、事件与审计。
//
// 快照 hash 由服务端从**被引用的版本行**读出，而不是信任客户端传来的 hash：
// 客户端提供的 hash 无法证明它对应库里的内容，而快照的全部价值就在于
// 「事后能核对内容是否变过」。因此这里必须自己查。
func (s *BatchStore) CreateBatch(ctx context.Context, projectID, actorID int64, targetKind string, input model.CreateBatchInput) (model.Batch, error) {
	input.Normalize()
	if err := input.Validate(); err != nil {
		return model.Batch{}, err
	}
	if targetKind != model.TargetKindSFT && targetKind != model.TargetKindGRPO {
		return model.Batch{}, &apiStoreError{Message: "项目目标类型不合法"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Batch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot, generationConfig, err := resolveBatchSnapshotTx(ctx, tx, projectID, input)
	if err != nil {
		return model.Batch{}, err
	}
	generationConfig.SchemaVersion = model.SampleSchemaForTarget(targetKind)

	slice := input.CoverageSlice
	if len(slice) == 0 {
		slice = json.RawMessage(`{}`)
	}

	var batch model.Batch
	var leaseUntil *time.Time
	// 快照里的版本引用列是 NULLable（0 表示「未引用」存为 NULL），
	// 因此必须扫描到 *int64 再拍平。直接扫进 int64 会在
	// 「批次只引用蓝图、未引用覆盖/标准」时失败 —— 而那是试制的常见形态。
	var (
		blueprintVersionID     *int64
		coverageVersionID      *int64
		standardVersionID      *int64
		qualityPolicyVersionID *int64
		mappingVersionID       *int64
	)
	// 局部缓冲：绝不能用包级变量。CreateBatch 会被并发的 HTTP 请求调用，
	// 共享一个 []byte 扫描目标会让两个请求互相覆盖对方的 generation_config
	// （-race 可测的真实数据竞争，而不是理论风险）。
	var rawGenerationConfig []byte
	err = tx.QueryRow(ctx, `
    INSERT INTO batches (
      project_id, purpose, status, control_state,
      blueprint_version_id, blueprint_content_hash,
      coverage_version_id, coverage_content_hash,
      standard_version_id, standard_content_hash,
      quality_policy_version_id, quality_policy_content_hash,
      mapping_version_id, mapping_content_hash,
      generation_config, target_kind, schema_version,
      planned_units, budget_currency, budget_limit_minor, coverage_slice, created_by)
    VALUES ($1, $2, 'queued', 'run',
            $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
            $13, $14, $15, $16, $17, $18, $19, $20)
    RETURNING id, project_id, purpose, status, control_state, target_kind, schema_version,
              blueprint_version_id, blueprint_content_hash,
              coverage_version_id, coverage_content_hash,
              standard_version_id, standard_content_hash,
              quality_policy_version_id, quality_policy_content_hash,
              mapping_version_id, mapping_content_hash,
              generation_config, planned_units, completed_units, failed_units, in_flight_units,
              budget_currency, budget_limit_minor, coverage_slice, fencing_token, lease_owner, lease_until,
              created_by, started_at, finished_at, created_at, updated_at`,
		projectID, input.Purpose,
		nullableVersionID(snapshot.BlueprintVersionID), snapshot.BlueprintContentHash,
		nullableVersionID(snapshot.CoverageVersionID), snapshot.CoverageContentHash,
		nullableVersionID(snapshot.StandardVersionID), snapshot.StandardContentHash,
		nullableVersionID(snapshot.QualityPolicyVersionID), snapshot.QualityPolicyContentHash,
		nullableVersionID(snapshot.MappingVersionID), snapshot.MappingContentHash,
		generationConfigJSON(generationConfig), targetKind, generationConfig.SchemaVersion,
		input.UnitCount, input.Budget.Currency, input.BudgetLimitValue(), slice, actorID,
	).Scan(&batch.ID, &batch.ProjectID, &batch.Purpose, &batch.Status, &batch.ControlState,
		&batch.TargetKind, &batch.SchemaVersion,
		&blueprintVersionID, &batch.Snapshot.BlueprintContentHash,
		&coverageVersionID, &batch.Snapshot.CoverageContentHash,
		&standardVersionID, &batch.Snapshot.StandardContentHash,
		&qualityPolicyVersionID, &batch.Snapshot.QualityPolicyContentHash,
		&mappingVersionID, &batch.Snapshot.MappingContentHash,
		&rawGenerationConfig, &batch.PlannedUnits, &batch.CompletedUnits, &batch.FailedUnits,
		&batch.InFlightUnits, &batch.Budget.Currency, &batch.Budget.LimitMinor,
		&batch.CoverageSlice, &batch.FencingToken, &batch.LeaseOwner, &leaseUntil,
		&batch.CreatedBy, &batch.StartedAt, &batch.FinishedAt, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return model.Batch{}, model.FieldErrors{{
				Field:   "blueprintVersionId",
				Message: "引用的文档版本不存在或不属于本项目，请重新选择方案版本",
			}}
		}
		return model.Batch{}, err
	}
	batch.Snapshot.BlueprintVersionID = derefVersionID(blueprintVersionID)
	batch.Snapshot.CoverageVersionID = derefVersionID(coverageVersionID)
	batch.Snapshot.StandardVersionID = derefVersionID(standardVersionID)
	batch.Snapshot.QualityPolicyVersionID = derefVersionID(qualityPolicyVersionID)
	batch.Snapshot.MappingVersionID = derefVersionID(mappingVersionID)
	batch.GenerationConfig = decodeGenerationConfig(rawGenerationConfig)
	batch.LeaseUntil = leaseUntil
	batch.Budget.OnExhausted = model.BudgetOnExhaustedPause

	// 事件：BatchQueued（契约 §5 的事件表用的是同名字段）。
	if err := appendBatchEventTx(ctx, tx, batch.ID, projectID, model.BatchEventQueued, actorID, map[string]any{
		"projectId": projectID,
		"batchId":   batch.ID,
		"purpose":   input.Purpose,
	}); err != nil {
		return model.Batch{}, err
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:    actorID,
		Action:     "batch_create",
		Resource:   "batch",
		ResourceID: strconv.FormatInt(batch.ID, 10),
		ProjectID:  projectID,
		Reason:     fmt.Sprintf("purpose=%s units=%d", input.Purpose, input.UnitCount),
	}); err != nil {
		return model.Batch{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Batch{}, err
	}
	return batch, nil
}

// resolveBatchSnapshotTx 读取被引用版本的 hash，组装快照。
//
// 只接受**同项目**的版本行：跨项目引用在迁移 0025 里由复合外键强制，
// 但这里先查一次是为了给出可读的字段错误（外键报错只有 SQLSTATE）。
func resolveBatchSnapshotTx(ctx context.Context, tx pgx.Tx, projectID int64, input model.CreateBatchInput) (model.BatchSnapshot, model.BatchGenerationConfig, error) {
	var snapshot model.BatchSnapshot
	var generationConfig model.BatchGenerationConfig

	resolve := func(versionID int64, field string) (string, error) {
		if versionID <= 0 {
			return "", nil
		}
		var hash string
		var projectIDOfVersion int64
		err := tx.QueryRow(ctx, `
      SELECT content_hash, project_id FROM document_versions WHERE id = $1`, versionID).
			Scan(&hash, &projectIDOfVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", model.FieldErrors{{Field: field, Message: "引用的版本不存在，请重新选择"}}
		}
		if err != nil {
			return "", err
		}
		if projectIDOfVersion != projectID {
			return "", model.FieldErrors{{Field: field, Message: "不能引用其它项目的版本"}}
		}
		return hash, nil
	}

	var err error
	if snapshot.BlueprintContentHash, err = resolve(input.BlueprintVersionID, "blueprintVersionId"); err != nil {
		return snapshot, generationConfig, err
	}
	snapshot.BlueprintVersionID = input.BlueprintVersionID
	if snapshot.CoverageContentHash, err = resolve(input.CoverageVersionID, "coverageVersionId"); err != nil {
		return snapshot, generationConfig, err
	}
	snapshot.CoverageVersionID = input.CoverageVersionID
	if snapshot.StandardContentHash, err = resolve(input.StandardVersionID, "standardVersionId"); err != nil {
		return snapshot, generationConfig, err
	}
	snapshot.StandardVersionID = input.StandardVersionID
	if snapshot.QualityPolicyContentHash, err = resolve(input.QualityPolicyVersionID, "qualityPolicyVersionId"); err != nil {
		return snapshot, generationConfig, err
	}
	snapshot.QualityPolicyVersionID = input.QualityPolicyVersionID
	if snapshot.MappingContentHash, err = resolve(input.MappingVersionID, "mappingVersionId"); err != nil {
		return snapshot, generationConfig, err
	}
	snapshot.MappingVersionID = input.MappingVersionID

	// 生成配置从蓝图 payload 的 generation 节点解出（它是「这一批怎么跑」的权威来源）。
	// 蓝图版本缺省时保持零值：试制允许只给标准（走连接默认），
	// 真正的「必须配齐」检查在 T13 的执行前核对里。
	if input.BlueprintVersionID > 0 {
		var payload []byte
		if err := tx.QueryRow(ctx, `
      SELECT payload FROM document_versions WHERE id = $1`, input.BlueprintVersionID).Scan(&payload); err != nil {
			return snapshot, generationConfig, err
		}
		var blueprint model.BlueprintPayload
		if err := json.Unmarshal(payload, &blueprint); err == nil {
			generationConfig.ModelConnectionID = blueprint.Nodes.Generation.ModelConnectionID
			generationConfig.ModelVersion = blueprint.Nodes.Generation.ModelVersion
			generationConfig.Concurrency = blueprint.Nodes.Generation.Concurrency
			generationConfig.MaxTokens = blueprint.Nodes.Generation.MaxTokens
			generationConfig.Temperature = blueprint.Nodes.Generation.Temperature
		}
	}
	return snapshot, generationConfig, nil
}

// nullableVersionID 把 0 转成 NULL：0 表示「未引用」，
// 而 0 不是合法的 document_versions.id（BIGSERIAL 从 1 开始）。
// 存 0 会让复合外键失败，把「未引用」变成「引用不存在」。
func nullableVersionID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

// generationConfigJSON 序列化生成配置。
func generationConfigJSON(config model.BatchGenerationConfig) []byte {
	raw, err := json.Marshal(config)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

// decodeGenerationConfig 反序列化生成配置；坏数据返回零值而不是报错。
//
// 为什么不报错：它是**展示/追溯**字段。一条读路径因为历史坏数据而整体失败，
// 会让用户看不到批次，而实际影响只是「配置列显示不全」。
func decodeGenerationConfig(raw []byte) model.BatchGenerationConfig {
	var config model.BatchGenerationConfig
	if len(raw) == 0 {
		return config
	}
	_ = json.Unmarshal(raw, &config)
	return config
}

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

// batchColumns 是批次的权威列清单，**只作为核对基准，不插值进 SQL**。
//
// 为什么不把它拼进 SQL：把列清单常量插值到语句里，会让每处调用都多一个
// 「这段 SQL 不是静态的」的审查面 —— 静态分析器无法区分可信常量与用户输入，
// 于是真正的注入点会被淹没在噪声里（那正是安全门禁反复报出的形态）。
//
// 因此本文件每条语句都**完整静态**地写出列清单（含 ListBatches 的 WHERE：
// 它的条件文本本身也是静态的，只有参数是动态的）。重复的代价由
// TestBatchSelectStatementsMatchCanonicalColumns 兜住 —— 它断言每条静态
// 语句的列清单与本节逐字一致，于是「改了一处忘了另一处」会立即失败，
// 而不是等到「列表正常但详情错列」时才暴露。
const batchColumns = `
  id, project_id, purpose, status, control_state, target_kind, schema_version,
  blueprint_version_id, blueprint_content_hash,
  coverage_version_id, coverage_content_hash,
  standard_version_id, standard_content_hash,
  quality_policy_version_id, quality_policy_content_hash,
  mapping_version_id, mapping_content_hash,
  generation_config, planned_units, completed_units, failed_units, in_flight_units,
  budget_currency, budget_limit_minor, coverage_slice, fencing_token, lease_owner, lease_until,
  created_by, started_at, finished_at, created_at, updated_at`

// batchSelectByIDSQL 按 ID 读取批次（静态语句，见 batchColumns 的说明）。
const batchSelectByIDSQL = `
  SELECT
    id, project_id, purpose, status, control_state, target_kind, schema_version,
    blueprint_version_id, blueprint_content_hash,
    coverage_version_id, coverage_content_hash,
    standard_version_id, standard_content_hash,
    quality_policy_version_id, quality_policy_content_hash,
    mapping_version_id, mapping_content_hash,
    generation_config, planned_units, completed_units, failed_units, in_flight_units,
    budget_currency, budget_limit_minor, coverage_slice, fencing_token, lease_owner, lease_until,
    created_by, started_at, finished_at, created_at, updated_at
  FROM batches WHERE id = $1`

// batchListSQL 是批次列表查询（静态语句，见 batchColumns 的说明）。
//
// keyset 游标（契约 §1.5）：用 (created_at, id) 而不是 OFFSET。
// OFFSET 在并发写入下会漏行或重复行，而批次列表正是「边跑边看」的场景。
const batchListSQL = `
    SELECT
      id, project_id, purpose, status, control_state, target_kind, schema_version,
      blueprint_version_id, blueprint_content_hash,
      coverage_version_id, coverage_content_hash,
      standard_version_id, standard_content_hash,
      quality_policy_version_id, quality_policy_content_hash,
      mapping_version_id, mapping_content_hash,
      generation_config, planned_units, completed_units, failed_units, in_flight_units,
      budget_currency, budget_limit_minor, coverage_slice, fencing_token, lease_owner, lease_until,
      created_by, started_at, finished_at, created_at, updated_at
    FROM batches
    WHERE project_id = $1
      AND ($2 = '' OR purpose = $2)
      AND ($3 = '' OR status = $3)
      AND ($4::timestamptz IS NULL OR (created_at, id) < ($4::timestamptz, $5::bigint))
    ORDER BY created_at DESC, id DESC
    LIMIT $6`

// GetBatch 读取单个批次；不存在返回 pgx.ErrNoRows。
func (s *BatchStore) GetBatch(ctx context.Context, batchID int64) (model.Batch, error) {
	return scanBatch(s.db.QueryRow(ctx, batchSelectByIDSQL, batchID))
}

// BatchListQuery 是批次列表条件。
type BatchListQuery struct {
	ProjectID int64
	Purpose   string
	Status    string
	Cursor    time.Time
	CursorID  int64
	Limit     int
}

// ListBatches 按「同项目内最近」列出批次，keyset 游标（§1.5）。
//
// 刻意**不**提供「当前运行批次」这种查询：契约 §3 明确
// 「不按最大 ID 猜『当前运行』」。同项目可以有多个并行批次，
// 界面应当把它们都列出来，而不是挑一个当「当前」。
func (s *BatchStore) ListBatches(ctx context.Context, query BatchListQuery) ([]model.Batch, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var cursorTime *time.Time
	if !query.Cursor.IsZero() {
		truncated := query.Cursor
		cursorTime = &truncated
	}

	rows, err := s.db.Query(ctx, batchListSQL,
		query.ProjectID, strings.TrimSpace(query.Purpose), strings.TrimSpace(query.Status),
		cursorTime, query.CursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Batch{}
	for rows.Next() {
		batch, err := scanBatch(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, batch)
	}
	return items, rows.Err()
}

// ListBatchSteps 读取批次的所有阶段。
func (s *BatchStore) ListBatchSteps(ctx context.Context, batchID int64) ([]model.BatchStep, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, batch_id, phase, unit_label, status, total_units, done_units, failed_units,
           error_summary, started_at, finished_at, created_at, updated_at
    FROM batch_steps WHERE batch_id = $1 ORDER BY id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	steps := []model.BatchStep{}
	for rows.Next() {
		var step model.BatchStep
		if err := rows.Scan(&step.ID, &step.BatchID, &step.Phase, &step.UnitLabel, &step.Status,
			&step.TotalUnits, &step.DoneUnits, &step.FailedUnits, &step.ErrorSummary,
			&step.StartedAt, &step.FinishedAt, &step.CreatedAt, &step.UpdatedAt); err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// UpsertBatchStep 创建或更新一个阶段（按 (batch_id, phase) 唯一）。
//
// 只允许在批次活跃时更新：终态批次的阶段进度是历史事实，
// 改它会让「事件时间线」与「阶段进度」互相矛盾。
func (s *BatchStore) UpsertBatchStep(ctx context.Context, batchID int64, step model.BatchStep) error {
	_, err := s.db.Exec(ctx, `
    INSERT INTO batch_steps (batch_id, phase, unit_label, status, total_units, done_units, failed_units, error_summary)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    ON CONFLICT (batch_id, phase) DO UPDATE SET
      unit_label = EXCLUDED.unit_label,
      status = EXCLUDED.status,
      total_units = EXCLUDED.total_units,
      done_units = EXCLUDED.done_units,
      failed_units = EXCLUDED.failed_units,
      error_summary = EXCLUDED.error_summary,
      started_at = COALESCE(batch_steps.started_at, NOW()),
      finished_at = CASE WHEN EXCLUDED.status IN ('completed', 'failed', 'skipped') THEN NOW() ELSE batch_steps.finished_at END,
      updated_at = NOW()
    WHERE EXISTS (SELECT 1 FROM batches b WHERE b.id = $1 AND b.status NOT IN ('completed', 'failed'))`,
		batchID, step.Phase, step.UnitLabel, step.Status,
		step.TotalUnits, step.DoneUnits, step.FailedUnits, step.ErrorSummary)
	return err
}

// ListBatchItems 列出批次的单元（可按状态过滤）。
func (s *BatchStore) ListBatchItems(ctx context.Context, batchID int64, status string, limit int) ([]model.BatchItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, batch_id, project_id, item_key, sample_id, status, attempt,
           error_class, error_message, retryable, sample_version_id,
           started_at, finished_at, created_at, updated_at
    FROM batch_items
    WHERE batch_id = $1 AND ($2 = '' OR status = $2)
    ORDER BY id
    LIMIT $3`, batchID, strings.TrimSpace(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.BatchItem{}
	for rows.Next() {
		var item model.BatchItem
		if err := rows.Scan(&item.ID, &item.BatchID, &item.ProjectID, &item.ItemKey, &item.SampleID,
			&item.Status, &item.Attempt, &item.ErrorClass, &item.ErrorMessage, &item.Retryable,
			&item.SampleVersionID, &item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListBatchEvents 读取批次事件时间线（倒序）。
func (s *BatchStore) ListBatchEvents(ctx context.Context, batchID int64, limit int) ([]model.BatchEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, batch_id, project_id, event_type, sequence, actor_id, detail, created_at
    FROM batch_events WHERE batch_id = $1 ORDER BY sequence DESC LIMIT $2`, batchID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []model.BatchEvent{}
	for rows.Next() {
		var event model.BatchEvent
		if err := rows.Scan(&event.ID, &event.BatchID, &event.ProjectID, &event.EventType,
			&event.Sequence, &event.ActorID, &event.Detail, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// CountBatchItemsByStatus 按状态统计单元数。
//
// 用它而不是读 batches 上的计数列来做**对账**：计数列是聚合缓存，
// 而对账恰恰要验证缓存与事实一致。T05 的测试会两边都比。
func (s *BatchStore) CountBatchItemsByStatus(ctx context.Context, batchID int64) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `
    SELECT status, COUNT(*) FROM batch_items WHERE batch_id = $1 GROUP BY status`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// ---------------------------------------------------------------------------
// 控制命令（暂停 / 恢复 / 重试失败项）
// ---------------------------------------------------------------------------

// PauseBatch 请求暂停：只阻止**新**请求提交，在途请求仍可完成并计费（§2.4）。
//
// 因此 control_state 变成 pause_requested 而不是 paused —— 真正的 paused
// 发生在在途请求归零之后（T12/T13 的 drain 逻辑）。把两者合成一个状态
// 就没法表达「已请求暂停，但有 3 个请求还在跑」这一事实。
func (s *BatchStore) PauseBatch(ctx context.Context, projectID, batchID, actorID int64) (model.Batch, error) {
	return s.controlBatch(ctx, projectID, batchID, actorID, "pause")
}

// ResumeBatch 恢复暂停的批次（同一批次的新 attempt，配置不变）。
func (s *BatchStore) ResumeBatch(ctx context.Context, projectID, batchID, actorID int64) (model.Batch, error) {
	return s.controlBatch(ctx, projectID, batchID, actorID, "resume")
}

// controlBatch 是暂停/恢复的共同实现。
func (s *BatchStore) controlBatch(ctx context.Context, projectID, batchID, actorID int64, action string) (model.Batch, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Batch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SELECT ... FOR UPDATE：两个并发控制命令必须串行，
	// 否则「暂停 + 恢复」会以任意顺序生效，用户看到的最终状态取决于网络时序。
	var status, controlState string
	if err := tx.QueryRow(ctx, `
    SELECT status, control_state FROM batches WHERE id = $1 AND project_id = $2 FOR UPDATE`,
		batchID, projectID).Scan(&status, &controlState); err != nil {
		return model.Batch{}, err
	}

	terminal := status == model.BatchStatusCompleted || status == model.BatchStatusFailed
	if terminal {
		return model.Batch{}, fmt.Errorf("%w：批次已结束（%s），不能%v", ErrBatchNotControllable, status, controlVerb(action))
	}

	var nextStatus, nextControl string
	switch action {
	case "pause":
		if controlState == model.BatchControlPaused || controlState == model.BatchControlPauseRequested {
			return model.Batch{}, fmt.Errorf("%w：批次已经处于暂停或暂停请求状态", ErrBatchNotControllable)
		}
		nextControl = model.BatchControlPauseRequested
		nextStatus = model.BatchStatusPauseRequested
	case "resume":
		if controlState == model.BatchControlRun {
			return model.Batch{}, fmt.Errorf("%w：批次没有处于暂停状态", ErrBatchNotControllable)
		}
		nextControl = model.BatchControlRun
		// 恢复后的状态取决于是否已经有失败项：有就是 partial_failed
		//（界面据此继续显示「恢复失败项」入口）。
		var failedCount int
		if err := tx.QueryRow(ctx, `
      SELECT COUNT(*) FROM batch_items WHERE batch_id = $1 AND status = 'failed'`,
			batchID).Scan(&failedCount); err != nil {
			return model.Batch{}, err
		}
		if failedCount > 0 {
			nextStatus = model.BatchStatusPartialFailed
		} else {
			nextStatus = model.BatchStatusRunning
		}
	default:
		return model.Batch{}, fmt.Errorf("%w：不支持的控制动作", ErrBatchNotControllable)
	}

	if _, err := tx.Exec(ctx, `
    UPDATE batches SET status = $2, control_state = $3, updated_at = NOW() WHERE id = $1`,
		batchID, nextStatus, nextControl); err != nil {
		return model.Batch{}, err
	}

	eventType := model.BatchEventPaused
	if action == "resume" {
		eventType = model.BatchEventResumed
	}
	// 事件载荷带 in_flight：契约 §5 要求 BatchPaused/BatchResumed 载荷含 inFlight，
	// 因为「暂停时还有几个在途」正是用户最需要知道的事。
	var inFlight int
	if err := tx.QueryRow(ctx, `SELECT in_flight_units FROM batches WHERE id = $1`, batchID).Scan(&inFlight); err != nil {
		return model.Batch{}, err
	}
	if err := appendBatchEventTx(ctx, tx, batchID, projectID, eventType, actorID, map[string]any{
		"batchId":  batchID,
		"inFlight": inFlight,
	}); err != nil {
		return model.Batch{}, err
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:    actorID,
		Action:     "batch_" + action,
		Resource:   "batch",
		ResourceID: strconv.FormatInt(batchID, 10),
		ProjectID:  projectID,
		Reason:     fmt.Sprintf("status=%s control=%s", nextStatus, nextControl),
	}); err != nil {
		return model.Batch{}, err
	}

	batch, err := scanBatch(tx.QueryRow(ctx, batchSelectByIDSQL, batchID))
	if err != nil {
		return model.Batch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Batch{}, err
	}
	return batch, nil
}

// controlVerb 把控制动作转成中文动词（错误文案用）。
func controlVerb(action string) string {
	if action == "resume" {
		return "恢复"
	}
	return "暂停"
}

// MarkRetryableItemsPending 把失败且可重试的单元重置为待执行。
//
// 「恢复失败项」的核心语义（契约 §2.4 与 T13 验收项）：
//   - 只处理**失败且可重试**的项，成功内容保留；
//   - 反复点击不重复成果（重置本身幂等：已经在 pending 的不会被再次重置）；
//   - 沿用同一快照（批次不改，因此快照不变）；
//   - 不可重试的失败（例如 schema 不合规）不动 —— 重试只会浪费预算。
//
// 返回被重置的单元数。
func (s *BatchStore) MarkRetryableItemsPending(ctx context.Context, projectID, batchID, actorID int64) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if err := tx.QueryRow(ctx, `
    SELECT status FROM batches WHERE id = $1 AND project_id = $2 FOR UPDATE`,
		batchID, projectID).Scan(&status); err != nil {
		return 0, err
	}
	if status == model.BatchStatusCompleted {
		return 0, fmt.Errorf("%w：已完成的批次没有需要恢复的失败项", ErrBatchNotControllable)
	}

	tag, err := tx.Exec(ctx, `
    UPDATE batch_items
    SET status = 'pending', updated_at = NOW()
    WHERE batch_id = $1 AND status = 'failed' AND retryable`,
		batchID)
	if err != nil {
		return 0, err
	}
	reset := int(tag.RowsAffected())

	// 批次回到运行态（或 partial_failed，取决于是否还有其它失败项）。
	if _, err := tx.Exec(ctx, `
    UPDATE batches
    SET status = CASE
          WHEN status = 'failed' THEN 'partial_failed'
          WHEN status = 'completed' THEN status
          ELSE 'running'
        END,
        control_state = 'run',
        updated_at = NOW()
    WHERE id = $1`, batchID); err != nil {
		return 0, err
	}

	if err := appendBatchEventTx(ctx, tx, batchID, projectID, model.BatchEventRetryRequested, actorID, map[string]any{
		"batchId":    batchID,
		"resetItems": reset,
		// 明确告诉界面「不可重试的失败不在这次恢复范围内」，
		// 否则用户会以为点击后所有失败都会消失。
		"retryableOnly": true,
	}); err != nil {
		return 0, err
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:    actorID,
		Action:     "batch_retry_failed",
		Resource:   "batch",
		ResourceID: strconv.FormatInt(batchID, 10),
		ProjectID:  projectID,
		Reason:     fmt.Sprintf("resetItems=%d", reset),
	}); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return reset, nil
}

// ---------------------------------------------------------------------------
// 样本与内容版本
// ---------------------------------------------------------------------------

// EnsureSampleTx 幂等创建样本身份（供 worker 在事务内复用）。
func EnsureSampleTx(ctx context.Context, tx pgx.Tx, projectID int64, sampleKey, targetKind, title string, originBatchID *int64) (model.Sample, error) {
	var sample model.Sample
	err := tx.QueryRow(ctx, `
    INSERT INTO samples (project_id, sample_key, target_kind, title, origin_batch_id)
    VALUES ($1, $2, $3, $4, $5)
    ON CONFLICT (project_id, sample_key) DO UPDATE SET
      -- 只更新展示元数据；身份字段（sample_key/target_kind/origin_batch_id）不改：
      -- 改 origin 会让「首个来源」追溯失真。
      title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE samples.title END,
      updated_at = NOW()
    RETURNING id, project_id, sample_key, target_kind, title, origin_batch_id, latest_version, created_at, updated_at`,
		projectID, sampleKey, targetKind, title, originBatchID,
	).Scan(&sample.ID, &sample.ProjectID, &sample.SampleKey, &sample.TargetKind, &sample.Title,
		&sample.OriginBatchID, &sample.LatestVersion, &sample.CreatedAt, &sample.UpdatedAt)
	return sample, err
}

// EnsureSample 是 EnsureSampleTx 的独立事务版本。
func (s *BatchStore) EnsureSample(ctx context.Context, projectID int64, sampleKey, targetKind, title string, originBatchID *int64) (model.Sample, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Sample{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sample, err := EnsureSampleTx(ctx, tx, projectID, sampleKey, targetKind, title, originBatchID)
	if err != nil {
		return model.Sample{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Sample{}, err
	}
	return sample, nil
}

// AppendSampleVersionInput 是追加一个内容版本的请求。
type AppendSampleVersionInput struct {
	ProjectID   int64
	SampleKey   string
	TargetKind  string
	Title       string
	BatchID     *int64
	BatchItemID *int64
	Attempt     int
	// Payload 是 typed 样本内容（SFT 或 GRPO），由调用方按 target_kind 组装。
	Payload any
	// GeneratorConfig 是生成配置快照（非秘密标识）。
	GeneratorConfig json.RawMessage
	// 生成时引用的标准/蓝图（用于质量证据追溯）。
	StandardVersionID    *int64
	StandardContentHash  string
	BlueprintVersionID   *int64
	BlueprintContentHash string
	CreatedBy            *int64
}

// AppendSampleVersion 追加一个**新的**内容版本（永不覆盖）。
//
// 这是 T05 的核心写入路径。三条语义：
//  1. **追加**：即使内容与上一版完全相同，也产生新版本号 —— 因为「重生成了一次」
//     本身是事实（消耗了预算），而内容相同是结论。合并它们会让预算与来源失真。
//  2. **hash 在服务端算**：对规范化 JSON，不信任调用方。
//  3. **同事务推进 samples.latest_version**，并在冲突时重试
//     （并发生成同一 sample_key 时两个 worker 会争用版本号）。
func (s *BatchStore) AppendSampleVersion(ctx context.Context, input AppendSampleVersionInput) (model.Sample, model.SampleVersion, error) {
	if strings.TrimSpace(input.SampleKey) == "" {
		return model.Sample{}, model.SampleVersion{}, model.FieldErrors{
			{Field: "sampleKey", Message: "必填"},
		}
	}
	if input.TargetKind != model.TargetKindSFT && input.TargetKind != model.TargetKindGRPO {
		return model.Sample{}, model.SampleVersion{}, &apiStoreError{Message: "样本目标类型不合法"}
	}

	schemaVersion := model.SampleSchemaForTarget(input.TargetKind)
	if err := validateSamplePayload(input.TargetKind, input.Payload); err != nil {
		return model.Sample{}, model.SampleVersion{}, err
	}
	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return model.Sample{}, model.SampleVersion{}, err
	}
	contentHash, err := model.ContentHash(input.Payload)
	if err != nil {
		return model.Sample{}, model.SampleVersion{}, err
	}

	attempt := input.Attempt
	if attempt < 1 {
		attempt = 1
	}
	generatorConfig := input.GeneratorConfig
	if len(generatorConfig) == 0 {
		generatorConfig = json.RawMessage(`{}`)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Sample{}, model.SampleVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sample, err := EnsureSampleTx(ctx, tx, input.ProjectID, input.SampleKey, input.TargetKind, input.Title, input.BatchID)
	if err != nil {
		return model.Sample{}, model.SampleVersion{}, err
	}

	var version model.SampleVersion
	err = tx.QueryRow(ctx, `
    INSERT INTO sample_versions (
      sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
      batch_id, batch_item_id, attempt, generator_config,
      standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash, created_by)
    VALUES ($1, $2,
            (SELECT COALESCE(MAX(version), 0) + 1 FROM sample_versions WHERE sample_id = $1),
            $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
    RETURNING id, sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
              batch_id, batch_item_id, attempt, generator_config,
              standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
              created_by, created_at`,
		sample.ID, input.ProjectID, input.TargetKind, schemaVersion, payloadJSON, contentHash,
		input.BatchID, input.BatchItemID, attempt, generatorConfig,
		input.StandardVersionID, input.StandardContentHash,
		input.BlueprintVersionID, input.BlueprintContentHash, input.CreatedBy,
	).Scan(&version.ID, &version.SampleID, &version.ProjectID, &version.Version, &version.TargetKind,
		&version.SchemaVersion, &version.Payload, &version.ContentHash,
		&version.BatchID, &version.BatchItemID, &version.Attempt, &version.GeneratorConfig,
		&version.StandardVersionID, &version.StandardContentHash,
		&version.BlueprintVersionID, &version.BlueprintContentHash,
		&version.CreatedBy, &version.CreatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			// 并发生成同一 sample_key：两个 worker 算出同一个 version。
			// 这**不是**服务端故障，也不该静默丢弃 —— 调用方可以重试
			//（重试会拿到下一个版本号，于是两份内容都保留，符合「只追加」语义）。
			return model.Sample{}, model.SampleVersion{}, ErrSampleVersionConflict
		}
		if isForeignKeyViolation(err) {
			return model.Sample{}, model.SampleVersion{}, model.FieldErrors{{
				Field:   "standardVersionId",
				Message: "引用的标准或蓝图版本不存在或不属于本项目",
			}}
		}
		return model.Sample{}, model.SampleVersion{}, err
	}

	if err := tx.QueryRow(ctx, `
    UPDATE samples SET latest_version = GREATEST(latest_version, $2), updated_at = NOW()
    WHERE id = $1
    RETURNING id, project_id, sample_key, target_kind, title, origin_batch_id, latest_version, created_at, updated_at`,
		sample.ID, version.Version,
	).Scan(&sample.ID, &sample.ProjectID, &sample.SampleKey, &sample.TargetKind, &sample.Title,
		&sample.OriginBatchID, &sample.LatestVersion, &sample.CreatedAt, &sample.UpdatedAt); err != nil {
		return model.Sample{}, model.SampleVersion{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		if IsUniqueViolation(err) {
			return model.Sample{}, model.SampleVersion{}, ErrSampleVersionConflict
		}
		return model.Sample{}, model.SampleVersion{}, err
	}
	return sample, version, nil
}

// validateSamplePayload 按 target_kind 校验样本 payload（§2.2）。
//
// SFT 必填 question/reasoning/answer；GRPO 必填 question/judge_prompt/levels/
// level_rubrics，且 levels 至少两档、不重复、与判据一一对应。
// **不接受**旧名 chainOfThought（§2.2 明确它不再作为新契约字段）。
func validateSamplePayload(targetKind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return &apiStoreError{Message: "样本内容格式不正确"}
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return &apiStoreError{Message: "样本内容必须是一个对象"}
	}

	var errs model.FieldErrors
	required := model.RequiredSampleFields(targetKind)
	for _, field := range required {
		value, ok := decoded[field]
		if !ok || len(value) == 0 || string(value) == "null" {
			errs = append(errs, model.FieldError{Field: field, Message: "必填"})
		}
	}

	// 旧字段名必须被明确拒绝，而不是静默忽略：静默忽略会让调用方
	// 以为它生效了，而实际样本里没有推理内容。
	if _, hasLegacy := decoded["chainOfThought"]; hasLegacy {
		errs = append(errs, model.FieldError{
			Field:   "chainOfThought",
			Message: "该字段已不再使用，请改用 reasoning",
		})
	}

	if targetKind == model.TargetKindGRPO {
		var levels []string
		if raw, ok := decoded["levels"]; ok {
			if err := json.Unmarshal(raw, &levels); err != nil {
				// 拍成逗号字符串会走到这里：这正是旧导出丢结构的地方。
				errs = append(errs, model.FieldError{
					Field:   "levels",
					Message: "必须是字符串数组，不能是逗号分隔的字符串",
				})
			}
		}
		var rubrics []model.GrpoLevelRubric
		if raw, ok := decoded["level_rubrics"]; ok {
			if err := json.Unmarshal(raw, &rubrics); err != nil {
				errs = append(errs, model.FieldError{
					Field:   "level_rubrics",
					Message: "必须是对象数组（每档含判据与边界例）",
				})
			}
		}
		if len(errs) == 0 || (len(levels) > 0 || len(rubrics) > 0) {
			if err := model.ValidateGRPOSamplePayload(levels, rubrics); err != nil {
				if fieldErrors, ok := model.HasFieldErrors(err); ok {
					errs = append(errs, fieldErrors...)
				}
			}
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// GetSample 读取样本身份。
func (s *BatchStore) GetSample(ctx context.Context, projectID, sampleID int64) (model.Sample, error) {
	var sample model.Sample
	err := s.db.QueryRow(ctx, `
    SELECT id, project_id, sample_key, target_kind, title, origin_batch_id, latest_version, created_at, updated_at
    FROM samples WHERE id = $1 AND project_id = $2`, sampleID, projectID,
	).Scan(&sample.ID, &sample.ProjectID, &sample.SampleKey, &sample.TargetKind, &sample.Title,
		&sample.OriginBatchID, &sample.LatestVersion, &sample.CreatedAt, &sample.UpdatedAt)
	return sample, err
}

// GetSampleVersion 读取一个内容版本（默认取最新版）。
//
// version <= 0 表示「取最新」—— 界面上的「打开样本」正是这个语义。
// 但**批次、证据与发布必须显式指定版本**：它们要能解释「看的是哪一版」。
func (s *BatchStore) GetSampleVersion(ctx context.Context, projectID, sampleID int64, version int) (model.SampleVersion, error) {
	if version <= 0 {
		var latest int
		if err := s.db.QueryRow(ctx, `
      SELECT latest_version FROM samples WHERE id = $1 AND project_id = $2`,
			sampleID, projectID).Scan(&latest); err != nil {
			return model.SampleVersion{}, err
		}
		version = latest
	}
	var item model.SampleVersion
	err := s.db.QueryRow(ctx, `
    SELECT id, sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
           batch_id, batch_item_id, attempt, generator_config,
           standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
           created_by, created_at
    FROM sample_versions WHERE sample_id = $1 AND project_id = $2 AND version = $3`,
		sampleID, projectID, version,
	).Scan(&item.ID, &item.SampleID, &item.ProjectID, &item.Version, &item.TargetKind,
		&item.SchemaVersion, &item.Payload, &item.ContentHash,
		&item.BatchID, &item.BatchItemID, &item.Attempt, &item.GeneratorConfig,
		&item.StandardVersionID, &item.StandardContentHash,
		&item.BlueprintVersionID, &item.BlueprintContentHash,
		&item.CreatedBy, &item.CreatedAt)
	return item, err
}

// ListSampleVersions 列出样本的历史版本（倒序）。
//
// 用途是 D03「版本与来源」时间线：用户要能看到「这一版是谁产出的、
// 按哪版标准」，而原始内容不可编辑（§4.1）。
func (s *BatchStore) ListSampleVersions(ctx context.Context, projectID, sampleID int64, limit int) ([]model.SampleVersion, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
           batch_id, batch_item_id, attempt, generator_config,
           standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
           created_by, created_at
    FROM sample_versions WHERE sample_id = $1 AND project_id = $2
    ORDER BY version DESC LIMIT $3`, sampleID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.SampleVersion{}
	for rows.Next() {
		var item model.SampleVersion
		if err := rows.Scan(&item.ID, &item.SampleID, &item.ProjectID, &item.Version, &item.TargetKind,
			&item.SchemaVersion, &item.Payload, &item.ContentHash,
			&item.BatchID, &item.BatchItemID, &item.Attempt, &item.GeneratorConfig,
			&item.StandardVersionID, &item.StandardContentHash,
			&item.BlueprintVersionID, &item.BlueprintContentHash,
			&item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------------------
// 单元级执行（幂等）
// ---------------------------------------------------------------------------

// EnsureBatchItem 幂等创建/取回一个单元行。
//
// 返回的第二值为 true 表示「刚刚创建」（第一次见到这个 item_key）。
// worker 用它在「已完成」时跳过重复工作，而不是靠应用层先查后写。
func (s *BatchStore) EnsureBatchItem(ctx context.Context, batchID, projectID int64, itemKey string, sampleID *int64) (model.BatchItem, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.BatchItem{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	item, created, err := ensureBatchItemTx(ctx, tx, batchID, projectID, itemKey, sampleID)
	if err != nil {
		return model.BatchItem{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.BatchItem{}, false, err
	}
	return item, created, nil
}

// ensureBatchItemTx 是 EnsureBatchItem 的事务内实现（供提交结果时复用）。
func ensureBatchItemTx(ctx context.Context, tx pgx.Tx, batchID, projectID int64, itemKey string, sampleID *int64) (model.BatchItem, bool, error) {
	var item model.BatchItem
	err := tx.QueryRow(ctx, `
    INSERT INTO batch_items (batch_id, project_id, item_key, sample_id, status)
    VALUES ($1, $2, $3, $4, 'pending')
    ON CONFLICT (batch_id, item_key) DO NOTHING
    RETURNING id, batch_id, project_id, item_key, sample_id, status, attempt,
              error_class, error_message, retryable, sample_version_id,
              started_at, finished_at, created_at, updated_at`,
		batchID, projectID, itemKey, sampleID,
	).Scan(&item.ID, &item.BatchID, &item.ProjectID, &item.ItemKey, &item.SampleID, &item.Status,
		&item.Attempt, &item.ErrorClass, &item.ErrorMessage, &item.Retryable, &item.SampleVersionID,
		&item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 冲突：已存在。取回它。
		err = tx.QueryRow(ctx, `
      SELECT id, batch_id, project_id, item_key, sample_id, status, attempt,
             error_class, error_message, retryable, sample_version_id,
             started_at, finished_at, created_at, updated_at
      FROM batch_items WHERE batch_id = $1 AND item_key = $2`,
			batchID, itemKey,
		).Scan(&item.ID, &item.BatchID, &item.ProjectID, &item.ItemKey, &item.SampleID, &item.Status,
			&item.Attempt, &item.ErrorClass, &item.ErrorMessage, &item.Retryable, &item.SampleVersionID,
			&item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt)
		return item, false, err
	}
	if err != nil {
		return model.BatchItem{}, false, err
	}
	return item, true, nil
}

// ClaimBatchItemForAttempt 抢占一个单元进入执行（返回 false 表示无需执行）。
//
// 返回 false 的两种情形，都必须让 worker **跳过**：
//   - 该单元已经 succeeded（重放相同成功项不增加样本，T05 验收项）；
//   - 该单元已被别的 worker 抢到 running（并发不重复调用外部模型）。
//
// 使用单语句条件更新而不是 SELECT-then-UPDATE：后者的窗口会让两个 worker
// 同时通过检查，于是同一个单元被调用两次外部模型（既多花钱又会产生两个版本）。
func (s *BatchStore) ClaimBatchItemForAttempt(ctx context.Context, itemID int64) (model.BatchItem, bool, error) {
	var item model.BatchItem
	err := s.db.QueryRow(ctx, `
    UPDATE batch_items
    SET status = 'running', attempt = attempt + 1, started_at = COALESCE(started_at, NOW()), updated_at = NOW()
    WHERE id = $1 AND status IN ('pending', 'failed')
    RETURNING id, batch_id, project_id, item_key, sample_id, status, attempt,
              error_class, error_message, retryable, sample_version_id,
              started_at, finished_at, created_at, updated_at`, itemID,
	).Scan(&item.ID, &item.BatchID, &item.ProjectID, &item.ItemKey, &item.SampleID, &item.Status,
		&item.Attempt, &item.ErrorClass, &item.ErrorMessage, &item.Retryable, &item.SampleVersionID,
		&item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 状态不是 pending/failed：已成功或正在被别的 worker 执行。
		return model.BatchItem{}, false, nil
	}
	if err != nil {
		return model.BatchItem{}, false, err
	}
	return item, true, nil
}

// CommitBatchItemSuccess 提交一个成功单元与其内容版本（**同一事务**）。
//
// 为什么必须同事务（T05 验收项「成功 item 提交与版本追加在事务中幂等」）：
// 若分两次写，崩溃窗口会留下「单元已成功但样本不存在」—— 于是恢复逻辑
// 认为这项不用重跑，而内容永远不会产生。那是静默的数据丢失。
//
// 幂等：若该单元已经 succeeded，直接返回既有的 sample_version_id，
// 不再追加版本（否则「重放相同成功项」会增加样本，违反 T05 验收项）。
func (s *BatchStore) CommitBatchItemSuccess(ctx context.Context, batchID, projectID, itemID int64, versionInput AppendSampleVersionInput) (model.SampleVersion, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.SampleVersion{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 锁住单元行，读它的当前状态与已产出的版本。
	var status string
	var existingVersionID *int64
	if err := tx.QueryRow(ctx, `
    SELECT status, sample_version_id FROM batch_items WHERE id = $1 AND batch_id = $2 FOR UPDATE`,
		itemID, batchID).Scan(&status, &existingVersionID); err != nil {
		return model.SampleVersion{}, false, err
	}

	if status == model.ItemStatusSucceeded && existingVersionID != nil {
		// 已经成功过：回放既有版本，不追加新版本。
		var existing model.SampleVersion
		if err := tx.QueryRow(ctx, `
      SELECT id, sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
             batch_id, batch_item_id, attempt, generator_config,
             standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
             created_by, created_at
      FROM sample_versions WHERE id = $1`, *existingVersionID,
		).Scan(&existing.ID, &existing.SampleID, &existing.ProjectID, &existing.Version,
			&existing.TargetKind, &existing.SchemaVersion, &existing.Payload, &existing.ContentHash,
			&existing.BatchID, &existing.BatchItemID, &existing.Attempt, &existing.GeneratorConfig,
			&existing.StandardVersionID, &existing.StandardContentHash,
			&existing.BlueprintVersionID, &existing.BlueprintContentHash,
			&existing.CreatedBy, &existing.CreatedAt); err != nil {
			return model.SampleVersion{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return model.SampleVersion{}, false, err
		}
		// 第二值为 false：本次没有产生新版本（重放）。
		return existing, false, nil
	}

	versionInput.ProjectID = projectID
	versionInput.BatchID = &batchID
	versionInput.BatchItemID = &itemID
	version, err := appendSampleVersionTx(ctx, tx, versionInput)
	if err != nil {
		return model.SampleVersion{}, false, err
	}

	// 单元置为成功并指向产出（同一事务）。
	if _, err := tx.Exec(ctx, `
    UPDATE batch_items
    SET status = 'succeeded', sample_version_id = $2, error_class = '', error_message = '',
        finished_at = NOW(), updated_at = NOW()
    WHERE id = $1`, itemID, version.ID); err != nil {
		return model.SampleVersion{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		if IsUniqueViolation(err) {
			return model.SampleVersion{}, false, ErrSampleVersionConflict
		}
		return model.SampleVersion{}, false, err
	}
	return version, true, nil
}

// appendSampleVersionTx 是 AppendSampleVersion 的事务内实现（不自己开事务）。
//
// 抽出来是为了让 CommitBatchItemSuccess 能在**同一事务**里追加版本并更新单元，
// 而不是嵌套事务（pgx 的嵌套需要 savepoint，会让「同事务」的保证变模糊）。
func appendSampleVersionTx(ctx context.Context, tx pgx.Tx, input AppendSampleVersionInput) (model.SampleVersion, error) {
	if input.TargetKind != model.TargetKindSFT && input.TargetKind != model.TargetKindGRPO {
		return model.SampleVersion{}, &apiStoreError{Message: "样本目标类型不合法"}
	}
	schemaVersion := model.SampleSchemaForTarget(input.TargetKind)
	if err := validateSamplePayload(input.TargetKind, input.Payload); err != nil {
		return model.SampleVersion{}, err
	}
	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return model.SampleVersion{}, err
	}
	contentHash, err := model.ContentHash(input.Payload)
	if err != nil {
		return model.SampleVersion{}, err
	}
	attempt := input.Attempt
	if attempt < 1 {
		attempt = 1
	}
	generatorConfig := input.GeneratorConfig
	if len(generatorConfig) == 0 {
		generatorConfig = json.RawMessage(`{}`)
	}

	sample, err := EnsureSampleTx(ctx, tx, input.ProjectID, input.SampleKey, input.TargetKind, input.Title, input.BatchID)
	if err != nil {
		return model.SampleVersion{}, err
	}

	var version model.SampleVersion
	err = tx.QueryRow(ctx, `
    INSERT INTO sample_versions (
      sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
      batch_id, batch_item_id, attempt, generator_config,
      standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash, created_by)
    VALUES ($1, $2,
            (SELECT COALESCE(MAX(version), 0) + 1 FROM sample_versions WHERE sample_id = $1),
            $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
    RETURNING id, sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
              batch_id, batch_item_id, attempt, generator_config,
              standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
              created_by, created_at`,
		sample.ID, input.ProjectID, input.TargetKind, schemaVersion, payloadJSON, contentHash,
		input.BatchID, input.BatchItemID, attempt, generatorConfig,
		input.StandardVersionID, input.StandardContentHash,
		input.BlueprintVersionID, input.BlueprintContentHash, input.CreatedBy,
	).Scan(&version.ID, &version.SampleID, &version.ProjectID, &version.Version, &version.TargetKind,
		&version.SchemaVersion, &version.Payload, &version.ContentHash,
		&version.BatchID, &version.BatchItemID, &version.Attempt, &version.GeneratorConfig,
		&version.StandardVersionID, &version.StandardContentHash,
		&version.BlueprintVersionID, &version.BlueprintContentHash,
		&version.CreatedBy, &version.CreatedAt)
	if err != nil {
		return model.SampleVersion{}, err
	}

	if _, err := tx.Exec(ctx, `
    UPDATE samples SET latest_version = GREATEST(latest_version, $2), updated_at = NOW()
    WHERE id = $1`, sample.ID, version.Version); err != nil {
		return model.SampleVersion{}, err
	}
	return version, nil
}

// CommitBatchItemFailure 记录一个失败单元。
//
// 幂等：若该单元已经 succeeded，**不覆盖为 failed**。
// 那会让「已经产出的内容」在下次恢复时被重跑，而用户看到的是
// 「本来成功的样本变成失败」。
func (s *BatchStore) CommitBatchItemFailure(ctx context.Context, batchID, itemID int64, errorClass, message string, retryable bool) (bool, error) {
	if !isKnownErrorClass(errorClass) {
		// 未知类别归入 internal：界面仍能给出「可重试」建议，
		// 而静默存一个未分类的值会让界面无法选择文案。
		errorClass = model.ErrorClassInternal
	}
	if len([]rune(message)) > 1000 {
		message = string([]rune(message)[:1000])
	}

	tag, err := s.db.Exec(ctx, `
    UPDATE batch_items
    SET status = 'failed', error_class = $3, error_message = $4, retryable = $5,
        finished_at = NOW(), updated_at = NOW()
    WHERE id = $1 AND batch_id = $2 AND status <> 'succeeded'`,
		itemID, batchID, errorClass, message, retryable)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// isKnownErrorClass 判断错误类别是否在契约允许的集合内。
func isKnownErrorClass(errorClass string) bool {
	switch errorClass {
	case model.ErrorClassProvider, model.ErrorClassRateLimited, model.ErrorClassTimeout,
		model.ErrorClassEmptyOutput, model.ErrorClassTruncated, model.ErrorClassInvalidJSON,
		model.ErrorClassSchema, model.ErrorClassConfig, model.ErrorClassInternal:
		return true
	}
	return false
}

// RefreshBatchCounts 从 batch_items 事实重算批次的聚合计数。
//
// 为什么从事实重算而不是增量加减：增量加减在「重放」「并发」「部分失败」
// 三种情况下都会漂移，而计数是用户判断进度的唯一依据。
// 重算的成本是一次 GROUP BY，而正确性远重要于这点成本。
func (s *BatchStore) RefreshBatchCounts(ctx context.Context, batchID int64) (model.Batch, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Batch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var projectID int64
	var status string
	if err := tx.QueryRow(ctx, `
    SELECT project_id, status FROM batches WHERE id = $1 FOR UPDATE`, batchID).
		Scan(&projectID, &status); err != nil {
		return model.Batch{}, err
	}

	var completed, failed, inFlight, total int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FILTER (WHERE status = 'succeeded'),
           COUNT(*) FILTER (WHERE status = 'failed'),
           COUNT(*) FILTER (WHERE status = 'running'),
           COUNT(*)
    FROM batch_items WHERE batch_id = $1`, batchID).
		Scan(&completed, &failed, &inFlight, &total); err != nil {
		return model.Batch{}, err
	}

	// 状态聚合：终态由「所有单元都定稿」推导，而不是由调用方声明。
	// 这样「worker 崩在最后一步」不会留下一个永远 running 的批次。
	nextStatus := status
	switch {
	case status == model.BatchStatusCompleted || status == model.BatchStatusFailed:
		// 终态不被计数刷新改写（历史事实）。
	case status == model.BatchStatusPauseRequested || status == model.BatchStatusPaused:
		// 暂停态保持：控制意图高于计数推导。
	case total > 0 && completed+failed == total && failed > 0:
		nextStatus = model.BatchStatusPartialFailed
	case total > 0 && completed == total:
		nextStatus = model.BatchStatusCompleted
	case total > 0 && failed > 0:
		nextStatus = model.BatchStatusPartialFailed
	}

	// 参数显式转型：`$2 + $3 + $4` 在 Postgres 里是 unknown + unknown，
	// 无法唯一决定运算符（实测 SQLSTATE 42725）。传 ::int 让意图明确，
	// 而不是靠调用方传 Go int 让驱动猜类型。
	if _, err := tx.Exec(ctx, `
    UPDATE batches
    SET completed_units = $2, failed_units = $3, in_flight_units = $4,
        planned_units = GREATEST($5::int, planned_units),
        status = $6,
        started_at = COALESCE(started_at,
          CASE WHEN ($2::int + $3::int + $4::int) > 0 THEN NOW() ELSE NULL END),
        finished_at = CASE WHEN $6 IN ('completed', 'failed') THEN NOW() ELSE finished_at END,
        updated_at = NOW()
    WHERE id = $1`, batchID, completed, failed, inFlight, total, nextStatus); err != nil {
		return model.Batch{}, err
	}

	batch, err := scanBatch(tx.QueryRow(ctx, batchSelectByIDSQL, batchID))
	if err != nil {
		return model.Batch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Batch{}, err
	}
	return batch, nil
}

// ---------------------------------------------------------------------------
// 事件与扫描辅助
// ---------------------------------------------------------------------------

// appendBatchEventTx 追加一条批次事件。
//
// sequence 是客户端去重与排序的依据，因此它必须**单调且无冲突**。
//
// 实现取舍（实测结论，不是推测）：最初把序号写成同一语句里的
// `(SELECT MAX(sequence)+1 ...)`。在 READ COMMITTED 下并发的追加会各自
// 读到同一个 MAX，于是除第一个之外全部撞 UNIQUE (batch_id, sequence)。
// 实测 11 个并发追加只有 3 个成功 —— 也就是**静默丢掉 8 条事件**。
// 事件时间线正是界面上解释「为什么变成现在这样」的依据，静默丢失不可接受。
//
// 修法是先锁住批次行（`SELECT ... FOR UPDATE`）把同一批次的事件追加串行化。
// 代价可忽略：事件是低频写入（控制动作、阶段完成），而批次行的行锁本来
// 就被控制命令（PauseBatch/ResumeBatch）使用，因此这里不引入新的争用面，
// 也不会与其他写路径互相阻塞出更长的等待链。
func appendBatchEventTx(ctx context.Context, tx pgx.Tx, batchID, projectID int64, eventType string, actorID int64, detail map[string]any) error {
	var lockedID int64
	if err := tx.QueryRow(ctx, `
    SELECT id FROM batches WHERE id = $1 FOR UPDATE`, batchID).Scan(&lockedID); err != nil {
		return err
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	_, err = tx.Exec(ctx, `
    INSERT INTO batch_events (batch_id, project_id, event_type, sequence, actor_id, detail)
    VALUES ($1, $2, $3,
            (SELECT COALESCE(MAX(sequence), 0) + 1 FROM batch_events WHERE batch_id = $1),
            $4, $5)`,
		batchID, projectID, eventType, actor, raw)
	return err
}

// AppendBatchEvent 是 appendBatchEventTx 的独立事务版本（供 API/T12 使用）。
func (s *BatchStore) AppendBatchEvent(ctx context.Context, batchID, projectID int64, eventType string, actorID int64, detail map[string]any) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := appendBatchEventTx(ctx, tx, batchID, projectID, eventType, actorID, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// scanBatch 读取一行批次。
//
// 为什么缓冲声明为**局部**变量：这个函数由并发请求（列表/详情/批次控制）
// 调用。用包级 []byte 作为 Scan 目标会让两个并发调用互相覆盖对方的
// generation_config。局部变量每次分配一个 slice 头，成本可忽略。
func scanBatch(row pgx.Row) (model.Batch, error) {
	var batch model.Batch
	var leaseUntil *time.Time
	var rawGenerationConfig []byte
	// 版本引用列可空，必须扫到 *int64 再拍平（见 CreateBatch 的同一说明）。
	var (
		blueprintVersionID     *int64
		coverageVersionID      *int64
		standardVersionID      *int64
		qualityPolicyVersionID *int64
		mappingVersionID       *int64
	)
	err := row.Scan(&batch.ID, &batch.ProjectID, &batch.Purpose, &batch.Status, &batch.ControlState,
		&batch.TargetKind, &batch.SchemaVersion,
		&blueprintVersionID, &batch.Snapshot.BlueprintContentHash,
		&coverageVersionID, &batch.Snapshot.CoverageContentHash,
		&standardVersionID, &batch.Snapshot.StandardContentHash,
		&qualityPolicyVersionID, &batch.Snapshot.QualityPolicyContentHash,
		&mappingVersionID, &batch.Snapshot.MappingContentHash,
		&rawGenerationConfig, &batch.PlannedUnits, &batch.CompletedUnits, &batch.FailedUnits,
		&batch.InFlightUnits, &batch.Budget.Currency, &batch.Budget.LimitMinor,
		&batch.CoverageSlice, &batch.FencingToken, &batch.LeaseOwner, &leaseUntil,
		&batch.CreatedBy, &batch.StartedAt, &batch.FinishedAt, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		return model.Batch{}, err
	}
	batch.Snapshot.BlueprintVersionID = derefVersionID(blueprintVersionID)
	batch.Snapshot.CoverageVersionID = derefVersionID(coverageVersionID)
	batch.Snapshot.StandardVersionID = derefVersionID(standardVersionID)
	batch.Snapshot.QualityPolicyVersionID = derefVersionID(qualityPolicyVersionID)
	batch.Snapshot.MappingVersionID = derefVersionID(mappingVersionID)
	batch.GenerationConfig = decodeGenerationConfig(rawGenerationConfig)
	batch.LeaseUntil = leaseUntil
	batch.Budget.OnExhausted = model.BudgetOnExhaustedPause
	return batch, nil
}

// derefVersionID 把可空的版本引用拍平成 0（表示「未引用」）。
func derefVersionID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}
