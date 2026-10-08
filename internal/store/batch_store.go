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
// CreateBatch 创建一个批次（独立事务）。
func (s *BatchStore) CreateBatch(ctx context.Context, projectID, actorID int64, targetKind string, input model.CreateBatchInput) (model.Batch, error) {
	batch, _, err := s.CreateBatchWithJob(ctx, projectID, actorID, targetKind, input, nil)
	return batch, err
}

// CreateImportedSnapshotBatch 创建一个由旧系统内容组成的快照批次。
//
// 导入批次没有生成配置，也没有蓝图版本：它记录的是已经存在的内容，
// 不是等待 worker 执行的生产任务。普通 CreateBatch* 路径拒绝这类输入，
// 迁移必须通过这个显式入口保留「无生成过程」这一事实。
func (s *BatchStore) CreateImportedSnapshotBatch(ctx context.Context, projectID, actorID int64, targetKind string, input model.CreateBatchInput) (model.Batch, error) {
	batch, _, err := s.createBatchWithJob(ctx, projectID, actorID, targetKind, input, nil, false)
	return batch, err
}

// CreateBatchWithJob 在**同一事务**内创建批次并创建作业（Issue #160 T06/T08）。
//
// 为什么必须同事务（T06 验收项「API 落库后、派发前崩溃不丢任务」）：
// 分两次写会留下两种坏状态 ——
//   - 批次写了但作业没写：用户看到「已排队」，实际永远不会被执行；
//   - 作业写了但批次没写：worker 去处理一个不存在的批次。
//
// job 为 nil 时退化为「只建批次不派发」，供只做设计验证的场景使用。
func (s *BatchStore) CreateBatchWithJob(ctx context.Context, projectID, actorID int64, targetKind string, input model.CreateBatchInput, job *EnqueueJobInput) (model.Batch, *model.Job, error) {
	return s.createBatchWithJob(ctx, projectID, actorID, targetKind, input, job, true)
}

func (s *BatchStore) createBatchWithJob(ctx context.Context, projectID, actorID int64, targetKind string, input model.CreateBatchInput, job *EnqueueJobInput, requireExecutionConfig bool) (model.Batch, *model.Job, error) {
	input.Normalize()
	if err := input.Validate(); err != nil {
		return model.Batch{}, nil, err
	}
	if targetKind != model.TargetKindSFT && targetKind != model.TargetKindGRPO {
		return model.Batch{}, nil, &apiStoreError{Message: "项目目标类型不合法"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Batch{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot, generationConfig, err := resolveBatchSnapshotTx(ctx, tx, projectID, input)
	if err != nil {
		return model.Batch{}, nil, err
	}
	// issue #190：计划量必须与覆盖矩阵**实际可产出量**比较。
	// 以前两者互不校对，于是「计划 12、可产出 1」的批次可以直接入队，
	// 跑完 1 个单元就置 completed —— 静默少交付比直接失败更危险。
	if err := validateBatchCapacityTx(ctx, tx, projectID, input, snapshot); err != nil {
		return model.Batch{}, nil, err
	}
	generationConfig.SchemaVersion = model.SampleSchemaForTarget(targetKind)
	if requireExecutionConfig {
		if err := validateBatchExecutionSnapshot(input, generationConfig); err != nil {
			return model.Batch{}, nil, err
		}
	}

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
              budget_currency, budget_limit_minor,
              budget_reserved_minor, budget_settled_minor, budget_uncertain_minor,
              coverage_slice, fencing_token, lease_owner, lease_until,
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
		&batch.BudgetReservedMinor, &batch.BudgetSettledMinor, &batch.BudgetUncertainMinor,
		&batch.CoverageSlice, &batch.FencingToken, &batch.LeaseOwner, &leaseUntil,
		&batch.CreatedBy, &batch.StartedAt, &batch.FinishedAt, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return model.Batch{}, nil, model.FieldErrors{{
				Field:   "blueprintVersionId",
				Message: "引用的文档版本不存在或不属于本项目，请重新选择方案版本",
			}}
		}
		return model.Batch{}, nil, err
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
		return model.Batch{}, nil, err
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:    actorID,
		Action:     "batch_create",
		Resource:   "batch",
		ResourceID: strconv.FormatInt(batch.ID, 10),
		ProjectID:  projectID,
		Reason:     fmt.Sprintf("purpose=%s units=%d", input.Purpose, input.UnitCount),
	}); err != nil {
		return model.Batch{}, nil, err
	}

	var createdJob *model.Job
	if job != nil {
		// 作业的作用域必须由服务端填，不接受调用方传入的批次 ID：
		// 否则一次笔误就能让作业指向别的批次（进而把内容写进别的项目）。
		job.BatchID = &batch.ID
		job.ProjectID = &projectID
		if job.CreatedBy == nil {
			job.CreatedBy = &actorID
		}
		enqueued, _, err := EnqueueJobTx(ctx, tx, *job)
		if err != nil {
			return model.Batch{}, nil, err
		}
		createdJob = &enqueued
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Batch{}, nil, err
	}
	return batch, createdJob, nil
}

// validateBatchCapacityTx 是 issue #190 的入口校验：
// `plannedUnits > 覆盖矩阵可产出量` 时直接 422，而不是静默少交付。
//
// 为什么放在「快照解析之后、INSERT 之前」：此时覆盖版本的 payload 已经可读，
// 而失败不会写入 batches/events/jobs —— 用户看到的是可操作的拒绝，
// 而不是一个已经花了钱的空批次。
func validateBatchCapacityTx(ctx context.Context, tx pgx.Tx, projectID int64, input model.CreateBatchInput, snapshot model.BatchSnapshot) error {
	if snapshot.CoverageVersionID <= 0 {
		// 没有覆盖版本时 unit_key 退化成 `unit/default#n`，容量等于计划量本身，
		// 因此不存在缺口。
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `
    SELECT payload FROM document_versions WHERE id = $1 AND project_id = $2`,
		snapshot.CoverageVersionID, projectID).Scan(&raw); err != nil {
		return err
	}
	var coverage model.CoveragePayload
	if err := json.Unmarshal(raw, &coverage); err != nil {
		return model.FieldErrors{{
			Field:   "coverageVersionId",
			Message: "覆盖版本内容无法解析，请重新保存覆盖方案后再启动批次",
		}}
	}
	capacity := model.CoverageCapacity(coverage)
	if capacity <= 0 {
		return model.FieldErrors{{
			Field:   "coverageVersionId",
			Message: "当前覆盖方案没有任何方向配额，最多产出 0 个单元；请先在覆盖矩阵里补充领域/方向与配额",
		}}
	}
	if input.UnitCount > capacity {
		return model.FieldErrors{{
			Field: "unitCount",
			Message: fmt.Sprintf(
				"计划单元数 %d 超过当前覆盖矩阵的可产出量 %d；请把计划量改为不超过 %d，或在覆盖矩阵里增加方向/配额",
				input.UnitCount, capacity, capacity),
		}}
	}
	return nil
}

// validateBatchExecutionSnapshot 是进入 queued 前的最终边界。
//
// 蓝图保存允许草稿节点暂时为空，但生产批次不能冻结一个无法执行的
// generation_config。校验放在快照解析完成后、INSERT 前，因而失败时不会
// 写入 batches、events 或 jobs。
func validateBatchExecutionSnapshot(input model.CreateBatchInput, config model.BatchGenerationConfig) error {
	if input.BlueprintVersionID <= 0 {
		return model.FieldErrors{{
			Field:   "blueprintVersionId",
			Message: "启动批次前必须选择一个已保存的蓝图版本",
		}}
	}

	var errs model.FieldErrors
	if config.ModelConnectionID <= 0 {
		errs = append(errs, model.FieldError{
			Field:   "generationConfig.modelConnectionId",
			Message: "蓝图的生成步骤必须选择模型连接，请先打开设计页的生成节点",
		})
	}
	if config.Concurrency < model.MinGenerationConcurrency || config.Concurrency > model.MaxGenerationConcurrency {
		errs = append(errs, model.FieldError{
			Field:   "generationConfig.concurrency",
			Message: fmt.Sprintf("必须在 %d–%d 之间", model.MinGenerationConcurrency, model.MaxGenerationConcurrency),
		})
	}
	if config.MaxTokens < 0 || (config.MaxTokens > 0 && config.MaxTokens < model.GenerationMinTokens) {
		errs = append(errs, model.FieldError{
			Field:   "generationConfig.maxTokens",
			Message: fmt.Sprintf("必须为 0（使用连接默认）或不小于 %d", model.GenerationMinTokens),
		})
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// resolveBatchSnapshotTx 读取被引用版本的 hash，组装快照。
//
// 只接受**同项目**的版本行：跨项目引用在迁移 0025 里由复合外键强制，
// 但这里先查一次是为了给出可读的字段错误（外键报错只有 SQLSTATE）。
func resolveBatchSnapshotTx(ctx context.Context, tx pgx.Tx, projectID int64, input model.CreateBatchInput) (model.BatchSnapshot, model.BatchGenerationConfig, error) {
	var snapshot model.BatchSnapshot
	var generationConfig model.BatchGenerationConfig

	// 蓝图是生产配置的组合入口。规划页可以显式覆写某一类版本（例如
	// 扩量采用同一蓝图但切换映射），但省略的引用必须从蓝图节点补齐，
	// 否则用户在设计区明明已经配置，生产快照却会丢掉这条关系。
	if input.BlueprintVersionID > 0 {
		var rawBlueprint []byte
		if err := tx.QueryRow(ctx, `
      SELECT payload FROM document_versions
      WHERE id = $1 AND project_id = $2 AND kind = 'blueprint'`,
			input.BlueprintVersionID, projectID).Scan(&rawBlueprint); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return snapshot, generationConfig, model.FieldErrors{{
					Field: "blueprintVersionId", Message: "引用的蓝图版本不存在或不属于本项目，请重新选择",
				}}
			}
			return snapshot, generationConfig, err
		}
		var blueprint model.BlueprintPayload
		if err := json.Unmarshal(rawBlueprint, &blueprint); err == nil {
			if input.CoverageVersionID <= 0 {
				input.CoverageVersionID = blueprint.Nodes.Coverage.CoverageVersionID
			}
			if input.StandardVersionID <= 0 {
				input.StandardVersionID = blueprint.Nodes.Standard.StandardVersionID
			}
			if input.QualityPolicyVersionID <= 0 {
				input.QualityPolicyVersionID = blueprint.Nodes.Rules.QualityPolicyVersionID
			}
			if input.MappingVersionID <= 0 {
				input.MappingVersionID = blueprint.Nodes.Delivery.MappingVersionID
			}
		}
	}

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
	// 蓝图版本缺省时保持零值：真正的「必须配齐」检查在执行前核对里。
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
  budget_currency, budget_limit_minor,
  budget_reserved_minor, budget_settled_minor, budget_uncertain_minor,
  coverage_slice, fencing_token, lease_owner, lease_until,
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
    budget_currency, budget_limit_minor,
    budget_reserved_minor, budget_settled_minor, budget_uncertain_minor,
    coverage_slice, fencing_token, lease_owner, lease_until,
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
      budget_currency, budget_limit_minor,
      budget_reserved_minor, budget_settled_minor, budget_uncertain_minor,
      coverage_slice, fencing_token, lease_owner, lease_until,
      created_by, started_at, finished_at, created_at, updated_at
    FROM batches
    WHERE project_id = $1
      AND ($2 = '' OR purpose = $2)
      AND ($3 = '' OR status = $3)
      AND ($4::timestamptz IS NULL OR (created_at, id) < ($4::timestamptz, $5::bigint))
    ORDER BY created_at DESC, id DESC
    LIMIT $6`

// ListDivergentBatchIDs 返回「状态与单元事实不符」的批次 ID。
//
// 为什么需要它（issue #201/#202）：收敛逻辑只在 `RefreshBatchCounts` 被调用时生效，
// 而它此前只有两个调用点 —— runner 跑完、以及控制命令。一个已经跑完的历史批次
// （b_1 永远是 running、b_2 永远是 completed+缺口）**没有任何路径**会再次触发它，
// 于是修复只对未来的批次有效，已存在的矛盾状态永久保留。
//
// 本查询把「哪几条需要重算」变成可扫描的事实，由 worker 的维护循环定期调用。
// 只用**已落库的列**判定，不回表算 batch_items：维护循环每 30 秒跑一次，
// 让它随批次数量增长而变慢会拖垮整个 worker。两种形态各自都极小：
//   - `completed` 但 `completed_units < planned_units`（#201 的静默少交付）；
//   - `running` 但既无在途/待执行单元、也无活作业（#202 的僵尸）。
//
// 第二个条件里的「无活作业」与 `RefreshBatchCounts` 里的 activeJobs 判定
// **必须一致**：否则会把一个刚被派发、还没建单元的正在跑的批次误判为僵尸
// （维护循环空转），或者把真正的僵尸判成「正在跑」而永远不收。
func (s *BatchStore) ListDivergentBatchIDs(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id FROM batches
    WHERE (status = 'completed' AND completed_units < planned_units)
       OR (status = 'running'
             AND NOT EXISTS (
               SELECT 1 FROM batch_items i
               WHERE i.batch_id = batches.id AND i.status IN ('pending', 'running'))
             AND NOT EXISTS (
               SELECT 1 FROM jobs j
               WHERE j.batch_id = batches.id AND j.job_kind = $2
                 AND j.status IN ('pending', 'leased', 'running')))
    ORDER BY id
    LIMIT $1`, limit, model.JobKindBatchGenerate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListBatchIDsWithStaleSteps 返回「阶段投影缺失或落后」的批次 ID。
//
// 为什么需要它（issue #212）：阶段投影只在 runner 跑完时落盘，而 runner 碰不到
// 已经跑完的历史批次 —— 因此只修 runner 并不能修好实测的那条（b_4 已完成、
// batch_steps 仍然 0 行）。把「哪几条需要补写阶段行」变成可扫描的事实，
// 由 worker 的维护循环定期调用，投影才能对存量数据也生效。
//
// 扫描只比较已落库的聚合列，覆盖缺行、终态未收敛和重试后的计数变化。
// 单元事实由 RefreshBatchSteps 校验，已收敛的行不会被周期性改写。
func (s *BatchStore) ListBatchIDsWithStaleSteps(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT b.id FROM batches b
    WHERE (SELECT COUNT(*) FROM batch_steps s
           WHERE s.batch_id = b.id AND s.phase IN ('plan', 'generate')) < 2
       OR EXISTS (
            SELECT 1 FROM batch_steps s
            WHERE s.batch_id = b.id
              AND s.phase IN ('plan', 'generate')
              AND (s.total_units <> b.planned_units
                   OR (s.phase = 'generate'
                       AND (s.done_units <> b.completed_units OR s.failed_units <> b.failed_units))))
       OR EXISTS (
            SELECT 1 FROM batch_steps s
            WHERE s.batch_id = b.id
              AND b.status IN ('completed', 'failed', 'partial_failed')
              AND s.status NOT IN ('completed', 'failed', 'skipped', 'partial_failed')
              AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.batch_id = b.id
                              AND j.job_kind = $2
                              AND j.status IN ('pending', 'leased', 'running')))
    ORDER BY b.id
    LIMIT $1`, limit, model.JobKindBatchGenerate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetBatch 读取单个批次；不存在返回 pgx.ErrNoRows。
func (s *BatchStore) GetBatch(ctx context.Context, batchID int64) (model.Batch, error) {
	batch, err := scanBatch(s.db.QueryRow(ctx, batchSelectByIDSQL, batchID))
	if err != nil {
		return model.Batch{}, err
	}
	// 失败原因不在批次行上（行里只有计数），必须在读取时从 batch_items 的事实分布
	// 补上，否则缺口文案只能写死 —— 那正是 #208 的误导来源。
	if batch.FailedUnits > 0 {
		class, err := dominantItemFailureClass(ctx, s.db, batchID)
		if err != nil {
			return model.Batch{}, err
		}
		batch.DominantFailureClass = class
	}
	return batch, nil
}

// dominantItemFailureClass 返回失败单元里占比最高的 error_class（无失败项时为空串）。
//
// 用「占比最高」而不是「第一条」：一个批次可能同时有 config_error 与 rate_limited，
// 而用户需要的是「主要原因是什么」，不是碰巧先写入的那条。
// 并列时按 error_class 升序取第一个，保证同一份事实每次都得到同一个答案（可重放）。
func dominantItemFailureClass(ctx context.Context, q queryable, batchID int64) (string, error) {
	var class string
	err := q.QueryRow(ctx, `
    SELECT error_class FROM batch_items
    WHERE batch_id = $1 AND status = 'failed' AND error_class <> ''
    GROUP BY error_class
    ORDER BY COUNT(*) DESC, error_class ASC
    LIMIT 1`, batchID).Scan(&class)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return class, nil
}

// maxInt 返回两个整数里较大的一个（缺口计算用）。
//
// 需要它是因为缺口的分母有两个来源：`planned_units` 是用户意图，而实际写入
// 的单元数可能更多（扩量、手动补单元）。两者取大就是「本来应该产出多少」。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// queryable 是 pgxpool.Pool 与 pgx.Tx 的共同查询面。
//
// 抽出来是为了让 dominantItemFailureClass 同时服务「事务内重算」（RefreshBatchCounts）
// 与「普通读取」（GetBatch），而不必写两份 SQL。
type queryable interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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

// ListBatchSteps 读取批次的阶段进度。
//
// issue #212：此前的实现只读 `batch_steps` 表，而 T05 只建了表、T12/T13 从未写入
// 任何一行 —— 于是「阶段进度」区块对**所有**批次恒为空，对已跑完的批次等于用空态
// 宣称「这次没有执行任何阶段」。
//
// 修法是让阶段进度成为 `batch_items` 的**投影**（唯一事实来源），而不是又一份
// 需要有人记得去写的计数：规划阶段看「计划量落成了多少单元」，生成阶段看
// 「这些单元里有多少产出了样本版本」。这与 #201 的教训同构 —— 声明式的进度
// 会与事实漂移，而投影不会。
//
// 读取**不写库**：投影每次从事实重算，因此不存在「读取顺手改写状态」的副作用；
// `batch_steps` 的持久化由 RefreshBatchSteps 负责（runner 跑完、维护循环收敛）。
//
// 持久化行里的身份与时间叠加回来，保持版本和历史记录可追溯。
func (s *BatchStore) ListBatchSteps(ctx context.Context, batchID int64) ([]model.BatchStep, error) {
	steps, err := s.derivedBatchSteps(ctx, s.db, batchID)
	if err != nil {
		return nil, err
	}
	return s.overlayPersistedStepTimes(ctx, batchID, steps)
}

// overlayPersistedStepTimes 把 batch_steps 里已落库的身份与时刻叠回投影结果。
//
// 按 phase 对齐（DB 的 UNIQUE (batch_id, phase) 保证了唯一性）。缺失对应行时
// 保留零值：读取路径不得因为「表里没有这行」而假装阶段不存在。
func (s *BatchStore) overlayPersistedStepTimes(ctx context.Context, batchID int64, steps []model.BatchStep) ([]model.BatchStep, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, phase, started_at, finished_at, created_at, updated_at
    FROM batch_steps WHERE batch_id = $1`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type persisted struct {
		id                    int64
		startedAt, finishedAt *time.Time
		createdAt, updatedAt  time.Time
	}
	byPhase := map[string]persisted{}
	for rows.Next() {
		var phase string
		var item persisted
		if err := rows.Scan(&item.id, &phase, &item.startedAt, &item.finishedAt,
			&item.createdAt, &item.updatedAt); err != nil {
			return nil, err
		}
		byPhase[phase] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for index := range steps {
		item, ok := byPhase[steps[index].Phase]
		if !ok {
			continue
		}
		steps[index].ID = item.id
		steps[index].StartedAt = item.startedAt
		steps[index].FinishedAt = item.finishedAt
		steps[index].CreatedAt = item.createdAt
		steps[index].UpdatedAt = item.updatedAt
	}
	return steps, nil
}

// RefreshBatchSteps 把阶段投影落盘进 `batch_steps` 并返回投影结果。
//
// 时间取执行记录中的历史事实，持久化保留阶段身份并让维护循环可以核对缺口。
//
// 调用点必须覆盖两种批次：#212 的缺陷形态是**历史批次**（已经跑完、不会再被
// runner 碰到），因此除了 runner 跑完，维护循环也必须收敛它们 ——
// 否则修复只对未来的批次生效（#201 的同一教训）。
func (s *BatchStore) RefreshBatchSteps(ctx context.Context, batchID int64) ([]model.BatchStep, error) {
	steps, err := s.derivedBatchSteps(ctx, s.db, batchID)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		if err := s.UpsertBatchStep(ctx, batchID, step); err != nil {
			return nil, err
		}
	}
	return s.ListBatchSteps(ctx, batchID)
}

// derivedBatchSteps 从 batch_items 的事实算出两个阶段行。
//
// 计数与时间都来自 batches / batch_items 的执行事实；维护时间不能冒充执行时间。
func (s *BatchStore) derivedBatchSteps(ctx context.Context, q queryable, batchID int64) ([]model.BatchStep, error) {
	var batchStatus string
	var planned int
	var batchFinished *time.Time
	if err := q.QueryRow(ctx, `
    SELECT status, planned_units, finished_at FROM batches WHERE id = $1`, batchID).
		Scan(&batchStatus, &planned, &batchFinished); err != nil {
		return nil, err
	}

	// COUNT(*) 是规划落库的单元数；FILTER 分出已把样本版本推进完的单元。
	// 用 `sample_version_id IS NOT NULL` 而不是 `status = 'succeeded'`：
	// 前者是「内容真的产出了」的唯一证据，后者是同一事实的状态镜像。
	var plannedDone, produced, failed, skipped, pending int
	var planStarted, planFinished, generateStarted, generateFinished *time.Time
	if err := q.QueryRow(ctx, `
    SELECT COUNT(*),
           COUNT(*) FILTER (WHERE sample_version_id IS NOT NULL),
           COUNT(*) FILTER (WHERE status = 'failed'),
           COUNT(*) FILTER (WHERE status = 'skipped'),
           COUNT(*) FILTER (WHERE status IN ('pending', 'running')),
           MIN(created_at), MAX(created_at),
           MIN(COALESCE(started_at, finished_at)), MAX(finished_at)
    FROM batch_items WHERE batch_id = $1`, batchID).
		Scan(&plannedDone, &produced, &failed, &skipped, &pending,
			&planStarted, &planFinished, &generateStarted, &generateFinished); err != nil {
		return nil, err
	}

	if planned < plannedDone {
		// 计划量是本阶段的**用户意图**（与 #190 的同一口径：不用已落库数覆盖它）。
		// 但它不能小于已落库单元数 —— 那时界面会显示「12 / 4」这种倒退的分数。
		planned = plannedDone
	}
	if planned < 0 {
		planned = 0
	}

	plan := model.BatchStep{
		BatchID:    batchID,
		Phase:      model.BatchStepPlan,
		UnitLabel:  model.BatchStepLabel(model.BatchStepPlan),
		TotalUnits: planned,
		DoneUnits:  plannedDone,
		Status:     model.StepStatusFor(batchStatus, plannedDone, 0, planned),
		StartedAt:  planStarted,
	}
	if plannedDone >= planned {
		plan.FinishedAt = planFinished
	} else if plan.Status == model.StepStatusFailed || plan.Status == model.StepStatusPartialFailed {
		plan.FinishedAt = batchFinished
	}
	// partial_failed 也可能表示仍在执行的批次；在途单元不能被标为已结束。
	generationStatus := batchStatus
	if pending > 0 && batchStatus == model.BatchStatusPartialFailed {
		generationStatus = model.BatchStatusRunning
	}
	generate := model.BatchStep{
		BatchID:     batchID,
		Phase:       model.BatchStepGenerate,
		UnitLabel:   model.BatchStepLabel(model.BatchStepGenerate),
		TotalUnits:  planned,
		DoneUnits:   produced,
		FailedUnits: failed,
		Status:      model.StepStatusFor(generationStatus, produced, failed+skipped, planned),
		StartedAt:   generateStarted,
	}
	if generate.Status == model.StepStatusCompleted || generate.Status == model.StepStatusFailed ||
		generate.Status == model.StepStatusPartialFailed {
		generate.FinishedAt = generateFinished
		if generate.FinishedAt == nil {
			generate.FinishedAt = batchFinished
		}
	}
	if failed+skipped > 0 {
		// 只记计数，原因不在这里推导：批次已经通过 DominantFailureClass /
		// ShortfallNote 给出**单一**原因来源（#208），阶段行再写一份会分叉。
		generate.ErrorSummary = fmt.Sprintf("有 %d 个失败单元、%d 个跳过单元", failed, skipped)
	}
	return []model.BatchStep{plan, generate}, nil
}

// UpsertBatchStep 创建或更新一个阶段（按 (batch_id, phase) 唯一）。
//
// 写入前锁住批次并验证当前单元事实：过期调用不能回退终态，合法重试产生的
// 新计数却必须能更新。时间使用执行事实，幂等重放不改 updated_at。
func (s *BatchStore) UpsertBatchStep(ctx context.Context, batchID int64, step model.BatchStep) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM batches WHERE id = $1 FOR UPDATE`, batchID).Scan(&lockedID); err != nil {
		return err
	}
	current, err := s.derivedBatchSteps(ctx, tx, batchID)
	if err != nil {
		return err
	}
	var canonical *model.BatchStep
	for index := range current {
		if current[index].Phase == step.Phase {
			canonical = &current[index]
			break
		}
	}
	if canonical == nil {
		return fmt.Errorf("未知批次阶段：%s", step.Phase)
	}
	if step.Status != canonical.Status || step.TotalUnits != canonical.TotalUnits ||
		step.DoneUnits != canonical.DoneUnits || step.FailedUnits != canonical.FailedUnits {
		return nil // 过期投影不覆盖新事实。
	}
	step = *canonical
	_, err = tx.Exec(ctx, `
    INSERT INTO batch_steps (batch_id, phase, unit_label, status, total_units, done_units, failed_units, error_summary,
                             started_at, finished_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    ON CONFLICT (batch_id, phase) DO UPDATE SET
      unit_label = EXCLUDED.unit_label,
      status = EXCLUDED.status,
      total_units = EXCLUDED.total_units,
      done_units = EXCLUDED.done_units,
      failed_units = EXCLUDED.failed_units,
      error_summary = EXCLUDED.error_summary,
      started_at = COALESCE(batch_steps.started_at, EXCLUDED.started_at),
      finished_at = CASE WHEN EXCLUDED.status IN ('completed', 'failed', 'skipped', 'partial_failed')
                         THEN COALESCE(EXCLUDED.finished_at, batch_steps.finished_at)
                         ELSE NULL END,
      updated_at = NOW()
    WHERE (batch_steps.unit_label, batch_steps.status, batch_steps.total_units, batch_steps.done_units,
           batch_steps.failed_units, batch_steps.error_summary,
           batch_steps.started_at, batch_steps.finished_at)
      IS DISTINCT FROM
          (EXCLUDED.unit_label, EXCLUDED.status, EXCLUDED.total_units, EXCLUDED.done_units,
           EXCLUDED.failed_units, EXCLUDED.error_summary,
           COALESCE(batch_steps.started_at, EXCLUDED.started_at),
           CASE WHEN EXCLUDED.status IN ('completed', 'failed', 'skipped', 'partial_failed')
                THEN COALESCE(EXCLUDED.finished_at, batch_steps.finished_at)
                ELSE NULL END)`,
		batchID, step.Phase, step.UnitLabel, step.Status,
		step.TotalUnits, step.DoneUnits, step.FailedUnits, step.ErrorSummary, step.StartedAt, step.FinishedAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	//
	// planned_units 与这里一起读：resume 的缺口判定需要同一个快照下的
	// 计划量（分两条语句读会与状态判定之间产生不一致的窗口）。
	var status, controlState string
	var planned int
	if err := tx.QueryRow(ctx, `
    SELECT status, control_state, planned_units FROM batches WHERE id = $1 AND project_id = $2 FOR UPDATE`,
		batchID, projectID).Scan(&status, &controlState, &planned); err != nil {
		return model.Batch{}, err
	}

	terminal := status == model.BatchStatusCompleted || status == model.BatchStatusFailed
	if terminal {
		return model.Batch{}, fmt.Errorf("%w：批次已结束（%s），不能%v", ErrBatchNotControllable, status, controlVerb(action))
	}

	var nextStatus, nextControl string
	// shouldEnqueue 表示本次控制动作**真的产生了待办工作**，因此需要派作业。
	//
	// issue #202：不区分这一点会派一个什么都不做的作业 —— 例如 b_1 的 12 个单元
	// 全部已定稿、只剩不可重试失败，resume 无法产生任何新工作，入队只是空转一轮
	// （还会多写一条事件，污染时间线）。
	shouldEnqueue := false
	// pendingUnits 已落库、但还没执行的单元数（runner 会处理它们）。
	pendingUnits := 0
	switch action {
	case "pause":
		if controlState == model.BatchControlPaused || controlState == model.BatchControlPauseRequested {
			return model.Batch{}, fmt.Errorf("%w：批次已经处于暂停或暂停请求状态", ErrBatchNotControllable)
		}
		nextControl = model.BatchControlPauseRequested
		nextStatus = model.BatchStatusPauseRequested
	case "resume":
		// issue #201：暂停恢复之外，`resume` 还承载「补齐缺口」。
		//
		// b_2 的形态是 planned=4 / completed=1 / failed=0：既不在暂停、也不是
		// 「重试失败项」（一个失败都没有），却少了 3 个从未被创建的单元。
		// 旧实现只允许 `controlState != run` 的批次恢复，因此这条缺口在界面上
		// 根本无路可走。现在 partial_failed（已定稿但产出不足）也允许走本路径。
		gapBatch := status == model.BatchStatusPartialFailed
		if controlState == model.BatchControlRun && !gapBatch {
			return model.Batch{}, fmt.Errorf("%w：批次没有处于暂停状态", ErrBatchNotControllable)
		}
		nextControl = model.BatchControlRun
		var failedCount, counted int
		if err := tx.QueryRow(ctx, `
      SELECT COUNT(*) FILTER (WHERE status = 'failed'), COUNT(*)
      FROM batch_items WHERE batch_id = $1`,
			batchID).Scan(&failedCount, &counted); err != nil {
			return model.Batch{}, err
		}
		if err := tx.QueryRow(ctx, `
      SELECT COUNT(*) FROM batch_items WHERE batch_id = $1 AND status IN ('pending', 'running')`,
			batchID).Scan(&pendingUnits); err != nil {
			return model.Batch{}, err
		}
		// uncreated 是「计划里有、但库里连一行单元都没有」的个数。
		//
		// 它才是 resume 能创造的**新**工作（runner 的 ensureItems 会把缺失的键补出来）。
		// 已存在的失败行不在其中 —— 那些只有 `retry-failed`（重置可重试项）能处理，
		// 而把不可重试的失败当作「待办」只会重复得到同样的失败并再花一次钱。
		uncreated := planned - counted
		if uncreated < 0 {
			uncreated = 0
		}
		switch {
		case pendingUnits > 0 || uncreated > 0:
			// 有待办或有待创建的单元：回到运行态并派作业（runner 是幂等的，
			// 已成功的单元不会被重跑）。
			nextStatus = model.BatchStatusRunning
			shouldEnqueue = true
		case failedCount > 0:
			// 没有任何待办但仍有失败项（含不可重试）：落到「部分完成」，
			// 缺口可见。旧实现无条件声明 running，于是「继续」把批次变成
			// 一个永远不会推进的僵尸（这正是 #202 的实测形态）。
			nextStatus = model.BatchStatusPartialFailed
		default:
			// 既无待办也无缺口也无失败：产出已全部定稿。
			nextStatus = model.BatchStatusCompleted
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
		Reason:     fmt.Sprintf("status=%s control=%s pending=%d", nextStatus, nextControl, pendingUnits),
	}); err != nil {
		return model.Batch{}, err
	}

	// issue #202(a)：resume 必须**同时**建新作业，而不只是改状态。
	//
	// 旧实现只改 status/control_state 并写事件，不创建 job、也不写 outbox，
	// 因此对一个「作业早已 succeeded 结束」的批次点「继续」等于把状态指针拨回
	// running 后什么都不做 —— 状态说在跑，事实一条都不会再跑。
	// 入队必须与状态变更在**同一事务**：分两次写会留下「状态回运行但作业丢了」
	// 的半成品，而那正是本条缺陷的形态。
	if action == "resume" && shouldEnqueue {
		if err := enqueueBatchGenerateTx(ctx, tx, projectID, batchID, actorID, "resume"); err != nil {
			return model.Batch{}, err
		}
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

// enqueueBatchGenerateTx 为批次派发一个新的生成作业（与调用方的事务同生共死）。
//
// issue #202(a)：`resume` 以前只改状态不建作业，因此对「作业早已 succeeded 结束」
// 的批次点「继续」等于什么都不做 —— 状态指针拨回 running，而没有任何东西会再跑。
//
// 幂等键取「该批次已有生成作业数 + 1」：它在本函数调用时已被调用方
// `SELECT ... FOR UPDATE` 锁住的批次事务里求值，因此并发恢复不会得到同一个序号；
// 而同一个事务重放时会得到同一个键，于是不会派发两个作业。
// 直接复用批次 ID 当键是错的 —— 后续每次恢复都会被 ON CONFLICT DO NOTHING 吞掉。
func enqueueBatchGenerateTx(ctx context.Context, tx pgx.Tx, projectID, batchID, actorID int64, reason string) error {
	var sequence int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) + 1 FROM jobs WHERE batch_id = $1 AND job_kind = $2`,
		batchID, model.JobKindBatchGenerate).Scan(&sequence); err != nil {
		return err
	}
	project := projectID
	batch := batchID
	actor := actorID
	_, _, err := EnqueueJobTx(ctx, tx, EnqueueJobInput{
		ProjectID:      &project,
		BatchID:        &batch,
		Kind:           model.JobKindBatchGenerate,
		IdempotencyKey: fmt.Sprintf("batch:%d:generate:%d", batchID, sequence),
		CreatedBy:      &actor,
		Payload: map[string]any{
			"batchId": batchID,
			"reason":  reason,
		},
	})
	return err
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

	// issue #202：重置 0 项时**不能**把批次丢回 running。
	//
	// 真实形态（b_1）：12 个单元全是 `config_error` 且 `retryable=false`，
	// 因此这里重置 0 条，而旧实现仍无条件把状态改成 running —— 批次于是成为
	// 「状态说在跑、事实一条都不会再跑」的僵尸，用户只能反复点控制按钮
	//（每点一次多写一条事件，污染时间线）。
	//
	// 修法：只有真的重置出待执行项才回到 running；否则**不动状态**（不回声明
	// 一个事实不支持的运行态，收敛由计数重算与 resume 路径负责）。
	if reset > 0 {
		if _, err := tx.Exec(ctx, `
      UPDATE batches
      SET status = CASE WHEN status = 'failed' THEN 'partial_failed' ELSE 'running' END,
          control_state = 'run',
          updated_at = NOW()
      WHERE id = $1`, batchID); err != nil {
			return 0, err
		}
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

	// issue #202(a) 的同形态：重置出待执行单元却**不派作业**，一样会让批次成为
	// 僵尸（状态说在跑、事实一条都不会再跑）。只有真的重置出单元时才入队。
	if reset > 0 {
		if err := enqueueBatchGenerateTx(ctx, tx, projectID, batchID, actorID, "retry-failed"); err != nil {
			return 0, err
		}
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
	aliases := map[string]string{
		"judge_prompt":  "judgePrompt",
		"level_rubrics": "levelRubrics",
	}
	for _, field := range required {
		value, ok := decoded[field]
		if !ok {
			value, ok = decoded[aliases[field]]
		}
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
		rubricRaw, rubricOK := decoded["level_rubrics"]
		if !rubricOK {
			rubricRaw, rubricOK = decoded["levelRubrics"]
		}
		if rubricOK {
			if err := json.Unmarshal(rubricRaw, &rubrics); err != nil {
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
    SET status = 'succeeded', sample_id = $3, sample_version_id = $2, error_class = '', error_message = '',
        finished_at = NOW(), updated_at = NOW()
    WHERE id = $1`, itemID, version.ID, version.SampleID); err != nil {
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

// MarkBatchItemSkipped records a source unit that cannot produce a sample
// without inventing content (for example a legacy question with no answer).
// Skipped is a terminal, auditable state and is intentionally distinct from a
// provider failure: it must not be retried or counted as a generated sample.
func (s *BatchStore) MarkBatchItemSkipped(ctx context.Context, batchID, itemID int64, message string) (bool, error) {
	if len([]rune(message)) > 1000 {
		message = string([]rune(message)[:1000])
	}
	tag, err := s.db.Exec(ctx, `
    UPDATE batch_items
    SET status = 'skipped', error_class = 'schema_violation', error_message = $3,
        retryable = FALSE, finished_at = NOW(), updated_at = NOW()
    WHERE id = $1 AND batch_id = $2 AND status NOT IN ('succeeded', 'skipped')`,
		itemID, batchID, message)
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

	// planned 取「用户计划量」与「已落库单元数」的较大值，绝不用 total 覆盖它：
	// 计划量是用户的意图事实（界面显示「计划 12」），而 total 只是已写入的单元数。
	// 把计划量改小会让 #190 的缺口在状态推导里消失。
	var planned int
	if err := tx.QueryRow(ctx, `SELECT planned_units FROM batches WHERE id = $1`, batchID).
		Scan(&planned); err != nil {
		return model.Batch{}, err
	}

	// activeJobs 是「本批次是否还有一个活作业」。
	//
	// 为什么聚合需要知道它（issue #202）：一个 `running` 但还没落任何单元的批次
	// 有两种完全不同的含义 —— 「runner 刚被派发、还没建单元」（正常，不能动）
	// 与「没有任何东西会再跑」（僵尸，必须收敛）。两者的唯一区别就是有没有活作业。
	// 把它写进**推导本身**（而不是依靠调用方先过滤）是为了让不变式自洽：
	// 以后新增调用点也不会把正在跑的批次误判为僵尸。
	var activeJobs int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FROM jobs
    WHERE batch_id = $1 AND job_kind = $2
      AND status IN ('pending', 'leased', 'running')`,
		batchID, model.JobKindBatchGenerate).Scan(&activeJobs); err != nil {
		return model.Batch{}, err
	}

	// 状态聚合：终态由「事实是否已定稿」推导，而不是由调用方声明。
	//
	// issue #190："completed" 只允许在"计划量真的都产出了"时出现 ——
	// 「计划 12、完成 1」显示绿色「已完成」是比直接失败更危险的静默少交付。
	//
	// issue #201/#202：聚合必须**收敛**，不能把不一致的状态当作不可变的历史事实。
	//   - #201：`completed` 且 `planned > completed`（b_2 计划 4 只产出 1）永远停在
	//     「已完成」，而 BatchCapabilitiesFor 对 completed 返回全 false —— 用户连
	//     一个可点的按钮都没有。终态必须**仍然复核缺口**。
	//   - #202：所有单元已定稿、无在途作业，状态却仍是 `running`（b_1 12/12 失败），
	//     批次成为「状态说在跑、事实一条都不会再跑」的僵尸。
	//
	// 两个前提决定了推导的形态：
	//   1. 只有「无活作业且无在途单元」时才允许判终态。否则会把一个刚被派发、
	//      runner 还没建单元的正在跑的批次抢先判死 —— 这是本函数最危险的误判方向。
	//   2. 推导必须是**幂等的不动点**：对同一份事实重复调用必须得到同一个状态，
	//      否则维护循环会每 30 秒改一次状态而永远不收敛（本函数的第一版就犯过这个错，
	//      被 TestListDivergentBatchIDsFindsStaleStates 抓出）。
	nextStatus := status
	switch {
	case status == model.BatchStatusPauseRequested || status == model.BatchStatusPaused:
		// 暂停态保持：控制意图高于计数推导（在途请求仍会完成，见 §2.4）。
	case status == model.BatchStatusFailed:
		// failed 是**致命**终态（继续重试没有意义）。completed 不在这里 ——
		// 它必须复核缺口（#201）。
	case activeJobs == 0 && inFlight == 0:
		// 已定稿：按已完成/失败/缺口三个事实分叉。这一支是幂等的 ——
		// 同样的事实每次得到同样的结果。
		settled := completed + failed
		switch {
		case failed > 0:
			// 有失败项（含不可重试的）永远是 partial_failed，缺口可见。
			nextStatus = model.BatchStatusPartialFailed
		case planned == 0 && settled == 0:
			// 计划量为 0 的空批次没有待办工作，「已完成」才是诚实描述。
			nextStatus = model.BatchStatusCompleted
		case completed >= planned && settled >= planned:
			nextStatus = model.BatchStatusCompleted
		default:
			// 产出少于计划（或计划单元从未被创建）：缺口必须可见，
			// 不能叫「已完成」。
			nextStatus = model.BatchStatusPartialFailed
		}
	case failed > 0 && status != model.BatchStatusQueued:
		// 还有在途/待执行单元且有失败：缺口已成立（queued 除外 —— 那是刚创建、
		// 还没开始跑，不应在第一个单元就把它标成「部分失败」）。
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
        finished_at = CASE WHEN $6 IN ('completed', 'failed', 'partial_failed') THEN NOW() ELSE finished_at END,
        updated_at = NOW()
    WHERE id = $1`, batchID, completed, failed, inFlight, total, nextStatus); err != nil {
		return model.Batch{}, err
	}

	// 状态被修正时必须留一条事件（issue #201）。
	//
	// b_2 的实测形态是：时间线只有 BatchQueued + BatchCompleted 两条，而实际上
	// 只产出了 1/4。用户看到「已完成」，时间线里又找不到任何解释缺口的记录 ——
	// 界面因此自相矛盾。静默地从 completed 改成 partial_failed 同样不够：
	// 下一轮看到旧截图的人依然不知道发生了什么。事件是这里唯一的解释载体。
	//
	// 只在**推导出的终态与原状态不同且原状态已是终态**时写：正常路径
	// （running → completed）由 runner 自己的终态事件负责，不要写两条。
	prevTerminal := status == model.BatchStatusCompleted || status == model.BatchStatusFailed ||
		status == model.BatchStatusPartialFailed
	if nextStatus != status && prevTerminal && nextStatus == model.BatchStatusPartialFailed {
		if err := appendBatchEventTx(ctx, tx, batchID, projectID, model.BatchEventPartialFailed, 0, map[string]any{
			"batchId":      batchID,
			"previous":     status,
			"planned":      maxInt(planned, total),
			"completed":    completed,
			"failed":       failed,
			"shortfall":    maxInt(planned, total) - completed,
			"correctionOf": status,
			"reason":       "状态与单元事实不符：计划量未被产出，已从终态修正为部分完成",
		}); err != nil {
			return model.Batch{}, err
		}
	}

	batch, err := scanBatch(tx.QueryRow(ctx, batchSelectByIDSQL, batchID))
	if err != nil {
		return model.Batch{}, err
	}
	// 缺口原因必须随聚合一起刷新：调用方（runner、控制命令）拿到返回值后
	// 直接渲染缺口文案，若这里不填，它就只能退化成中性描述（#208）。
	if batch.FailedUnits > 0 {
		class, err := dominantItemFailureClass(ctx, tx, batchID)
		if err != nil {
			return model.Batch{}, err
		}
		batch.DominantFailureClass = class
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
		&batch.BudgetReservedMinor, &batch.BudgetSettledMinor, &batch.BudgetUncertainMinor,
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

// SampleVersionFact 是「一条产出内容」的分析事实（issue #197 第 13 条）。
//
// 只取分析需要的字段，不返回 payload 正文：
// 批次详情要做长度/难度/接地/重复/审阅状态统计，而把 10 万条正文拉进
// 进程既慢又没必要（长度在 SQL 里用 jsonb 的字面长度算即可）。
type SampleVersionFact struct {
	SampleVersionID   int64
	SampleKey         string
	DomainStableID    string
	DirectionStableID string
	Difficulty        string
	ReviewStatus      string
	ContentHash       string
	PayloadChars      int
	// LengthByField 是真正参与长度统计的字段 → 字符数（键取自 model.SampleLengthFields）。
	//
	// 为什么必须带出来（issue #197 第 13 条残余）：`PayloadChars` 是多个文本字段的
	// **合计**，而「合计了哪几个」此前只存在于本文件的实现细节里 —— 读模型只能
	// 硬编码一个字段数，界面上也没有任何口径说明，于是用户看到「长度中位 1082」
	// 会以为是单条内容（例如只算问题）的长度。让事实随读数一起出来，
	// 是上层能诚实标注口径、并给出逐字段分列的前提。
	LengthByField map[string]int
	Grounded      bool
}

// ListSampleVersionFacts 读取某批次产出的样本版本分析事实。
//
// 三条口位约定：
//
//  1. **只读 sample_versions**（只追加的内容事实），不读「当前采用」指针：
//     分析必须针对这一批真正产出的内容，否则「跑了 3 次、改了 2 次配置」的
//     项目会把历史内容算进当前批次。
//  2. **方向与难度来自 batch_items.item_key**（形如 `domain/direction#ordinal`）：
//     它记录的是**产出时**的实际分配，而不是事后拿覆盖版本重算（覆盖可以被改）。
//  3. **接地与否来自 payload**：`question`/`reasoning`/`answer` 之外的
//     `sourceChunkIds`（或 `sourceChunk`）字段存在且非空即视为有素材接地。
//     没有该字段的项目自然全部为 false，这是**如实**的而不是缺陷。
func (s *BatchStore) ListSampleVersionFacts(ctx context.Context, projectID, batchID int64, limit int) ([]SampleVersionFact, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.Query(ctx, `
    SELECT sv.id,
           s.sample_key,
           sv.payload,
           COALESCE(sv.content_hash, ''),
           COALESCE(rp.effective_action, 'pending'),
           COALESCE(bi.item_key, '')
    FROM sample_versions sv
    JOIN samples s ON s.id = sv.sample_id
    LEFT JOIN batch_items bi ON bi.id = sv.batch_item_id
    -- review_projections 以**样本版本**为主键（不是样本）：一个样本可以有多版，
    -- 每版的审阅状态各自独立。用 sample_id 关联会得到 SQLSTATE 42703。
    LEFT JOIN review_projections rp ON rp.sample_version_id = sv.id
    WHERE sv.project_id = $1 AND sv.batch_id = $2
    ORDER BY sv.id
    LIMIT $3`, projectID, batchID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	facts := []SampleVersionFact{}
	for rows.Next() {
		var fact SampleVersionFact
		var payload []byte
		var itemKey string
		if err := rows.Scan(&fact.SampleVersionID, &fact.SampleKey, &payload,
			&fact.ContentHash, &fact.ReviewStatus, &itemKey); err != nil {
			return nil, err
		}
		fact.DomainStableID, fact.DirectionStableID = splitItemKey(itemKey)
		fact.Difficulty, fact.PayloadChars, fact.LengthByField, fact.Grounded = summarizePayload(payload)
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// splitItemKey 把 `domain/direction#ordinal` 拆成 (domain, direction)。
//
// 键不存在或形状异常时返回空串而不是猜测：猜测会把不同方向的内容
// 混进同一行统计，而那是比「未归类」更糟的错误。
func splitItemKey(itemKey string) (string, string) {
	trimmed := strings.TrimSpace(itemKey)
	if trimmed == "" {
		return "", ""
	}
	if index := strings.LastIndex(trimmed, "#"); index >= 0 {
		trimmed = trimmed[:index]
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// payloadLengthFields 是参与「内容长度」统计的字段键。
//
// 取 `model.SampleLengthFields()` 而不是在本文件重写一份：长度口径已经跨
// store / studio / 前端三处使用，第二份清单必然漂移（AGENTS.md §3.1）。
// 这里只是把它取一次以避开循环内的反复分配。
var payloadLengthFields = model.SampleLengthFields()

// summarizePayload 从内容 payload 里取出分析需要的四个值。
//
// 用解析后的字符串长度而不是 JSON 字节数：JSON 里的转义（`\n` 占两个字节）
// 会让「长度」变成存储大小的度量，而不是用户看到的文本长度。
//
// 第三个返回值是**逐字段的字符数**（只含真正出现且有内容的字段）：空串贡献
// 0 个字符，因此不算参与 —— 这样「长度 = 哪些字段之和」才能被上层如实标注，
// 而不用猜一套硬编码的字段数（issue #197 第 13 条）。
func summarizePayload(payload []byte) (difficulty string, chars int, byField map[string]int, grounded bool) {
	if len(payload) == 0 {
		return "", 0, nil, false
	}
	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		return "", 0, nil, false
	}
	for _, key := range []string{"difficulty", "difficultyLevel"} {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			difficulty = strings.ToLower(strings.TrimSpace(value))
			break
		}
	}
	for _, key := range payloadLengthFields {
		value, ok := record[key].(string)
		if !ok || value == "" {
			continue
		}
		fieldChars := len([]rune(value))
		chars += fieldChars
		if byField == nil {
			byField = map[string]int{}
		}
		byField[key] = fieldChars
	}
	for _, key := range []string{"sourceChunkIds", "sourceChunks", "groundedFrom"} {
		value, ok := record[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []any:
			grounded = grounded || len(typed) > 0
		case string:
			grounded = grounded || strings.TrimSpace(typed) != ""
		}
	}
	return difficulty, chars, byField, grounded
}
