package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现旧数据的**幂等导入**（Issue #160 T31）。
//
// 契约：#160 T31 的原文要求：「按 dataset 小批导入，`legacy_imports` 唯一来源键/
// 游标/内容 hash，支持暂停、续跑、重复执行；不覆盖已迁移后产生的新版本；
// 每个 dataset 迁移前冻结旧写入口并等在途完成，或记录明确一致性水位；
// 迁移前后数量/状态/内容 hash/引用/文件下载对账，失败报告精确到对象」。
//
// 三层幂等（任何一层单独都不够）：
//
//  1. **台账唯一键**：`legacy_imports(source_kind, source_key)`。重复执行先命中
//     这一行；`completed` 时直接回放，不产生任何副作用。
//  2. **内容 hash**：台账因中途失败未标完成时，按 `sample_key + content_hash`
//     判断「这条内容已经导入过」，因此不会把同一份历史内容追加成 version+1。
//  3. **确定性 sample_key**：`legacy-<datasetId>-<questionId>`。键不稳定的话
//     第二层判据也无从命中（同一内容会落到不同的样本上）。
//
// 关于「不覆盖已迁移后产生的新版本」：本实现的写入路径只有
// `AppendSampleVersion`（只追加，没有 UPDATE）。因此**结构上不可能**覆盖，
// 而不是靠「记得不要更新」。
//
// 关于「冻结旧写入口 + 一致性水位」：本文件在 `before_snapshot` 里记录
// 旧侧的数量与内容摘要作为**一致性水位**。冻结旧写入口（让旧 API 拒绝写入
// 并返回迁移说明）属于 API 层，见 `apps/api/routes_legacy.go`。

const (
	// LegacyImportSourceKindDataset 是导入来源类型（与迁移 0034 的 CHECK 一致）。
	LegacyImportSourceKindDataset = "dataset"
	// maxRecordedFailures 是报告里保留的失败条数上限。
	//
	// 上限存在的理由：一次几万条导入的失败列表会让报告文件大到无法在终端阅读，
	// 而「有几条失败、前几条为什么」才是用户能行动的信息。计数不截断。
	maxRecordedFailures = 200
)

// ImportDeps 是导入需要的依赖。
type ImportDeps struct {
	Pool      *pgxpool.Pool
	Projects  *store.ProjectStore
	Batches   *store.BatchStore
	Imports   *store.LegacyImportStore
	Documents *store.DocumentStore
}

// NewImportDeps 构造依赖。
func NewImportDeps(pool *pgxpool.Pool) ImportDeps {
	return ImportDeps{
		Pool:      pool,
		Projects:  store.NewProjectStore(pool),
		Batches:   store.NewBatchStore(pool),
		Imports:   store.NewLegacyImportStore(pool),
		Documents: store.NewDocumentStore(pool),
	}
}

// ImportOptions 控制一次导入。
type ImportOptions struct {
	DatasetID int64
	// TargetProjectID 显式指定目标项目（0 = 按 legacy_dataset_id 反查或新建）。
	//
	// 为什么需要它：T30 把「同名项目已存在」归为 conflict，那时自动新建会失败，
	// 而正确的处置往往是「导入到运维已经建好的那个项目」。有这条路径，
	// conflict 才是可操作的，而不是一个死胡同。
	TargetProjectID int64
	// WorkspaceID 为 0 时用默认工作区（与项目创建 API 的语义一致）。
	WorkspaceID int64
	// ActorID 是执行导入的用户（写进 created_by 与审计）。
	ActorID int64
	// OwnerOverrideID 允许管理员为历史数据集补充缺失归属。它不会改写旧
	// datasets.created_by，只把明确授权的用户作为新项目 owner，并写入台账说明。
	// 没有 owner 且没有显式 override 时仍然拒绝导入。
	OwnerOverrideID int64
	// DryRun 为 true 时**不写任何业务数据**，只产出计划与对账水位。
	DryRun bool
	// BatchSize 是每次从 questions 取的条数（小批导入）。
	BatchSize int
	// Resume 为 true 时从台账游标继续；false 时若台账已存在但未完成也从头开始
	//（仍受内容 hash 幂等保护，不会产生重复版本）。
	Resume bool
}

// ImportFailure 是一条失败明细（精确到源对象）。
type ImportFailure struct {
	SourceID int64  `json:"sourceId"`
	Reason   string `json:"reason"`
}

// Reconciliation 是一侧的对账数据。
type Reconciliation struct {
	Questions        int64  `json:"questions"`
	SFTRecords       int64  `json:"sftRecords"`
	ReasoningRecords int64  `json:"reasoningRecords"`
	GRPOPrompts      int64  `json:"grpoPrompts"`
	RewardRecords    int64  `json:"rewardRecords"`
	Artifacts        int64  `json:"artifacts"`
	Samples          int64  `json:"samples"`
	Versions         int64  `json:"versions"`
	ContentHash      string `json:"contentHash,omitempty"`
}

// ImportSummary 是一次导入的结果（写报告与日志用）。
type ImportSummary struct {
	DatasetID   int64                    `json:"datasetId"`
	ProjectID   int64                    `json:"projectId"`
	BatchID     int64                    `json:"batchId,omitempty"`
	Status      string                   `json:"status"`
	Replay      bool                     `json:"replay"`
	DryRun      bool                     `json:"dryRun"`
	Counts      store.LegacyImportCounts `json:"counts"`
	Before      Reconciliation           `json:"before"`
	After       Reconciliation           `json:"after"`
	ContentHash string                   `json:"contentHash,omitempty"`
	Failures    []ImportFailure          `json:"failures"`
	Notes       []string                 `json:"notes"`
}

// DatasetSourceKey 返回一个 dataset 的来源键。
func DatasetSourceKey(datasetID int64) string {
	return fmt.Sprintf("dataset:%d", datasetID)
}

// legacyDatasetRow 是导入需要的 dataset 字段。
type legacyDatasetRow struct {
	ID          int64
	Name        string
	RootKeyword string
	Status      string
	TargetKind  string
	OwnerID     *int64
}

// ImportDataset 幂等导入一个旧 dataset。
func ImportDataset(ctx context.Context, deps ImportDeps, options ImportOptions) (ImportSummary, error) {
	if options.DatasetID <= 0 {
		return ImportSummary{}, errors.New("需要 datasetId")
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 500
	}
	if options.BatchSize > 5000 {
		options.BatchSize = 5000
	}

	dataset, err := loadLegacyDataset(ctx, deps.Pool, options.DatasetID)
	if err != nil {
		return ImportSummary{}, err
	}
	summary := ImportSummary{DatasetID: dataset.ID, DryRun: options.DryRun, Failures: []ImportFailure{}}

	// 归属：没有可靠 owner 的 dataset 一律拒绝自动导入（T30 的 blocker）。
	// 理由：导入本身就是一次授权（内容进入某个工作区、某些人有读权）。
	if dataset.OwnerID == nil && options.OwnerOverrideID <= 0 {
		return summary, errors.New("dataset 没有可靠归属（created_by 为空）：请先由管理员显式分派，" +
			"再指定目标项目后导入（默认不会给任意用户读权限）")
	}
	if dataset.OwnerID == nil {
		var exists bool
		if err := deps.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, options.OwnerOverrideID).Scan(&exists); err != nil {
			return summary, err
		}
		if !exists {
			return summary, fmt.Errorf("指定的归属用户 %d 不存在", options.OwnerOverrideID)
		}
		summary.Notes = append(summary.Notes, fmt.Sprintf("源 dataset 未设置 owner；管理员已将新项目归属显式分派给用户 %d（未改写旧数据）", options.OwnerOverrideID))
	}

	before, err := reconcileDataset(ctx, deps.Pool, dataset)
	if err != nil {
		return summary, err
	}
	summary.Before = before

	// 解析目标项目：显式指定 → 已有映射 → 新建。
	projectID := int64(0)
	if options.WorkspaceID <= 0 {
		workspace, err := deps.Projects.DefaultWorkspace(ctx)
		if err != nil {
			return summary, fmt.Errorf("解析默认工作区失败：%w", err)
		}
		options.WorkspaceID = workspace.ID
	}
	projectID, err = deps.Imports.FindProjectByLegacyDataset(ctx, dataset.ID)
	if err != nil {
		return summary, err
	}
	if options.TargetProjectID > 0 {
		// 显式指定的目标项目优先，但要先确认它真的存在：
		// 直接把一个不存在的 ID 写进台账会让后续所有导入都指向空。
		var exists bool
		if err := deps.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1)`,
			options.TargetProjectID).Scan(&exists); err != nil {
			return summary, err
		}
		if !exists {
			return summary, fmt.Errorf("指定的目标项目 %d 不存在", options.TargetProjectID)
		}
		if projectID != 0 && projectID != options.TargetProjectID {
			summary.Notes = append(summary.Notes, fmt.Sprintf(
				"注意：该 dataset 已映射到项目 %d，本次显式指定为 %d —— 台账会记录新目标，但两个映射同时存在",
				projectID, options.TargetProjectID))
		}
		projectID = options.TargetProjectID
	}
	if projectID == 0 {
		// dry-run 不得建项目（它与建样本一样是写入）。
		// 这里只做「会不会冲突」的检查并告知，让运维在真跑之前看到风险。
		if options.DryRun {
			conflict, err := projectNameTaken(ctx, deps, options, dataset)
			if err != nil {
				return summary, err
			}
			if conflict {
				summary.Notes = append(summary.Notes, fmt.Sprintf(
					"dry-run：工作区里已存在同名项目 %q，真跑会因冲突失败；请先重命名或用 -target-project 指定目标项目",
					dataset.Name))
			} else {
				summary.Notes = append(summary.Notes, fmt.Sprintf(
					"dry-run：将新建 legacy-origin 项目 %q（legacy_dataset_id=%d）", dataset.Name, dataset.ID))
			}
			summary.Status = "dry_run"
			summary.Notes = append(summary.Notes,
				"dry-run：未写入任何业务数据，也未创建导入台账；上面的对账水位是导入前的快照")
			return summary, nil
		}
		projectID, err = resolveOrCreateProject(ctx, deps, options, dataset)
		if err != nil {
			return summary, err
		}
		summary.Notes = append(summary.Notes,
			fmt.Sprintf("新建 legacy-origin 项目 %d（legacy_dataset_id=%d）", projectID, dataset.ID))
	}
	summary.ProjectID = projectID
	if options.DryRun {
		summary.Status = "dry_run"
		summary.Notes = append(summary.Notes,
			"dry-run：未写入任何业务数据，也未创建导入台账；上面的对账水位是导入前的快照")
		return summary, nil
	}
	if deps.Documents != nil {
		if err := deps.Documents.BootstrapProjectDocuments(ctx, projectID, options.ActorID, dataset.TargetKind, dataset.Name, dataset.RootKeyword); err != nil {
			return summary, fmt.Errorf("初始化迁移项目配置失败：%w", err)
		}
	}

	importRow, replay, err := deps.Imports.BeginImport(ctx, LegacyImportSourceKindDataset, DatasetSourceKey(dataset.ID))
	if err != nil {
		return summary, err
	}
	summary.BatchID = derefInt64Ptr(importRow.BatchID)
	summary.Counts = importRow.Counts
	if replay {
		summary.Replay = true
		summary.Status = importRow.Status
		summary.ContentHash = importRow.ContentHash
		summary.Notes = append(summary.Notes,
			"该来源已完成过导入：本次为回放，未产生任何新版本（重复执行零重复导入）")
		after, err := reconcileDataset(ctx, deps.Pool, dataset)
		if err != nil {
			return summary, err
		}
		summary.After = after
		return summary, nil
	}

	if err := deps.Imports.SetImportTarget(ctx, importRow.ID, &projectID, nil); err != nil {
		return summary, err
	}

	// 快照批次的创建与「标记完成」都放在内容导入之前还是之后？
	// 放在之前：批次是内容的容器（sample_versions.batch_id 指向它），
	// 而且它的 ID 要写进台账供续跑复用。
	if summary.BatchID == 0 {
		batchID, err := ensureSnapshotBatch(ctx, deps, options, dataset, projectID)
		if err != nil {
			return summary, err
		}
		summary.BatchID = batchID
		if err := deps.Imports.SetImportTarget(ctx, importRow.ID, nil, &batchID); err != nil {
			return summary, err
		}
	}

	counts, cursor, failures, err := importQuestions(ctx, deps, options, dataset, projectID, summary.BatchID, importRow)
	summary.Counts = counts
	summary.Failures = failures
	if markErr := deps.Imports.MarkImportedBatchCompleted(ctx, summary.BatchID); markErr != nil && err == nil {
		return summary, markErr
	}

	after, reconcileErr := reconcileDataset(ctx, deps.Pool, dataset)
	if reconcileErr != nil {
		return summary, reconcileErr
	}
	summary.After = after
	summary.ContentHash = after.ContentHash

	status := "completed"
	errorMessage := ""
	if err != nil {
		status = "failed"
		errorMessage = err.Error()
	}
	if len(failures) > 0 && status == "completed" {
		// 有失败项时不标 completed：那会掩盖「部分内容没进来」。
		status = "failed"
		errorMessage = fmt.Sprintf("%d 条内容导入失败，详见 failures", counts.FailedItems)
	}

	failuresJSON, marshalErr := json.Marshal(failures)
	if marshalErr != nil {
		return summary, marshalErr
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if finishErr := deps.Imports.FinishImport(ctx, importRow.ID, status, cursor, counts,
		after.ContentHash, beforeJSON, afterJSON, failuresJSON, errorMessage); finishErr != nil {
		return summary, finishErr
	}
	summary.Status = status
	if err != nil {
		return summary, err
	}
	summary.Notes = append(summary.Notes,
		fmt.Sprintf("对账：问题 %d → 样本 %d / 内容版本 %d；跳过（已存在）%d，跳过（无内容）%d，失败 %d",
			after.Questions, after.Samples, after.Versions,
			counts.SkippedExisting, counts.SkippedNoContent, counts.FailedItems))
	return summary, nil
}

// loadLegacyDataset 读取一个 dataset。
func loadLegacyDataset(ctx context.Context, pool *pgxpool.Pool, datasetID int64) (legacyDatasetRow, error) {
	var row legacyDatasetRow
	err := pool.QueryRow(ctx, `
    SELECT id, name, root_keyword, status, target_kind, created_by FROM datasets WHERE id = $1`, datasetID).
		Scan(&row.ID, &row.Name, &row.RootKeyword, &row.Status, &row.TargetKind, &row.OwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, fmt.Errorf("dataset %d 不存在", datasetID)
	}
	if err != nil {
		return row, err
	}
	return row, nil
}

// projectNameTaken 判断工作区里是否已有同名项目。
func projectNameTaken(ctx context.Context, deps ImportDeps, options ImportOptions, dataset legacyDatasetRow) (bool, error) {
	var conflict bool
	err := deps.Pool.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM projects WHERE workspace_id = $1 AND name = $2)`,
		options.WorkspaceID, dataset.Name).Scan(&conflict)
	return conflict, err
}

// resolveOrCreateProject 解析或创建 legacy-origin 项目。
//
// 同名项目存在时**报错**而不是自动改名：自动改名会让「旧 dataset 与项目
// 的对应关系」变成只有系统知道的事，而运维在界面上看到两个名字相似的项目
// 无法判断哪个是哪一个（T30 的 conflict 决策就说的是这个）。
func resolveOrCreateProject(ctx context.Context, deps ImportDeps, options ImportOptions, dataset legacyDatasetRow) (int64, error) {
	var conflict bool
	if err := deps.Pool.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM projects WHERE workspace_id = $1 AND name = $2)`,
		options.WorkspaceID, dataset.Name).Scan(&conflict); err != nil {
		return 0, err
	}
	if conflict {
		return 0, fmt.Errorf(
			"工作区里已存在同名项目 %q：请先重命名其一，或用 -target-project 显式指定目标项目后重试"+
				"（自动改名会让旧数据与项目的对应关系变得不可解释）", dataset.Name)
	}
	targetKind := dataset.TargetKind
	if targetKind != model.TargetKindGRPO {
		targetKind = model.TargetKindSFT
	}
	input := model.CreateProjectInput{
		Name: dataset.Name, Goal: dataset.RootKeyword, TargetKind: targetKind,
		LegacyDatasetID: &dataset.ID,
	}
	input.Normalize()
	if err := input.Validate(); err != nil {
		return 0, err
	}
	projectOwner := options.ActorID
	if options.OwnerOverrideID > 0 {
		projectOwner = options.OwnerOverrideID
	}
	project, err := deps.Projects.CreateProject(ctx, options.WorkspaceID, projectOwner, input)
	if err != nil {
		return 0, err
	}
	return project.ID, nil
}

// ensureSnapshotBatch 创建（或复用）导入快照批次。
//
// 批次状态直接标为 completed：导入的快照**本来就是完整的**（内容已经在库里），
// 让它停在 queued 会显示成「排队中」，用户会等一个永远不会发生的生成。
// 这不是伪造一次运行 —— `legacy_imports` 明确记录这批内容来自导入，
// 而「没有生成过程」的事实由 `generation_config` 为空体现。
func ensureSnapshotBatch(ctx context.Context, deps ImportDeps, options ImportOptions, dataset legacyDatasetRow, projectID int64) (int64, error) {
	var itemCount int
	if err := deps.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM questions WHERE dataset_id = $1`, dataset.ID).
		Scan(&itemCount); err != nil {
		return 0, err
	}
	if itemCount <= 0 {
		itemCount = 1
	}
	targetKind := dataset.TargetKind
	if targetKind != model.TargetKindGRPO {
		targetKind = model.TargetKindSFT
	}
	input := model.CreateBatchInput{Purpose: model.BatchPurposeScale, UnitCount: itemCount}
	input.Normalize()
	if err := input.Validate(); err != nil {
		return 0, err
	}
	projectOwner := options.ActorID
	if options.OwnerOverrideID > 0 {
		projectOwner = options.OwnerOverrideID
	}
	batch, _, err := deps.Batches.CreateBatchWithJob(ctx, projectID, projectOwner, targetKind, input, nil)
	if err != nil {
		return 0, fmt.Errorf("创建导入快照批次失败：%w", err)
	}
	return batch.ID, nil
}

// importQuestions 逐批导入问题内容。
func importQuestions(ctx context.Context, deps ImportDeps, options ImportOptions,
	dataset legacyDatasetRow, projectID, batchID int64, importRow store.LegacyImport) (store.LegacyImportCounts, int64, []ImportFailure, error) {
	counts := importRow.Counts
	cursor := importRow.Cursor
	failures := []ImportFailure{}
	if !options.Resume {
		// 不续跑时从 0 开始：内容 hash 幂等保证不会产生重复版本，
		// 而从头走一遍能修好「上次失败在中间」留下的空洞。
		cursor = 0
	}

	for {
		rows, err := deps.Pool.Query(ctx, `
      SELECT q.id, q.content,
             COALESCE(sr.chain_of_thought, ''), COALESCE(sr.answer, ''),
             COALESCE(rr.reasoning, ''), COALESCE(rr.answer_summary, ''),
             COALESCE(gp.levels, '[]'::jsonb), COALESCE(gp.judge_prompt, ''),
             COALESCE(gp.level_rubrics, '[]'::jsonb), COALESCE(gp.framework_ref, ''),
             COALESCE(rw.score, 0), COALESCE(rw.status, ''), COALESCE(rw.object_key, '')
      FROM questions q
      LEFT JOIN LATERAL (
        SELECT chain_of_thought, answer FROM sft_records
        WHERE dataset_id = q.dataset_id AND question_id = q.id
        ORDER BY id DESC LIMIT 1
      ) sr ON TRUE
      LEFT JOIN LATERAL (
        SELECT reasoning, answer_summary FROM reasoning_records
        WHERE dataset_id = q.dataset_id AND question_id = q.id
        ORDER BY id DESC LIMIT 1
      ) rr ON TRUE
      LEFT JOIN LATERAL (
        SELECT levels, judge_prompt, level_rubrics, framework_ref FROM grpo_prompts
        WHERE dataset_id = q.dataset_id AND question_id = q.id
        ORDER BY id DESC LIMIT 1
      ) gp ON TRUE
      LEFT JOIN LATERAL (
        SELECT score, status, object_key FROM reward_records
        WHERE dataset_id = q.dataset_id AND question_id = q.id
        ORDER BY id DESC LIMIT 1
      ) rw ON TRUE
      WHERE q.dataset_id = $1 AND q.id > $2
      ORDER BY q.id
      LIMIT $3`, dataset.ID, cursor, options.BatchSize)
		if err != nil {
			return counts, cursor, failures, err
		}
		type sourceItem struct {
			ID           int64
			Question     string
			Reasoning    string
			Answer       string
			GRPOReason   string
			GRPOAnswer   string
			LevelsJSON   []byte
			JudgePrompt  string
			RubricsJSON  []byte
			Framework    string
			RewardScore  float64
			RewardStatus string
			RewardObject string
		}
		items := []sourceItem{}
		for rows.Next() {
			var item sourceItem
			if err := rows.Scan(&item.ID, &item.Question, &item.Reasoning, &item.Answer,
				&item.GRPOReason, &item.GRPOAnswer, &item.LevelsJSON, &item.JudgePrompt,
				&item.RubricsJSON, &item.Framework, &item.RewardScore, &item.RewardStatus, &item.RewardObject); err != nil {
				rows.Close()
				return counts, cursor, failures, err
			}
			items = append(items, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return counts, cursor, failures, err
		}
		if len(items) == 0 {
			break
		}

		for _, item := range items {
			// Every source question gets a native batch item, including records that
			// cannot become a sample. This preserves the migration fact without
			// fabricating training content.
			// 为什么不做批量：追加版本必须是一条独立事务（它要维护
			// samples.latest_version 与只追加语义），批量化需要一个新的
			// 批量追加 store API。已记录为已知限制：导入 10 万条会明显慢，
			// 但本轮的验收项是「幂等 + 可续跑 + 可对账」，而不是吞吐。
			counts.SourceItems++
			cursor = item.ID
			targetKind := dataset.TargetKind
			if targetKind != model.TargetKindGRPO {
				targetKind = model.TargetKindSFT
			}
			sampleKey := fmt.Sprintf("legacy-%d-%d", dataset.ID, item.ID)
			batchItem, _, ensureErr := deps.Batches.EnsureBatchItem(ctx, batchID, projectID, sampleKey, nil)
			if ensureErr != nil {
				return counts, cursor, failures, ensureErr
			}
			if batchItem.Status == model.ItemStatusSucceeded || batchItem.Status == model.ItemStatusSkipped {
				counts.SkippedExisting++
				continue
			}
			var payload map[string]any
			if targetKind == model.TargetKindGRPO {
				var levels []string
				var rubrics []model.GrpoLevelRubric
				if err := json.Unmarshal(item.LevelsJSON, &levels); err != nil {
					counts.FailedItems++
					failure := fmt.Errorf("GRPO levels 无法解析：%w", err)
					_, _ = deps.Batches.CommitBatchItemFailure(ctx, batchID, batchItem.ID, model.ErrorClassInvalidJSON, failure.Error(), false)
					appendFailure(&failures, item.ID, failure)
					continue
				}
				if err := json.Unmarshal(item.RubricsJSON, &rubrics); err != nil {
					counts.FailedItems++
					failure := fmt.Errorf("GRPO level_rubrics 无法解析：%w", err)
					_, _ = deps.Batches.CommitBatchItemFailure(ctx, batchID, batchItem.ID, model.ErrorClassInvalidJSON, failure.Error(), false)
					appendFailure(&failures, item.ID, failure)
					continue
				}
				if err := model.ValidateGRPOSamplePayload(levels, rubrics); err != nil || strings.TrimSpace(item.JudgePrompt) == "" {
					counts.SkippedNoContent++
					message := "旧 GRPO 题目缺少可迁移的评分提示或量表内容"
					if err != nil {
						message = "旧 GRPO 题目内容不满足新样本约束：" + err.Error()
					}
					if _, skipErr := deps.Batches.MarkBatchItemSkipped(ctx, batchID, batchItem.ID, message); skipErr != nil {
						return counts, cursor, failures, skipErr
					}
					continue
				}
				payload = map[string]any{
					"question": item.Question, "judgePrompt": item.JudgePrompt,
					"levels": levels, "levelRubrics": rubrics, "frameworkRef": item.Framework,
					"legacySource": DatasetSourceKey(dataset.ID), "legacyQuestionId": item.ID,
				}
			} else {
				reasoning := item.Reasoning
				answer := item.Answer
				// Older datasets often stored only the reasoning table. Preserve it
				// as the new SFT reasoning/answer fields instead of discarding it.
				if strings.TrimSpace(reasoning) == "" {
					reasoning = item.GRPOReason
				}
				if strings.TrimSpace(answer) == "" {
					answer = item.GRPOAnswer
				}
				if strings.TrimSpace(reasoning) == "" && strings.TrimSpace(answer) == "" {
					counts.SkippedNoContent++
					if _, skipErr := deps.Batches.MarkBatchItemSkipped(ctx, batchID, batchItem.ID, "旧 SFT 题目没有 reasoning 或 answer，未伪造训练内容"); skipErr != nil {
						return counts, cursor, failures, skipErr
					}
					continue
				}
				payload = map[string]any{
					"question": item.Question, "reasoning": reasoning, "answer": answer,
					"legacySource": DatasetSourceKey(dataset.ID), "legacyQuestionId": item.ID,
				}
			}
			if item.RewardStatus != "" {
				payload["legacyReward"] = map[string]any{
					"score": item.RewardScore, "status": item.RewardStatus, "objectKey": item.RewardObject,
				}
			}
			contentHash, hashErr := model.ContentHash(payload)
			if hashErr != nil {
				counts.FailedItems++
				_, _ = deps.Batches.CommitBatchItemFailure(ctx, batchID, batchItem.ID, model.ErrorClassSchema, hashErr.Error(), false)
				appendFailure(&failures, item.ID, hashErr)
				continue
			}
			exists, existsErr := deps.Imports.SampleVersionContentHashExists(ctx, projectID, sampleKey, contentHash)
			if existsErr != nil {
				return counts, cursor, failures, existsErr
			}
			if exists {
				// 同一份内容已经在项目里：跳过（重复执行零重复导入的落点）。
				counts.SkippedExisting++
				if _, skipErr := deps.Batches.MarkBatchItemSkipped(ctx, batchID, batchItem.ID, "相同内容 hash 已存在，未重复追加版本"); skipErr != nil {
					return counts, cursor, failures, skipErr
				}
				continue
			}
			_, committed, appendErr := deps.Batches.CommitBatchItemSuccess(ctx, batchID, projectID, batchItem.ID, store.AppendSampleVersionInput{
				ProjectID: projectID, SampleKey: sampleKey, TargetKind: targetKind,
				Title: item.Question, Payload: payload, BatchID: &batchID,
				Attempt: 1, CreatedBy: importOwnerID(options),
			})
			if appendErr != nil {
				counts.FailedItems++
				_, _ = deps.Batches.CommitBatchItemFailure(ctx, batchID, batchItem.ID, model.ErrorClassSchema, appendErr.Error(), false)
				appendFailure(&failures, item.ID, appendErr)
				continue
			}
			if committed {
				counts.ImportedVersions++
			} else {
				counts.SkippedExisting++
			}
		}

		// 每批保存进度：中途崩溃时续跑不必从头开始（也从侧面记录了一致性水位）。
		if err := deps.Imports.SaveProgress(ctx, importRow.ID, cursor, counts); err != nil {
			return counts, cursor, failures, err
		}
	}
	return counts, cursor, failures, nil
}

func importOwnerID(options ImportOptions) *int64 {
	owner := options.ActorID
	if options.OwnerOverrideID > 0 {
		owner = options.OwnerOverrideID
	}
	return &owner
}

func appendFailure(failures *[]ImportFailure, sourceID int64, err error) {
	if len(*failures) >= maxRecordedFailures {
		return
	}
	*failures = append(*failures, ImportFailure{SourceID: sourceID, Reason: err.Error()})
}

// reconcileDataset 采集一侧的对账数据（旧侧数量 + 项目侧数量 + 内容摘要）。
func reconcileDataset(ctx context.Context, pool *pgxpool.Pool, dataset legacyDatasetRow) (Reconciliation, error) {
	var result Reconciliation
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM questions WHERE dataset_id = $1`, dataset.ID).
		Scan(&result.Questions); err != nil {
		return result, err
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sft_records WHERE dataset_id = $1`, dataset.ID).
		Scan(&result.SFTRecords); err != nil {
		return result, err
	}
	for query, target := range map[string]*int64{
		`SELECT COUNT(*) FROM reasoning_records WHERE dataset_id = $1`: &result.ReasoningRecords,
		`SELECT COUNT(*) FROM grpo_prompts WHERE dataset_id = $1`:      &result.GRPOPrompts,
		`SELECT COUNT(*) FROM reward_records WHERE dataset_id = $1`:    &result.RewardRecords,
		`SELECT COUNT(*) FROM artifacts WHERE dataset_id = $1`:         &result.Artifacts,
	} {
		if err := pool.QueryRow(ctx, query, dataset.ID).Scan(target); err != nil {
			return result, err
		}
	}
	// 项目侧数量按**确定性 sample_key 前缀**统计：项目里可能还有用户后来自己生成的
	// 内容，把它们算进「导入结果」会让对账数字对不上。
	prefix := fmt.Sprintf("legacy-%d-%%", dataset.ID)
	if err := pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM samples WHERE sample_key LIKE $1`, prefix).Scan(&result.Samples); err != nil {
		return result, err
	}
	if err := pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM sample_versions sv JOIN samples s ON s.id = sv.sample_id
    WHERE s.sample_key LIKE $1`, prefix).Scan(&result.Versions); err != nil {
		return result, err
	}
	var digest string
	if err := pool.QueryRow(ctx, `
    SELECT COALESCE(string_agg(sv.content_hash, ',' ORDER BY s.sample_key, sv.version), '')
    FROM sample_versions sv JOIN samples s ON s.id = sv.sample_id
    WHERE s.sample_key LIKE $1`, prefix).Scan(&digest); err != nil {
		return result, err
	}
	if digest != "" {
		hash, err := model.ContentHash(digest)
		if err != nil {
			return result, err
		}
		result.ContentHash = hash
	}
	return result, nil
}

func derefInt64Ptr(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
