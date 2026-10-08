package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T05 的批次、单元与不可变样本版本不变量。
//
// 为什么必须连真实 Postgres：全部核心断言都是**数据库约束与并发**——
//   - 同项目三个批次是三个独立 ID，互不覆盖；
//   - 保存新蓝图不改旧快照（快照是行内 hash，不是指针）；
//   - 重放相同成功项不增加样本（靠 UNIQUE (batch_id, item_key) + 条件更新）；
//   - 两个 worker 抢同一单元时恰有一个真正执行；
//   - 跨项目快照引用被复合外键拒绝。
// 纯内存测试无法证明任何一条。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

// batchFixture 是一个项目 + 一份完整的设计版本快照。
type batchFixture struct {
	pool        *pgxpool.Pool
	projectID   int64
	editorID    int64
	workspaceID int64
	batches     *BatchStore
	documents   *DocumentStore
	projects    *ProjectStore
	blueprintID int64
	standardID  int64
	coverageID  int64
	mappingID   int64
	policyID    int64
}

// newBatchFixture 建项目并保存一套完整的版本化文档，返回各版本行 ID。
func newBatchFixture(t *testing.T) batchFixture {
	t.Helper()
	pool := newStudioTestPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", os.Getpid(), t.Name())
	workspaceID := seedAuthzWorkspace(t, pool, suffix)
	editorID := seedStudioUser(t, pool, "batch-editor-"+suffix)

	projects := NewProjectStore(pool)
	project, err := projects.CreateProject(ctx, workspaceID, editorID, validProjectInput("批次测试项目"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	documents := NewDocumentStore(pool)
	save := func(kind model.DocumentKind, payload any, reason string) int64 {
		t.Helper()
		_, version, err := documents.SaveVersion(ctx, project.ID, kind, editorID,
			SaveDocumentVersionInput{ChangeReason: reason, Payload: payload})
		if err != nil {
			t.Fatalf("save %s: %v", kind, err)
		}
		return version.ID
	}

	coverageVersionID := save(model.KindCoverage, coveragePayload(), "初版覆盖")
	standardVersionID := save(model.KindStandard, standardPayload("识别约束"), "初版标准")
	mappingPayload := model.MappingPayload{
		SchemaVersion: model.SchemaVersionFor(model.KindMapping),
		Format:        model.ExportFormatJSONL,
		Fields: []model.MappingField{
			{TargetField: "question", SourceField: "question", Required: true},
			{TargetField: "reasoning", SourceField: "reasoning", Required: true},
			{TargetField: "answer", SourceField: "answer", Required: true},
		},
	}
	mappingVersionID := save(model.KindMapping, mappingPayload, "初版映射")
	policyPayload := model.QualityPolicyPayload{
		SchemaVersion: model.SchemaVersionFor(model.KindQualityPolicy),
		Rules: []model.QualityRule{{
			ID: "r1", Name: "空答案检查", MatchType: model.RuleMatchFieldCheck,
			Expression: "len>0", Field: "answer", Severity: model.RuleSeverityError,
			SuggestedAction: model.RuleActionReview,
		}},
	}
	policyVersionID := save(model.KindQualityPolicy, policyPayload, "初版质量策略")

	// 蓝图放在最后：它引用上面几个版本。
	blueprintPayload := blueprintPayload(coverageVersionID, standardVersionID)
	// 普通批次现在要求在入队前冻结一个可执行的生成配置；这里使用
	// 非秘密的测试连接标识即可，真实凭证解析属于 worker/连接 store。
	blueprintPayload.Nodes.Generation.ModelConnectionID = 1
	blueprintPayload.Nodes.Rules.QualityPolicyVersionID = policyVersionID
	blueprintPayload.Nodes.Delivery.MappingVersionID = mappingVersionID
	blueprintPayload.Nodes.Delivery.Format = model.ExportFormatJSONL
	blueprintVersionID := save(model.KindBlueprint, blueprintPayload, "初版蓝图")

	return batchFixture{
		pool: pool, projectID: project.ID, editorID: editorID, workspaceID: workspaceID,
		batches: NewBatchStore(pool), documents: documents, projects: projects,
		blueprintID: blueprintVersionID, standardID: standardVersionID,
		coverageID: coverageVersionID, mappingID: mappingVersionID, policyID: policyVersionID,
	}
}

// TestBatchRejectsIncompleteExecutionSnapshot verifies that an incomplete
// production request fails before batches/jobs can be persisted.
func TestBatchRejectsIncompleteExecutionSnapshot(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	_, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		model.CreateBatchInput{Purpose: model.BatchPurposePilot, UnitCount: 2})
	if err == nil {
		t.Fatal("缺少蓝图版本的生产批次必须在入队前被拒绝")
	}
	fieldErrors, ok := model.HasFieldErrors(err)
	if !ok || len(fieldErrors) == 0 || fieldErrors[0].Field != "blueprintVersionId" {
		t.Fatalf("缺少蓝图必须返回 blueprintVersionId 字段错误，实际 %v", err)
	}

	invalidBlueprint := blueprintPayload(fixture.coverageID, fixture.standardID)
	invalidBlueprint.Nodes.Generation.ModelConnectionID = 0
	invalidBlueprint.Nodes.Rules.QualityPolicyVersionID = fixture.policyID
	invalidBlueprint.Nodes.Delivery.MappingVersionID = fixture.mappingID
	invalidBlueprint.Nodes.Delivery.Format = model.ExportFormatJSONL
	_, invalidVersion, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindBlueprint, fixture.editorID,
		SaveDocumentVersionInput{ExpectedRevision: 2, ChangeReason: "缺少连接", Payload: invalidBlueprint})
	if err != nil {
		t.Fatalf("save invalid blueprint: %v", err)
	}
	_, err = fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		model.CreateBatchInput{Purpose: model.BatchPurposePilot, BlueprintVersionID: invalidVersion.ID, UnitCount: 2})
	if err == nil {
		t.Fatal("缺少模型连接的生产批次必须在入队前被拒绝")
	}
	fieldErrors, ok = model.HasFieldErrors(err)
	if !ok || !hasFieldError(fieldErrors, "generationConfig.modelConnectionId") {
		t.Fatalf("缺少模型连接必须返回字段错误，实际 %v", err)
	}

	var count int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM batches WHERE project_id = $1`, fixture.projectID).Scan(&count); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒请求不得写入 queued 批次，实际已有 %d 条", count)
	}
}

func hasFieldError(errs model.FieldErrors, field string) bool {
	for _, err := range errs {
		if err.Field == field {
			return true
		}
	}
	return false
}

// createBatchInput 组装一份引用完整快照的创建请求。
//
// 配额**大于等于**计划量时必须扩大覆盖矩阵：issue #190 之后
// `plannedUnits > 覆盖可产出量` 会被拒绝，因此这里让覆盖容量始终不少于
// 计划量，使各用例仍能测到它真正关心的性质（计费、事件、并发、幂等…），
// 而不是被容量校验挡住。
func (fixture batchFixture) createBatchInput(t *testing.T, purpose string, units int) model.CreateBatchInput {
	t.Helper()
	return fixture.createBatchInputWithCoverage(purpose, units, func(coverage *model.CoveragePayload) {
		if len(coverage.Domains) == 0 || len(coverage.Domains[0].Directions) == 0 {
			return
		}
		granted := model.CoverageCapacity(*coverage)
		if units > granted {
			coverage.Domains[0].Directions[0].Quota += units - granted
		}
	})
}

// createBatchInputWithCoverage 允许用例自定义覆盖矩阵，用于**刻意**构造
// 「计划量超过可产出量」的场景（issue #190 的回归用例）。
func (fixture batchFixture) createBatchInputWithCoverage(purpose string, units int,
	mutate func(coverage *model.CoveragePayload)) model.CreateBatchInput {
	coverage := coveragePayload()
	mutate(&coverage)
	coverageID := fixture.saveCoverage(coverage)
	input := model.CreateBatchInput{
		Purpose:                purpose,
		BlueprintVersionID:     fixture.blueprintVersionReferencing(coverageID),
		CoverageVersionID:      coverageID,
		StandardVersionID:      fixture.standardID,
		QualityPolicyVersionID: fixture.policyID,
		MappingVersionID:       fixture.mappingID,
		UnitCount:              units,
	}
	return input
}

// saveCoverage 保存一份覆盖版本并返回版本行 ID。
//
// 保存失败直接 panic：这些是**测试夹具**，调用方没有 `*testing.T`
// （`createBatchInput` 的签名要兼容既有 20 处调用）。夹具失败本身就是测试
// 环境的错误，静默返回 0 会变成「版本不存在」的误导性失败。
func (fixture batchFixture) saveCoverage(coverage model.CoveragePayload) int64 {
	return fixture.saveVersionForCase(model.KindCoverage, coverage, "批次用例覆盖")
}

// saveVersionForCase 保存一版用例文档，容忍乐观锁冲突。
//
// 为什么需要「容忍」：夹具会在**同一个测试**里多次保存同一类文档（每个用例
// 要一份容量不同的覆盖矩阵），而 `row_version` 已经被上一次保存推进过。
// 注入一个假的 revision 会让夹具失败与产品行为混淆；重读头记录再重试
// 才是真实编辑者的行为（前端也是先拉头记录再保存）。
func (fixture batchFixture) saveVersionForCase(kind model.DocumentKind, payload any, reason string) int64 {
	for attempt := 0; attempt < 3; attempt++ {
		document, err := fixture.documents.GetDocument(context.Background(), fixture.projectID, kind, DefaultLogicalID)
		revision := int64(0)
		if err == nil {
			revision = document.RowVersion
		}
		_, version, saveErr := fixture.documents.SaveVersion(context.Background(), fixture.projectID,
			kind, fixture.editorID,
			SaveDocumentVersionInput{ExpectedRevision: revision, ChangeReason: reason, Payload: payload})
		if saveErr == nil {
			return version.ID
		}
		if !errors.Is(saveErr, ErrRevisionConflict) {
			panic("batch fixture: save " + string(kind) + ": " + saveErr.Error())
		}
	}
	panic("batch fixture: save " + string(kind) + ": 乐观锁冲突重试 3 次仍然失败")
}

// blueprintVersionReferencing 保存一份「覆盖节点指向给定版本」的蓝图，返回蓝图版本行 ID。
//
// 为什么要重新保存蓝图：批次快照的覆盖版本来自**蓝图节点的引用**（未显式
// 传入 coverageVersionId 时由 resolveBatchSnapshotTx 补齐）。不更新蓝图就会
// 出现「传入的覆盖版本与蓝图引用的覆盖版本是两个」的不一致。
func (fixture batchFixture) blueprintVersionReferencing(coverageVersionID int64) int64 {
	payload := blueprintPayload(coverageVersionID, fixture.standardID)
	payload.Nodes.Generation.ModelConnectionID = 1
	payload.Nodes.Rules.QualityPolicyVersionID = fixture.policyID
	payload.Nodes.Delivery.MappingVersionID = fixture.mappingID
	payload.Nodes.Delivery.Format = model.ExportFormatJSONL
	return fixture.saveVersionForCase(model.KindBlueprint, payload, "批次用例蓝图")
}

// sftPayload 返回一份通过校验的 SFT 样本内容。
func sftPayload(question string) map[string]any {
	return map[string]any{
		"question":  question,
		"reasoning": "先识别约束，再逐步推导，最后核对边界。",
		"answer":    "结论：" + question,
	}
}

// TestBatchCreatesIndependentIDs 覆盖 T05 验收项：
// 「同一项目创建两次 pilot 和一次 scale 有三个独立 ID」。
//
// 这条正是 #160 §1 批评的缺陷形态：旧 generation_runs 对 (dataset_id, stage)
// 有活跃唯一约束，于是同项目的第二个批次无法存在，只能覆盖第一个。
func TestBatchCreatesIndependentIDs(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	pilotA, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 5))
	if err != nil {
		t.Fatalf("create pilot A: %v", err)
	}
	pilotB, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 8))
	if err != nil {
		t.Fatalf("create pilot B: %v", err)
	}
	scale, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposeScale, 12))
	if err != nil {
		t.Fatalf("create scale: %v", err)
	}

	if pilotA.ID == pilotB.ID || pilotB.ID == scale.ID || pilotA.ID == scale.ID {
		t.Fatalf("三个批次必须有独立 ID，实际 %d / %d / %d", pilotA.ID, pilotB.ID, scale.ID)
	}
	if pilotA.Purpose != model.BatchPurposePilot || scale.Purpose != model.BatchPurposeScale {
		t.Fatalf("用途必须被保留：%s / %s", pilotA.Purpose, scale.Purpose)
	}
	if pilotA.PlannedUnits != 5 || pilotB.PlannedUnits != 8 || scale.PlannedUnits != 12 {
		t.Fatalf("计划单元数必须独立：%d / %d / %d",
			pilotA.PlannedUnits, pilotB.PlannedUnits, scale.PlannedUnits)
	}

	// 列出时必须都能看到（不是只留最后一个）。
	listed, err := fixture.batches.ListBatches(ctx, BatchListQuery{ProjectID: fixture.projectID, Limit: 10})
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("三个批次都必须能被列出，实际 %d 个", len(listed))
	}
}

// TestBatchSnapshotIsFrozenAgainstNewBlueprint 覆盖 T05 验收项
// 「保存新蓝图不改旧快照」。
//
// 机制：批次存的是**行内 hash**（列），不是指向「当前采用」的指针。
// 因此后续保存新蓝图版本不影响已提交批次 —— 这是「可复现」的地基。
func TestBatchSnapshotIsFrozenAgainstNewBlueprint(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 3))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	originalHash := batch.Snapshot.BlueprintContentHash
	originalConfig := batch.GenerationConfig
	if originalHash == "" {
		t.Fatal("批次必须记录蓝图内容 hash（否则无法核对快照是否变过）")
	}
	if originalConfig.Concurrency != 8 || originalConfig.MaxTokens != 4096 {
		t.Fatalf("生成配置必须从蓝图快照解出，实际 %+v", originalConfig)
	}

	// 保存一个新蓝图版本，把并发从 8 改成 16。
	newBlueprint := blueprintPayload(fixture.coverageID, fixture.standardID)
	newBlueprint.Nodes.Generation.ModelConnectionID = 1
	newBlueprint.Nodes.Rules.QualityPolicyVersionID = fixture.policyID
	newBlueprint.Nodes.Delivery.MappingVersionID = fixture.mappingID
	newBlueprint.Nodes.Delivery.Format = model.ExportFormatJSONL
	newBlueprint.Nodes.Generation.Concurrency = 16
	newBlueprint.Nodes.Generation.MaxTokens = 8192
	// 用夹具的容错保存（重读头记录的 row_version 后重试）而不是写死
	// ExpectedRevision：写死会让「夹具在别处多存了一版」变成这里的假失败。
	newVersionID := fixture.saveVersionForCase(model.KindBlueprint, newBlueprint, "提高并发")
	newVersion, err := fixture.documents.GetVersionByID(ctx, newVersionID)
	if err != nil {
		t.Fatalf("read new blueprint version: %v", err)
	}

	reloaded, err := fixture.batches.GetBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if reloaded.Snapshot.BlueprintContentHash != originalHash {
		t.Fatalf("保存新蓝图不得改变旧快照：期望 %s，实际 %s",
			originalHash, reloaded.Snapshot.BlueprintContentHash)
	}
	// 快照必须仍指向**创建批次时**那一版蓝图行，而不是夹具最初的版本：
	// issue #190 之后夹具会为每个用例保存一份容量匹配的覆盖版本，
	// 也就顺带保存了一版引用它的蓝图。
	if reloaded.Snapshot.BlueprintVersionID != batch.Snapshot.BlueprintVersionID {
		t.Fatalf("快照必须仍指向创建时的版本行 %d，实际 %d",
			batch.Snapshot.BlueprintVersionID, reloaded.Snapshot.BlueprintVersionID)
	}
	if reloaded.GenerationConfig.Concurrency != 8 || reloaded.GenerationConfig.MaxTokens != 4096 {
		t.Fatalf("批次的生成配置快照不得被新蓝图改写，实际 %+v", reloaded.GenerationConfig)
	}
	// 新版本确实存在且不同，证明「保存成功了但批次没变」不是因为保存失败。
	if newVersion.ContentHash == originalHash {
		t.Fatal("前置条件不成立：新蓝图的内容 hash 应当与旧的不同")
	}
	// 新批次必须拿到新配置（否则「改配置」这件事永远不会生效）。
	freshBatch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		model.CreateBatchInput{
			Purpose: model.BatchPurposePilot, BlueprintVersionID: newVersion.ID, UnitCount: 2,
		})
	if err != nil {
		t.Fatalf("create batch with new blueprint: %v", err)
	}
	if freshBatch.GenerationConfig.Concurrency != 16 {
		t.Fatalf("新批次必须拿到新蓝图的配置（改配置要新建批次），实际 %+v", freshBatch.GenerationConfig)
	}
}

// TestBatchSnapshotCompletesReferencesFromBlueprint verifies the production
// handoff contract: the planner may send only a blueprint version, while the
// server still freezes the versions that blueprint references. This prevents
// a page-level default from silently creating a partial batch snapshot.
func TestBatchSnapshotCompletesReferencesFromBlueprint(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()
	input := model.CreateBatchInput{
		Purpose:            model.BatchPurposePilot,
		BlueprintVersionID: fixture.blueprintID,
		UnitCount:          2,
	}

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT, input)
	if err != nil {
		t.Fatalf("create batch with blueprint-only input: %v", err)
	}
	if batch.Snapshot.CoverageVersionID != fixture.coverageID ||
		batch.Snapshot.StandardVersionID != fixture.standardID ||
		batch.Snapshot.QualityPolicyVersionID != fixture.policyID ||
		batch.Snapshot.MappingVersionID != fixture.mappingID {
		t.Fatalf("blueprint references must be frozen into snapshot: %+v", batch.Snapshot)
	}

	// An explicit override remains authoritative for this batch; fallback must
	// only fill omitted fields rather than overwrite a deliberate choice.
	mappingDocument, err := fixture.documents.GetDocument(ctx, fixture.projectID, model.KindMapping, DefaultLogicalID)
	if err != nil {
		t.Fatalf("get mapping document: %v", err)
	}
	_, override, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindMapping,
		fixture.editorID, SaveDocumentVersionInput{ExpectedRevision: mappingDocument.RowVersion, ChangeReason: "替换映射", Payload: model.MappingPayload{
			SchemaVersion: model.SchemaVersionFor(model.KindMapping), Format: model.ExportFormatJSONL,
			Fields: []model.MappingField{{TargetField: "question", SourceField: "question", Required: true}},
		}})
	if err != nil {
		t.Fatalf("save mapping override: %v", err)
	}
	input.MappingVersionID = override.ID
	second, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT, input)
	if err != nil {
		t.Fatalf("create overridden batch: %v", err)
	}
	if second.Snapshot.MappingVersionID != override.ID {
		t.Fatalf("explicit mapping override must win, got %d want %d", second.Snapshot.MappingVersionID, override.ID)
	}
}

// TestBatchRejectsCrossProjectSnapshot 覆盖 §4.1「跨项目引用被拒绝」在批次上的表现。
func TestBatchRejectsCrossProjectSnapshot(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	// 另一个项目 + 它的蓝图。
	otherProject, err := fixture.projects.CreateProject(ctx, fixture.workspaceID, fixture.editorID, validProjectInput("另一个项目"))
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	_, foreignBlueprint, err := fixture.documents.SaveVersion(ctx, otherProject.ID, model.KindBlueprint, fixture.editorID,
		SaveDocumentVersionInput{
			ChangeReason: "别的项目蓝图",
			Payload:      blueprintPayload(0, 0),
		})
	if err != nil {
		t.Fatalf("save foreign blueprint: %v", err)
	}

	input := fixture.createBatchInput(t, model.BatchPurposePilot, 2)
	input.BlueprintVersionID = foreignBlueprint.ID
	_, err = fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT, input)
	if err == nil {
		t.Fatal("跨项目的快照引用必须被拒绝（否则会把别的项目内容打进本次交付）")
	}
	fieldErrors, ok := model.HasFieldErrors(err)
	if !ok {
		t.Fatalf("必须是字段级错误（handler 据此返回 422），实际: %v", err)
	}
	if fieldErrors[0].Field != "blueprintVersionId" {
		t.Fatalf("必须定位到 blueprintVersionId，实际 %+v", fieldErrors)
	}

	// 不得留下批次行。
	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM batches WHERE project_id = $1`, fixture.projectID).Scan(&count); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒绝的创建不得留下批次，实际 %d 个", count)
	}
}

// TestSampleVersionsAreAppendOnlyAndIndependent 覆盖 T05 验收项：
// 「同题不同批次输出不覆盖」+「原样本重生成是新版本」。
func TestSampleVersionsAreAppendOnlyAndIndependent(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	const sampleKey = "cold-chain.temperature.q1"

	// 第一次产出（批次 A）。
	batchA, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch A: %v", err)
	}
	sample, v1, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: sampleKey, TargetKind: model.TargetKindSFT,
		Title: "温控异常", BatchID: &batchA.ID, Payload: sftPayload("温控异常如何处理"),
	})
	if err != nil {
		t.Fatalf("append v1: %v", err)
	}
	if v1.Version != 1 {
		t.Fatalf("首个版本必须是 1，实际 %d", v1.Version)
	}

	// 同一批次重生成 → 新版本（v2），内容与 v1 并存。
	_, v2, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: sampleKey, TargetKind: model.TargetKindSFT,
		Title: "温控异常", BatchID: &batchA.ID, Payload: sftPayload("温控异常如何处理（第二版）"),
	})
	if err != nil {
		t.Fatalf("append v2: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("重生成必须产生新版本 2，实际 %d", v2.Version)
	}
	// 即使内容完全相同也必须是新版本：重生成消耗了预算，合并会让成本与来源失真。
	_, v3, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: sampleKey, TargetKind: model.TargetKindSFT,
		Title: "温控异常", BatchID: &batchA.ID, Payload: sftPayload("温控异常如何处理"),
	})
	if err != nil {
		t.Fatalf("append v3: %v", err)
	}
	if v3.Version != 3 {
		t.Fatalf("内容相同的重生成也必须是新版本（同一来源 trace），实际 %d", v3.Version)
	}
	if v3.ContentHash != v1.ContentHash {
		t.Fatalf("内容相同时 hash 必须相同（否则离线核对无法判断「内容没变」）：%s vs %s",
			v1.ContentHash, v3.ContentHash)
	}

	// 另一个批次产出**同一道题** → 同一个 sample 的新版本，但来源批次不同。
	batchB, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch B: %v", err)
	}
	_, v4, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: sampleKey, TargetKind: model.TargetKindSFT,
		Title: "温控异常", BatchID: &batchB.ID, Payload: sftPayload("温控异常如何处理（方案 B）"),
	})
	if err != nil {
		t.Fatalf("append v4: %v", err)
	}
	if v4.Version != 4 {
		t.Fatalf("同一道题在同一项目内是一个 sample 的连续版本，实际 %d", v4.Version)
	}
	if v4.BatchID == nil || *v4.BatchID != batchB.ID {
		t.Fatalf("版本必须记录自己的来源批次，实际 %+v", v4.BatchID)
	}
	if v4.ContentHash == v1.ContentHash {
		t.Fatal("不同方案的产出必须有不同 hash")
	}

	// 历史必须全部保留且可读，来源各不相同。
	versions, err := fixture.batches.ListSampleVersions(ctx, fixture.projectID, sample.ID, 10)
	if err != nil {
		t.Fatalf("ListSampleVersions: %v", err)
	}
	if len(versions) != 4 {
		t.Fatalf("四个版本必须全部保留，实际 %d 个", len(versions))
	}
	if versions[0].Version != 4 {
		t.Fatalf("列表必须按版本倒序（最新在前），实际首项 %d", versions[0].Version)
	}
	// v1 的内容必须未被后续写入改动。
	reloadedV1, err := fixture.batches.GetSampleVersion(ctx, fixture.projectID, sample.ID, 1)
	if err != nil {
		t.Fatalf("GetSampleVersion(v1): %v", err)
	}
	if reloadedV1.ContentHash != v1.ContentHash {
		t.Fatalf("旧版本内容不得被覆盖：%s vs %s", reloadedV1.ContentHash, v1.ContentHash)
	}
	if !json.Valid(reloadedV1.Payload) {
		t.Fatalf("v1 payload 必须是合法 JSON：%s", reloadedV1.Payload)
	}
}

// TestReplayingSuccessfulItemDoesNotDuplicateSample 是 T05 最关键的不变量：
// 「重放相同成功项不增加样本」。
//
// 机制（不是应用层判断）：batch_items 的 UNIQUE (batch_id, item_key) 保证
// 同一单元只有一行，CommitBatchItemSuccess 在单元已 succeeded 时**回放既有版本**
// 而不追加新版本。因此 worker 重复投递消息不会产生第二个样本版本。
func TestReplayingSuccessfulItemDoesNotDuplicateSample(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	const itemKey = "cold-chain.temperature.q1"
	item, created, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, itemKey, nil)
	if err != nil {
		t.Fatalf("EnsureBatchItem: %v", err)
	}
	if !created {
		t.Fatal("首次 EnsureBatchItem 必须报告 created=true")
	}

	// 再次 Ensure 必须拿到同一行（不是新行）。
	again, createdAgain, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, itemKey, nil)
	if err != nil {
		t.Fatalf("EnsureBatchItem again: %v", err)
	}
	if createdAgain {
		t.Fatal("重复 EnsureBatchItem 不得创建新行")
	}
	if again.ID != item.ID {
		t.Fatalf("必须是同一行：%d vs %d", item.ID, again.ID)
	}

	versionInput := AppendSampleVersionInput{
		SampleKey: itemKey, TargetKind: model.TargetKindSFT,
		Title: "温控异常", Payload: sftPayload("温控异常如何处理"),
	}
	first, appended, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, versionInput)
	if err != nil {
		t.Fatalf("commit first success: %v", err)
	}
	if !appended {
		t.Fatal("首次提交必须产生新版本")
	}

	// 重放三次相同的成功提交。
	for i := 0; i < 3; i++ {
		replayed, appendedAgain, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, versionInput)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if appendedAgain {
			t.Fatalf("第 %d 次重放不得追加新版本（重放相同成功项不增加样本）", i+1)
		}
		if replayed.ID != first.ID {
			t.Fatalf("重放必须回放同一版本：%d vs %d", first.ID, replayed.ID)
		}
	}

	// 事实核对：恰好一个样本版本。
	var versionCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM sample_versions WHERE project_id = $1`, fixture.projectID).Scan(&versionCount); err != nil {
		t.Fatalf("count sample versions: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("重放后必须恰好有 1 个样本版本，实际 %d 个", versionCount)
	}
}

// TestClaimBatchItemPreventsDoubleExecution 覆盖并发关键路径：
// 两个 worker 抢同一单元时**恰有一个**获得执行权。
//
// 若两个都通过检查，同一个单元会被调用两次外部模型 —— 既多花钱，
// 又会产生两个内容版本（用户看到「我只跑了一次却有两个结果」）。
func TestClaimBatchItemPreventsDoubleExecution(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-claim", nil)
	if err != nil {
		t.Fatalf("EnsureBatchItem: %v", err)
	}

	const workers = 8
	type outcome struct {
		index  int
		claim  bool
		itemID int64
		err    error
	}
	results := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			claimed, ok, err := fixture.batches.ClaimBatchItemForAttempt(ctx, item.ID)
			results <- outcome{index: idx, claim: ok, itemID: claimed.ID, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("worker %d 抢占有误: %v", result.index, result.err)
		}
		if result.claim {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("8 个 worker 必须恰有一个获得执行权（否则会重复调用外部模型），实际 %d 个", claimed)
	}
}

// TestCommitFailureDoesNotOverwriteSuccess 守住一条易被忽略的破坏性路径：
// 迟到的失败上报不得把**已成功**的单元改成失败。
//
// 触发场景很真实：worker A 成功提交后进程被杀（消息未确认），
// 消息被重投给 worker B，B 因超时失败上报 —— 若允许覆盖，
// 用户会看到「本来成功的样本变成失败」，而内容其实已经产出。
func TestCommitFailureDoesNotOverwriteSuccess(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-late-failure", nil)
	if err != nil {
		t.Fatalf("EnsureBatchItem: %v", err)
	}
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, AppendSampleVersionInput{
		SampleKey: "q-late-failure", TargetKind: model.TargetKindSFT,
		Payload: sftPayload("迟到失败测试"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}

	updated, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, item.ID, model.ErrorClassTimeout, "迟到的超时", true)
	if err != nil {
		t.Fatalf("CommitBatchItemFailure: %v", err)
	}
	if updated {
		t.Fatal("迟到的失败上报必须被拒绝（不得覆盖已成功单元）")
	}

	items, err := fixture.batches.ListBatchItems(ctx, batch.ID, "", 10)
	if err != nil {
		t.Fatalf("ListBatchItems: %v", err)
	}
	if len(items) != 1 || items[0].Status != model.ItemStatusSucceeded {
		t.Fatalf("单元必须仍是 succeeded，实际 %+v", items)
	}
}

// TestRetryFailedResetsOnlyRetryableItems 覆盖 T13 验收项在 store 层的语义：
// 「恢复只提交失败/未完成项，成功内容保留」+「不可重试的失败不动」。
func TestRetryFailedResetsOnlyRetryableItems(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 3))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	// 三个单元：一个成功、一个可重试失败、一个不可重试失败。
	succeeded, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-ok", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, succeeded.ID, AppendSampleVersionInput{
		SampleKey: "q-ok", TargetKind: model.TargetKindSFT, Payload: sftPayload("成功项"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}
	retryable, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-retry", nil)
	if _, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, retryable.ID, model.ErrorClassRateLimited, "限流", true); err != nil {
		t.Fatalf("commit retryable failure: %v", err)
	}
	permanent, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-bad-schema", nil)
	if _, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, permanent.ID, model.ErrorClassSchema, "schema 不合规", false); err != nil {
		t.Fatalf("commit permanent failure: %v", err)
	}

	reset, err := fixture.batches.MarkRetryableItemsPending(ctx, fixture.projectID, batch.ID, fixture.editorID)
	if err != nil {
		t.Fatalf("MarkRetryableItemsPending: %v", err)
	}
	if reset != 1 {
		t.Fatalf("只应重置 1 个可重试失败项，实际 %d 个", reset)
	}

	counts, err := fixture.batches.CountBatchItemsByStatus(ctx, batch.ID)
	if err != nil {
		t.Fatalf("CountBatchItemsByStatus: %v", err)
	}
	if counts[model.ItemStatusSucceeded] != 1 {
		t.Fatalf("成功项必须保留，实际 %+v", counts)
	}
	if counts[model.ItemStatusPending] != 1 {
		t.Fatalf("可重试失败项必须变成 pending，实际 %+v", counts)
	}
	if counts[model.ItemStatusFailed] != 1 {
		t.Fatalf("不可重试失败项必须保持 failed，实际 %+v", counts)
	}

	// 反复点击恢复：不应重复重置（幂等）。
	again, err := fixture.batches.MarkRetryableItemsPending(ctx, fixture.projectID, batch.ID, fixture.editorID)
	if err != nil {
		t.Fatalf("second MarkRetryableItemsPending: %v", err)
	}
	if again != 0 {
		t.Fatalf("第二次恢复不得再重置任何项（幂等），实际 %d 个", again)
	}
}

// TestBatchControlStateMachine 覆盖 §4.2 的暂停/恢复状态转换与非法转换 409。
func TestBatchControlStateMachine(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 2))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	// 未暂停时恢复 → 非法。
	if _, err := fixture.batches.ResumeBatch(ctx, fixture.projectID, batch.ID, fixture.editorID); !errors.Is(err, ErrBatchNotControllable) {
		t.Fatalf("未暂停时恢复必须返回 ErrBatchNotControllable，实际: %v", err)
	}

	// 暂停 → pause_requested（**不是** paused：在途请求仍在跑，§2.4）。
	paused, err := fixture.batches.PauseBatch(ctx, fixture.projectID, batch.ID, fixture.editorID)
	if err != nil {
		t.Fatalf("PauseBatch: %v", err)
	}
	if paused.ControlState != model.BatchControlPauseRequested || paused.Status != model.BatchStatusPauseRequested {
		t.Fatalf("暂停必须进入 pause_requested（不撤回在途请求），实际 %s/%s",
			paused.Status, paused.ControlState)
	}

	// 重复暂停 → 非法（避免用户以为「多点几次会更彻底地停」）。
	if _, err := fixture.batches.PauseBatch(ctx, fixture.projectID, batch.ID, fixture.editorID); !errors.Is(err, ErrBatchNotControllable) {
		t.Fatalf("重复暂停必须返回 ErrBatchNotControllable，实际: %v", err)
	}

	// 恢复 → run。
	resumed, err := fixture.batches.ResumeBatch(ctx, fixture.projectID, batch.ID, fixture.editorID)
	if err != nil {
		t.Fatalf("ResumeBatch: %v", err)
	}
	if resumed.ControlState != model.BatchControlRun {
		t.Fatalf("恢复后控制状态必须是 run，实际 %s", resumed.ControlState)
	}

	// 事件时间线必须记录两次控制动作，且序号单调。
	events, err := fixture.batches.ListBatchEvents(ctx, batch.ID, 20)
	if err != nil {
		t.Fatalf("ListBatchEvents: %v", err)
	}
	types := map[string]int{}
	for _, event := range events {
		types[event.EventType]++
	}
	if types[model.BatchEventQueued] != 1 || types[model.BatchEventPaused] != 1 || types[model.BatchEventResumed] != 1 {
		t.Fatalf("事件必须是 queued×1 + paused×1 + resumed×1，实际 %+v", types)
	}
	// 事件载荷必须带 inFlight（契约 §5）。
	for _, event := range events {
		if event.EventType != model.BatchEventPaused && event.EventType != model.BatchEventResumed {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(event.Detail, &detail); err != nil {
			t.Fatalf("event detail 必须是 JSON: %v", err)
		}
		if _, ok := detail["inFlight"]; !ok {
			t.Fatalf("%s 事件必须带 inFlight（暂停时用户最需要知道还有几个在途）", event.EventType)
		}
	}
}

// TestCompletedBatchCannotBeControlled 覆盖「终态不可再控制」。
//
// 允许在已完成的批次上暂停/恢复会让它回到运行态，从而重跑已经成功的单元 ——
// 那会与已发布内容不一致（发布引用的是具体样本版本）。
func TestCompletedBatchCannotBeControlled(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	item, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-done", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, AppendSampleVersionInput{
		SampleKey: "q-done", TargetKind: model.TargetKindSFT, Payload: sftPayload("完成项"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}

	// 从事实重算聚合 → 全部成功 → completed。
	refreshed, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if refreshed.Status != model.BatchStatusCompleted {
		t.Fatalf("全部成功必须聚合为 completed，实际 %s", refreshed.Status)
	}
	if refreshed.CompletedUnits != 1 || refreshed.FailedUnits != 0 {
		t.Fatalf("聚合计数必须与事实一致，实际 completed=%d failed=%d",
			refreshed.CompletedUnits, refreshed.FailedUnits)
	}

	if _, err := fixture.batches.PauseBatch(ctx, fixture.projectID, batch.ID, fixture.editorID); !errors.Is(err, ErrBatchNotControllable) {
		t.Fatalf("已完成的批次不得暂停，实际: %v", err)
	}
	if _, err := fixture.batches.MarkRetryableItemsPending(ctx, fixture.projectID, batch.ID, fixture.editorID); !errors.Is(err, ErrBatchNotControllable) {
		t.Fatalf("已完成的批次不得恢复失败项，实际: %v", err)
	}
}

// TestPartialFailureAggregation 覆盖 §4.2「有失败项 → partial_failed」。
func TestPartialFailureAggregation(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 2))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	ok, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, ok.ID, AppendSampleVersionInput{
		SampleKey: "q-1", TargetKind: model.TargetKindSFT, Payload: sftPayload("成功"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}
	bad, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-2", nil)
	if _, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, bad.ID, model.ErrorClassProvider, "供应商错误", true); err != nil {
		t.Fatalf("commit failure: %v", err)
	}

	refreshed, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if refreshed.Status != model.BatchStatusPartialFailed {
		t.Fatalf("部分失败必须聚合为 partial_failed，实际 %s", refreshed.Status)
	}
}

// TestBatchProgressDoesNotFakeOverallPercent 覆盖 §2.6 与 issue #141 的教训：
// 多阶段时**不编造**一个「总体百分比」。
//
// 平均值会让「方向跑完、题目开始」显示成 55%，而它不是任何一个真实单位的完成度。
func TestBatchProgressDoesNotFakeOverallPercent(t *testing.T) {
	batch := model.Batch{PlannedUnits: 10, CompletedUnits: 5, InFlightUnits: 2}

	single := model.ComputeBatchProgress(batch, []model.BatchStep{
		{Phase: "generation", UnitLabel: "方向", TotalUnits: 10, DoneUnits: 5},
	})
	if single.CompletionPercent == nil || *single.CompletionPercent != 50 {
		t.Fatalf("单阶段必须给出真实百分比 50，实际 %v", single.CompletionPercent)
	}
	if single.UnitLabel != "方向" {
		t.Fatalf("单位标签必须来自阶段声明，实际 %q", single.UnitLabel)
	}

	multi := model.ComputeBatchProgress(batch, []model.BatchStep{
		{Phase: "coverage", UnitLabel: "方向", TotalUnits: 4, DoneUnits: 4},
		{Phase: "generation", UnitLabel: "问题", TotalUnits: 20, DoneUnits: 5},
	})
	if multi.CompletionPercent != nil {
		t.Fatalf("多阶段不得编造总体百分比（会让题数冒充方向数），实际 %d", *multi.CompletionPercent)
	}
	if multi.InFlightUnits != 2 {
		t.Fatalf("在途数量必须透出（暂停时用户要知道还有几个在跑），实际 %d", multi.InFlightUnits)
	}
}

// TestConcurrentBatchCreationDoesNotShareState 是针对我**自己引入过**的一个
// 真实数据竞争的回归守卫：批次扫描必须用局部缓冲。
//
// 背景：初版实现把一个包级 `var generationConfigRaw []byte` 作为 pgx 的
// Scan 目标。CreateBatch 由并发 HTTP 请求调用，于是两个请求会互相覆盖
// 对方的 `generation_config`，表现为「批次 A 显示批次 B 的并发数」。
// 用 `go test -race` 跑这条测试即可复现（共享 slice 头的写-写竞争）。
//
// 断言用**内容正确性**而不是只看有没有 race 报告：即使不开 -race，
// 串数据也必须被这条断言抓住（否则 CI 默认不跑 -race 时会漏掉）。
func TestConcurrentBatchCreationDoesNotShareState(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	// 两份蓝图，并发数不同 —— 若扫描缓冲被共享，配置会串。
	other := blueprintPayload(fixture.coverageID, fixture.standardID)
	other.Nodes.Generation.ModelConnectionID = 1
	other.Nodes.Rules.QualityPolicyVersionID = fixture.policyID
	other.Nodes.Delivery.MappingVersionID = fixture.mappingID
	other.Nodes.Delivery.Format = model.ExportFormatJSONL
	other.Nodes.Generation.Concurrency = 24
	other.Nodes.Generation.MaxTokens = 12345
	_, otherVersion, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindBlueprint, fixture.editorID,
		SaveDocumentVersionInput{
			ExpectedRevision: 2,
			ChangeReason:     "并发 24",
			Payload:          other,
		})
	if err != nil {
		t.Fatalf("save other blueprint: %v", err)
	}

	const rounds = 8
	type outcome struct {
		index       int
		concurrency int
		maxTokens   int
		err         error
	}
	results := make(chan outcome, rounds*2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
				model.CreateBatchInput{
					Purpose: model.BatchPurposePilot, BlueprintVersionID: fixture.blueprintID, UnitCount: 1,
				})
			results <- outcome{index: idx, concurrency: batch.GenerationConfig.Concurrency,
				maxTokens: batch.GenerationConfig.MaxTokens, err: err}
		}(i)
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
				model.CreateBatchInput{
					Purpose: model.BatchPurposePilot, BlueprintVersionID: otherVersion.ID, UnitCount: 1,
				})
			results <- outcome{index: rounds + idx, concurrency: batch.GenerationConfig.Concurrency,
				maxTokens: batch.GenerationConfig.MaxTokens, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Fatalf("第 %d 个并发创建失败: %v", result.index, result.err)
		}
		// index < rounds 用的是并发 8 / maxTokens 4096 的蓝图。
		wantConcurrency, wantMaxTokens := 8, 4096
		if result.index >= rounds {
			wantConcurrency, wantMaxTokens = 24, 12345
		}
		if result.concurrency != wantConcurrency || result.maxTokens != wantMaxTokens {
			t.Fatalf("第 %d 个批次拿到了**别的批次**的生成配置（扫描缓冲被并发共享）："+
				"期望 concurrency=%d maxTokens=%d，实际 concurrency=%d maxTokens=%d",
				result.index, wantConcurrency, wantMaxTokens, result.concurrency, result.maxTokens)
		}
	}
}

// TestSamplePayloadRejectsLegacyAndBadStructures 覆盖 §2.2 的字段契约。
func TestSamplePayloadRejectsLegacyAndBadStructures(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		targetKind string
		payload    any
		field      string
	}{
		{
			name:       "SFT 缺少 reasoning",
			targetKind: model.TargetKindSFT,
			payload:    map[string]any{"question": "q", "answer": "a"},
			field:      "reasoning",
		},
		{
			name:       "SFT 使用旧字段名 chainOfThought",
			targetKind: model.TargetKindSFT,
			payload: map[string]any{
				"question": "q", "answer": "a",
				"chainOfThought": "旧命名不应被接受",
			},
			field: "chainOfThought",
		},
		{
			name:       "GRPO 档位被拍成逗号字符串",
			targetKind: model.TargetKindGRPO,
			payload: map[string]any{
				"question": "q", "judge_prompt": "p",
				"levels":        "-1,0,1",
				"level_rubrics": []map[string]any{},
			},
			field: "levels",
		},
		{
			name:       "GRPO 只有一档",
			targetKind: model.TargetKindGRPO,
			payload: map[string]any{
				"question": "q", "judge_prompt": "p",
				"levels": []string{"only-one"},
				"level_rubrics": []map[string]any{
					{"level": "only-one", "criteria": "判据", "acceptCase": "接受例"},
				},
			},
			field: "levels",
		},
		{
			name:       "GRPO 档位重复",
			targetKind: model.TargetKindGRPO,
			payload: map[string]any{
				"question": "q", "judge_prompt": "p",
				"levels": []string{"a", "a"},
				"level_rubrics": []map[string]any{
					{"level": "a", "criteria": "判据", "acceptCase": "接受例"},
				},
			},
			field: "levels[1]",
		},
		{
			name:       "GRPO 缺判据",
			targetKind: model.TargetKindGRPO,
			payload: map[string]any{
				"question": "q", "judge_prompt": "p",
				"levels": []string{"low", "high"},
				"level_rubrics": []map[string]any{
					{"level": "low", "criteria": "判据", "acceptCase": "接受例"},
				},
			},
			field: "levelRubrics[1]",
		},
		{
			name:       "GRPO 判据无边界例",
			targetKind: model.TargetKindGRPO,
			payload: map[string]any{
				"question": "q", "judge_prompt": "p",
				"levels": []string{"low", "high"},
				"level_rubrics": []map[string]any{
					{"level": "low", "criteria": "判据"},
					{"level": "high", "criteria": "判据", "acceptCase": "接受例"},
				},
			},
			field: "levelRubrics[0].acceptCase",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
				ProjectID:  fixture.projectID,
				SampleKey:  "reject-" + strings.ReplaceAll(testCase.name, " ", "-"),
				TargetKind: testCase.targetKind,
				Payload:    testCase.payload,
			})
			if err == nil {
				t.Fatal("非法样本内容必须被拒绝")
			}
			fieldErrors, ok := model.HasFieldErrors(err)
			if !ok {
				t.Fatalf("必须是字段级错误，实际: %v", err)
			}
			found := false
			for _, fieldError := range fieldErrors {
				if fieldError.Field == testCase.field {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("必须指出字段 %q，实际 %+v", testCase.field, fieldErrors)
			}
		})
	}

	// 非法内容不得留下任何样本版本。
	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM sample_versions WHERE project_id = $1`, fixture.projectID).Scan(&count); err != nil {
		t.Fatalf("count sample versions: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法样本内容不得落库，实际 %d 个版本", count)
	}
}

// TestBatchUnitCountLimits 覆盖契约 §2.3 的规模上限。
func TestBatchUnitCountLimits(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		purpose string
		units   int
		wantErr bool
	}{
		{"pilot 下界", model.BatchPurposePilot, 1, false},
		{"pilot 上界", model.BatchPurposePilot, 100, false},
		{"pilot 超界", model.BatchPurposePilot, 101, true},
		{"pilot 零", model.BatchPurposePilot, 0, true},
		{"scale 上界", model.BatchPurposeScale, 100000, false},
		{"scale 超界", model.BatchPurposeScale, 100001, true},
		{"非法用途", "explode", 5, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := fixture.createBatchInput(t, testCase.purpose, testCase.units)
			_, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT, input)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("超界或非法输入必须被拒绝")
				}
				if _, ok := model.HasFieldErrors(err); !ok {
					t.Fatalf("必须是字段级错误，实际: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("合法输入必须被接受: %v", err)
			}
		})
	}
}

// TestBatchEventSequenceIsMonotonic 覆盖「事件序号单调且不重复」。
//
// 序号是客户端去重与排序的依据，因此重复投递不得产生两个相同序号。
func TestBatchEventSequenceIsMonotonic(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	const extra = 10
	var wg sync.WaitGroup
	errs := make(chan error, extra)
	for i := 0; i < extra; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs <- fixture.batches.AppendBatchEvent(ctx, batch.ID, fixture.projectID,
				"TestEvent", fixture.editorID, map[string]any{"i": idx})
		}(i)
	}
	wg.Wait()
	close(errs)
	// 并发追加允许因序号竞争而失败（唯一约束兜底），但**不允许**产生重复序号。
	for err := range errs {
		if err != nil && !IsUniqueViolation(err) {
			t.Fatalf("并发追加事件的失败原因必须是序号竞争，实际: %v", err)
		}
	}

	events, err := fixture.batches.ListBatchEvents(ctx, batch.ID, 100)
	if err != nil {
		t.Fatalf("ListBatchEvents: %v", err)
	}
	seen := map[int]bool{}
	for _, event := range events {
		if seen[event.Sequence] {
			t.Fatalf("事件序号 %d 重复（客户端去重与排序会失效）", event.Sequence)
		}
		seen[event.Sequence] = true
	}
	if len(events) < 1+extra/2 {
		t.Fatalf("大多数并发追加应当成功并留下事件，实际只有 %d 条", len(events))
	}
}

// TestBatchCountsMatchItemFacts 是聚合缓存与事实的对账守卫。
func TestBatchCountsMatchItemFacts(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 4))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	// 2 成功 + 1 失败 + 1 未执行。
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("q-%d", i)
		item, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, key, nil)
		if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, AppendSampleVersionInput{
			SampleKey: key, TargetKind: model.TargetKindSFT, Payload: sftPayload(key),
		}); err != nil {
			t.Fatalf("commit success %d: %v", i, err)
		}
	}
	failed, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-failed", nil)
	if _, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, failed.ID, model.ErrorClassTimeout, "超时", true); err != nil {
		t.Fatalf("commit failure: %v", err)
	}
	if _, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "q-pending", nil); err != nil {
		t.Fatalf("ensure pending: %v", err)
	}

	refreshed, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	counts, err := fixture.batches.CountBatchItemsByStatus(ctx, batch.ID)
	if err != nil {
		t.Fatalf("CountBatchItemsByStatus: %v", err)
	}
	if refreshed.CompletedUnits != counts[model.ItemStatusSucceeded] {
		t.Fatalf("缓存与事实不一致：batch.completed=%d 实际 succeeded=%d",
			refreshed.CompletedUnits, counts[model.ItemStatusSucceeded])
	}
	if refreshed.FailedUnits != counts[model.ItemStatusFailed] {
		t.Fatalf("缓存与事实不一致：batch.failed=%d 实际 failed=%d",
			refreshed.FailedUnits, counts[model.ItemStatusFailed])
	}
	if refreshed.CompletedUnits != 2 || refreshed.FailedUnits != 1 {
		t.Fatalf("计数必须是 2 成功 + 1 失败，实际 %d/%d",
			refreshed.CompletedUnits, refreshed.FailedUnits)
	}
	if refreshed.PlannedUnits < 4 {
		t.Fatalf("计划单元数必须至少 4，实际 %d", refreshed.PlannedUnits)
	}
}

// TestErrorClassActionIsActionable 覆盖「失败有可操作原因」。
//
// 断言每个已知类别都给出中文建议，而不是让用户看到英文错误类别。
func TestErrorClassActionIsActionable(t *testing.T) {
	for _, errorClass := range []string{
		model.ErrorClassProvider, model.ErrorClassRateLimited, model.ErrorClassTimeout,
		model.ErrorClassEmptyOutput, model.ErrorClassTruncated, model.ErrorClassInvalidJSON,
		model.ErrorClassSchema, model.ErrorClassConfig, model.ErrorClassInternal,
		"unknown_class",
	} {
		action := model.ErrorClassAction(errorClass)
		if action == "" {
			t.Fatalf("错误类别 %q 必须给出可操作建议（不能让用户去猜）", errorClass)
		}
		if !strings.ContainsAny(action, "恢复重试联系检查新建降低提高") {
			t.Fatalf("建议必须指向具体动作，实际 %q", action)
		}
	}
}

// TestBatchSelectStatementsMatchCanonicalColumns 是 batch_store.go 中
// 「列清单不插值进 SQL」这一取舍的兜底。
//
// 为什么需要它：为了不让安全门禁无法区分「可信常量拼接」与「用户输入拼接」，
// 本文件每条查询都完整静态地写出列清单，而不是拼一个常量。代价是
// 同一份列清单出现多次 —— 一旦有人只改了一处，「列表里有这一列、详情里没有」
// 这类错误不会在编译期暴露，也不会在概览页暴露，只会在某个用户点开详情时
// 以「字段串位/值为零」的形态出现，而那时追溯成本极高。
//
// 本测试把 batchColumns 当作唯一基准，逐字比较每条静态语句的列清单。
func TestBatchSelectStatementsMatchCanonicalColumns(t *testing.T) {
	canonical := selectColumnsOf(t, batchColumns)
	if len(canonical) < 20 {
		t.Fatalf("基准列清单异常：只解析出 %d 列，可能是解析器或常量被改坏", len(canonical))
	}

	statements := map[string]string{
		"batchSelectByIDSQL": batchSelectByIDSQL,
		"batchListSQL":       batchListSQL,
	}
	for name, statement := range statements {
		columns := selectColumnsOf(t, statement)
		if len(columns) != len(canonical) {
			t.Fatalf("%s 的列数（%d）与基准（%d）不一致：\n语句列：%v\n基准列：%v",
				name, len(columns), len(canonical), columns, canonical)
		}
		for index := range canonical {
			if columns[index] != canonical[index] {
				t.Fatalf("%s 第 %d 列错位：语句是 %q，基准是 %q（列的顺序必须一致，因为 scanBatch 按顺序扫描）",
					name, index+1, columns[index], canonical[index])
			}
		}
	}
}

// selectColumnsOf 取出 SQL 中 SELECT 与 FROM 之间的列清单；
// 对于本身就是纯列清单的常量（batchColumns）则直接解析整个字符串。
func selectColumnsOf(t *testing.T, statement string) []string {
	t.Helper()
	body := statement
	upper := strings.ToUpper(statement)
	if start := strings.Index(upper, "SELECT"); start >= 0 {
		end := strings.Index(upper, " FROM ")
		if end < 0 || end < start {
			t.Fatalf("无法从语句中定位 SELECT ... FROM：%q", statement)
		}
		body = statement[start+len("SELECT") : end]
	}
	parts := strings.Split(body, ",")
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		column := strings.TrimSpace(strings.ReplaceAll(part, "\n", " "))
		column = strings.Join(strings.Fields(column), " ")
		if column == "" {
			continue
		}
		columns = append(columns, column)
	}
	return columns
}

// TestBatchRejectsPlanBeyondCoverageCapacity 覆盖 issue #190 的入口校验。
//
// 这是「静默少交付」的第一道闸门：计划量超过覆盖矩阵的可产出量时，
// 批次必须在写入 batches/jobs **之前**被拒绝，并给出可操作的字段错误。
// 如果没有这道校验，批次会入队、只跑出覆盖允许的那几个单元，然后被
// 宣告「已完成」—— 用户据此以为整批方案已验证。
func TestBatchRejectsPlanBeyondCoverageCapacity(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	// 刻意把覆盖矩阵收敛成 1 领域 × 1 方向 × 配额 1，构造真实场景：
	// 用户填了 3 个单元，而矩阵最多只能产出 1 个（#190 的实测形态）。
	overCapacity := fixture.createBatchInputWithCoverage(model.BatchPurposePilot, 3,
		func(coverage *model.CoveragePayload) {
			coverage.Domains = coverage.Domains[:1]
			coverage.Domains[0].Directions = coverage.Domains[0].Directions[:1]
			coverage.Domains[0].Directions[0].Quota = 1
		})
	_, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT, overCapacity)
	if err == nil {
		t.Fatal("计划量超过覆盖可产出量时必须拒绝，而不是静默少交付")
	}
	fieldErrors, ok := model.HasFieldErrors(err)
	if !ok || !hasFieldError(fieldErrors, "unitCount") {
		t.Fatalf("必须返回 unitCount 字段错误，实际 %v", err)
	}

	var count int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM batches WHERE project_id = $1`, fixture.projectID).Scan(&count); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒请求不得写入批次，实际已有 %d 条", count)
	}

	// 边界内的计划量必须照常通过（正常路径）：计划量正好等于容量 1。
	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInputWithCoverage(model.BatchPurposePilot, 1,
			func(coverage *model.CoveragePayload) {
				coverage.Domains = coverage.Domains[:1]
				coverage.Domains[0].Directions = coverage.Domains[0].Directions[:1]
				coverage.Domains[0].Directions[0].Quota = 1
			}))
	if err != nil {
		t.Fatalf("恰好等于容量时必须允许，实际 %v", err)
	}
	if batch.PlannedUnits != 1 {
		t.Fatalf("计划量必须被保留为 1，实际 %d", batch.PlannedUnits)
	}
}

// TestRefreshBatchCountsNeverClaimsCompletedWithShortfall 覆盖 issue #190 的
// 状态机修复：定稿的单元数少于计划量时，不得聚合为 completed。
//
// 这里刻意断言「剩下的计划单元从未被写入 batch_items」这一真实形态
// （覆盖配额小于计划量时 runner 就是这样），而不是伪造失败项。
func TestRefreshBatchCountsNeverClaimsCompletedWithShortfall(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	// 计划 3、覆盖容量 3：四个数字都是真的，唯一变量是「只产出了 1 个」。
	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInputWithCoverage(model.BatchPurposePilot, 3,
			func(coverage *model.CoveragePayload) {
				coverage.Domains = coverage.Domains[:1]
				coverage.Domains[0].Directions = coverage.Domains[0].Directions[:1]
				coverage.Domains[0].Directions[0].Quota = 3
			}))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	item, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, AppendSampleVersionInput{
		SampleKey: "d/d#1", TargetKind: model.TargetKindSFT, Payload: sftPayload("唯一产出"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}

	refreshed, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if refreshed.Status == model.BatchStatusCompleted {
		t.Fatalf("产出 1/3 时不得聚合为 completed（这正是 #190 的静默少交付）")
	}
	if refreshed.Status != model.BatchStatusPartialFailed {
		t.Fatalf("产出少于计划量必须聚合为 partial_failed，实际 %s", refreshed.Status)
	}
	if got := refreshed.Shortfall(); got != 2 {
		t.Fatalf("缺口必须是 3-1=2，实际 %d", got)
	}
	if note := refreshed.ShortfallNote(); note == "" {
		t.Fatal("定稿批次有缺口时必须给出中文原因")
	}
	if refreshed.FinishedAt == nil {
		t.Fatal("部分完成也是终态，必须写 finished_at，否则界面会显示永远在跑")
	}

	// 计划量必须保留为用户意图，而不是被已落库单元数覆盖。
	if refreshed.PlannedUnits != 3 {
		t.Fatalf("计划量必须保持 3（用户意图），实际 %d", refreshed.PlannedUnits)
	}

	// 补跑缺口后（把剩余两个单元写成功）才能变成 completed。
	for _, key := range []string{"d/d#2", "d/d#3"} {
		extra, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, key, nil)
		if err != nil {
			t.Fatalf("ensure %s: %v", key, err)
		}
		if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, extra.ID, AppendSampleVersionInput{
			SampleKey: key, TargetKind: model.TargetKindSFT, Payload: sftPayload(key),
		}); err != nil {
			t.Fatalf("commit %s: %v", key, err)
		}
	}
	final, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if final.Status != model.BatchStatusCompleted {
		t.Fatalf("计划量全部产出后才允许 completed，实际 %s", final.Status)
	}
	if final.Shortfall() != 0 || final.ShortfallNote() != "" {
		t.Fatalf("无缺口时不得报告缺口：%d / %q", final.Shortfall(), final.ShortfallNote())
	}
}

// TestRefreshBatchCountsCorrectsStaleCompletedWithShortfall 覆盖 issue #201：
// **历史终态路径**必须能被计数刷新纠正。
//
// 与 TestRefreshBatchCountsNeverClaimsCompletedWithShortfall 的区别：
// 那条覆盖「推导路径」（状态还在 running 时就不置 completed），
// 本条覆盖「状态已经是 completed 但事实只产出 1/4」的真实形态（实测 b_2）。
// 旧实现在终态分支直接提前返回，于是这类批次永远显示绿色「已完成」、
// 同时挂着「缺口 3」，而 BatchCapabilitiesFor 对 completed 返回全 false ——
// 用户拿不到任何出口。
func TestRefreshBatchCountsCorrectsStaleCompletedWithShortfall(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 4))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	item, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, AppendSampleVersionInput{
		SampleKey: "d/d#1", TargetKind: model.TargetKindSFT, Payload: sftPayload("唯一产出"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}

	// 手工构造 b_2 的历史形态：状态被写成 completed，而事实只有 1/4。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE batches SET status = 'completed', completed_units = 1, finished_at = NOW()
    WHERE id = $1`, batch.ID); err != nil {
		t.Fatalf("手工置为历史终态: %v", err)
	}

	// 修复前：这条 UPDATE 之后状态仍是 completed（终态分支提前返回）。
	refreshed, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if refreshed.Status == model.BatchStatusCompleted {
		t.Fatal("产出 1/4 的历史终态必须被纠正为 partial_failed，而不是继续显示「已完成」")
	}
	if refreshed.Status != model.BatchStatusPartialFailed {
		t.Fatalf("应纠正为 partial_failed，实际 %s", refreshed.Status)
	}
	if refreshed.Shortfall() != 3 {
		t.Fatalf("缺口必须是 4-1=3，实际 %d", refreshed.Shortfall())
	}
	// 缺口文案必须指出真实原因（这里无失败项，因此是「未创建/被跳过」而非覆盖率）。
	if note := refreshed.ShortfallNote(); note == "" {
		t.Fatal("定稿批次有缺口时必须给出中文原因")
	} else if strings.Contains(note, "覆盖率不足") {
		t.Fatalf("无失败项的缺口不得断言是覆盖率问题（#208 的误导来源），实际 %q", note)
	}

	// 时间线必须留下纠正记录，否则界面自相矛盾（只有 BatchQueued + BatchCompleted）。
	events, err := fixture.batches.ListBatchEvents(ctx, batch.ID, 20)
	if err != nil {
		t.Fatalf("ListBatchEvents: %v", err)
	}
	corrected := false
	for _, event := range events {
		if event.EventType != model.BatchEventPartialFailed {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(event.Detail, &detail); err != nil {
			t.Fatalf("event detail 必须是 JSON: %v", err)
		}
		if detail["correctionOf"] == model.BatchStatusCompleted {
			corrected = true
		}
	}
	if !corrected {
		t.Fatal("状态被纠正时必须写一条带 correctionOf 的事件，否则时间线无法解释为什么从「已完成」变成了「部分完成」")
	}

	// 边界：无缺口的历史 completed 不得被改写（正常路径不能被打扰）。
	whole, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	wholeItem, _, _ := fixture.batches.EnsureBatchItem(ctx, whole.ID, fixture.projectID, "d/d#1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, whole.ID, fixture.projectID, wholeItem.ID, AppendSampleVersionInput{
		SampleKey: "d/d#1", TargetKind: model.TargetKindSFT, Payload: sftPayload("完整产出"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}
	kept, err := fixture.batches.RefreshBatchCounts(ctx, whole.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if kept.Status != model.BatchStatusCompleted {
		t.Fatalf("产出齐全的批次必须保持 completed，实际 %s", kept.Status)
	}
}

// TestRefreshBatchCountsConvergesZombieRunningBatch 覆盖 issue #202(c)：
// 「全部单元失败 + 无在途作业 + 无待执行作业」不得停留在 running。
//
// 实测形态（b_1）：12/12 单元 config_error，status=running，jobs 表没有对应作业。
// 批次成为「状态说在跑、事实一条都不会再跑」的僵尸，/today 的「进行中批次」
// 会长期挂着它。
func TestRefreshBatchCountsConvergesZombieRunningBatch(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 3))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	for _, key := range []string{"d/d#1", "d/d#2", "d/d#3"} {
		item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, key, nil)
		if err != nil {
			t.Fatalf("ensure %s: %v", key, err)
		}
		// retryable=false 正是实测形态（config_error 被 runner 标成不可重试）。
		if _, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, item.ID,
			model.ErrorClassConfig, "missing model connection", false); err != nil {
			t.Fatalf("commit failure %s: %v", key, err)
		}
	}

	refreshed, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	if refreshed.Status == model.BatchStatusRunning {
		t.Fatal("全部单元已定稿且无在途作业时不得停留在 running（#202 的僵尸形态）")
	}
	if refreshed.Status != model.BatchStatusPartialFailed {
		t.Fatalf("全部失败必须收敛为 partial_failed，实际 %s", refreshed.Status)
	}
	if refreshed.ShortfallNote() == "" {
		t.Fatal("僵尸批次收敛后必须能解释缺口原因")
	}
	// 缺口原因必须来自失败事实（#208），并且与失败详情页同源。
	if refreshed.DominantFailureClass != model.ErrorClassConfig {
		t.Fatalf("缺口原因必须来自失败单元的 error_class 分布，实际 %q", refreshed.DominantFailureClass)
	}
	note := refreshed.ShortfallNote()
	if !strings.Contains(note, "模型连接") {
		t.Fatalf("缺口文案必须指向真实修复动作（选择模型连接），而不是覆盖率/配额，实际 %q", note)
	}
}

// TestListDivergentBatchIDsFindsStaleStates 覆盖 #201/#202 的**可达性**：
// 收敛逻辑只有在能被触达时才有意义。
//
// 实测教训：`RefreshBatchCounts` 此前只由 runner 跑完与控制命令调用，因此
// 已经跑完的历史批次永远走不到它 —— 修复只对未来的批次有效。
// 本查询把「哪几条与事实不符」变成可扫描的事实，由 worker 维护循环定期调用。
func TestQueuedBatchIsReconciledOnlyAfterGenerationJobSettles(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()
	jobs := NewJobStore(fixture.pool)
	batch, job, err := fixture.batches.CreateBatchWithJob(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1),
		&EnqueueJobInput{Kind: model.JobKindBatchGenerate, IdempotencyKey: fmt.Sprintf("queued-reconcile:%d", fixture.projectID)})
	if err != nil || job == nil {
		t.Fatalf("create batch/job: %v", err)
	}
	assertScanned := func(want bool) {
		t.Helper()
		ids, err := fixture.batches.ListDivergentBatchIDs(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range ids {
			found = found || id == batch.ID
		}
		if found != want {
			t.Fatalf("queued batch scanned=%v, want=%v", found, want)
		}
	}
	assertScanned(false) // A newly queued job is still legitimate pending work.
	claimed, ok, err := jobs.ClaimJobByID(ctx, job.ID, "queued-reconcile-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim job: %v", err)
	}
	assertScanned(false)
	item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID, AppendSampleVersionInput{
		SampleKey: item.ItemKey, TargetKind: model.TargetKindSFT, Payload: sftPayload("真实作业定稿顺序"),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil || before.Status != model.BatchStatusQueued {
		t.Fatalf("active job must prevent premature terminal state: %+v, %v", before, err)
	}
	assertScanned(false)
	if _, err := jobs.CompleteJob(ctx, job.ID, claimed.LeaseOwner, claimed.FencingToken, nil); err != nil {
		t.Fatal(err)
	}
	assertScanned(true) // Simulate a crash after job commit, before worker refresh.
	final, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil || final.Status != model.BatchStatusCompleted || final.CompletedUnits != 1 {
		t.Fatalf("settled queued batch must converge: %+v, %v", final, err)
	}
	assertScanned(false)
}

func TestListDivergentBatchIDsFindsStaleStates(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	// 正常批次（completed 且产出齐全）：不得出现在结果里。
	healthy, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	okItem, _, _ := fixture.batches.EnsureBatchItem(ctx, healthy.ID, fixture.projectID, "d/d#1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, healthy.ID, fixture.projectID, okItem.ID, AppendSampleVersionInput{
		SampleKey: "d/d#1", TargetKind: model.TargetKindSFT, Payload: sftPayload("完整"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, healthy.ID); err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}

	// 不一致 1：completed 但产出不足（#201）。
	stale, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 4))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE batches SET status = 'completed', completed_units = 1 WHERE id = $1`, stale.ID); err != nil {
		t.Fatalf("构造不一致 completed: %v", err)
	}

	// 不一致 2：running 但没有任何在途/待执行单元（#202 的僵尸）。
	zombie, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 2))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	zombieItem, _, _ := fixture.batches.EnsureBatchItem(ctx, zombie.ID, fixture.projectID, "d/d#1", nil)
	if _, err := fixture.batches.CommitBatchItemFailure(ctx, zombie.ID, zombieItem.ID,
		model.ErrorClassConfig, "missing model connection", false); err != nil {
		t.Fatalf("commit failure: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE batches SET status = 'running' WHERE id = $1`, zombie.ID); err != nil {
		t.Fatalf("构造僵尸 running: %v", err)
	}

	ids, err := fixture.batches.ListDivergentBatchIDs(ctx, 100)
	if err != nil {
		t.Fatalf("ListDivergentBatchIDs: %v", err)
	}
	found := map[int64]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found[stale.ID] {
		t.Fatal("completed 但产出不足的批次必须被扫出来（#201），否则收敛逻辑不可达")
	}
	if !found[zombie.ID] {
		t.Fatal("running 但无待执行单元的批次必须被扫出来（#202）")
	}
	if found[healthy.ID] {
		t.Fatal("产出齐全的正常批次不得被扫出来（否则每 30 秒做一次无意义的重算）")
	}

	// 收敛后必须**不再**出现在结果里 —— 否则维护循环会永远重复处理同一条。
	for _, id := range []int64{stale.ID, zombie.ID} {
		if _, err := fixture.batches.RefreshBatchCounts(ctx, id); err != nil {
			t.Fatalf("RefreshBatchCounts(%d): %v", id, err)
		}
	}
	after, err := fixture.batches.ListDivergentBatchIDs(ctx, 100)
	if err != nil {
		t.Fatalf("ListDivergentBatchIDs: %v", err)
	}
	for _, id := range after {
		if id == stale.ID || id == zombie.ID {
			t.Fatalf("批次 %d 收敛后仍被判定为不一致（维护循环会空转）", id)
		}
	}
}

// TestResumeBatchEnqueuesJobWithoutClaimingZombie 覆盖 issue #202(a)：
// `resume` 必须**真的派作业**，且不得把无待办工作的批次声明成 running。
//
// 实测形态：b_1 的 12 个单元全部 config_error 且 retryable=false，作业早已
// succeeded 结束。旧实现在点「继续」时只改状态、不建作业 —— 状态回到 running
// 而没有任何东西会再跑，批次成为僵尸。
//
// 两条断言分别覆盖两种恢复场景，它们**必须给出不同结果**：
//  1. 无待办（全部已定稿、只剩不可重试失败）→ partial_failed 且**不**派作业
//     （派了也是空转一轮，还会多写事件污染时间线）；
//  2. 有未创建的单元（#201 的缺口形态）→ running 且**恰好**派一个新作业。
func TestResumeBatchEnqueuesJobWithoutClaimingZombie(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	countGenerateJobs := func(batchID int64) int {
		t.Helper()
		var count int
		if err := fixture.pool.QueryRow(ctx, `
      SELECT COUNT(*) FROM jobs WHERE batch_id = $1 AND job_kind = $2`,
			batchID, model.JobKindBatchGenerate).Scan(&count); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		return count
	}

	// 场景 1：全部单元已定稿、只剩不可重试失败（b_1 的形态）。
	zombie, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 2))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	for _, key := range []string{"d/d#1", "d/d#2"} {
		item, _, err := fixture.batches.EnsureBatchItem(ctx, zombie.ID, fixture.projectID, key, nil)
		if err != nil {
			t.Fatalf("ensure %s: %v", key, err)
		}
		if _, err := fixture.batches.CommitBatchItemFailure(ctx, zombie.ID, item.ID,
			model.ErrorClassConfig, "missing model connection", false); err != nil {
			t.Fatalf("commit failure: %v", err)
		}
	}
	// 先让聚合收敛，再模拟用户点「继续」。
	if _, err := fixture.batches.RefreshBatchCounts(ctx, zombie.ID); err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	beforeJobs := countGenerateJobs(zombie.ID)
	if _, err := fixture.batches.ResumeBatch(ctx, fixture.projectID, zombie.ID, fixture.editorID); err != nil {
		t.Fatalf("ResumeBatch(僵尸批次): %v", err)
	}
	after, err := fixture.batches.GetBatch(ctx, zombie.ID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if after.Status == model.BatchStatusRunning {
		t.Fatal("无待办工作的批次点「继续」后不得停在 running（#202 的僵尸形态）")
	}
	if after.Status != model.BatchStatusPartialFailed {
		t.Fatalf("应保持 partial_failed，实际 %s", after.Status)
	}
	if got := countGenerateJobs(zombie.ID); got != beforeJobs {
		t.Fatalf("没有产生任何待办工作却派了作业（空转一轮）：之前 %d，之后 %d", beforeJobs, got)
	}

	// 场景 2：计划 4 但只落库 1 行（b_2 的形态）→ 「补齐缺口」必须派作业。
	gap, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 4))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	only, _, _ := fixture.batches.EnsureBatchItem(ctx, gap.ID, fixture.projectID, "d/d#1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, gap.ID, fixture.projectID, only.ID, AppendSampleVersionInput{
		SampleKey: "d/d#1", TargetKind: model.TargetKindSFT, Payload: sftPayload("唯一产出"),
	}); err != nil {
		t.Fatalf("commit success: %v", err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, gap.ID); err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	beforeGap := countGenerateJobs(gap.ID)
	resumed, err := fixture.batches.ResumeBatch(ctx, fixture.projectID, gap.ID, fixture.editorID)
	if err != nil {
		t.Fatalf("ResumeBatch(缺口批次): %v", err)
	}
	if resumed.Status != model.BatchStatusRunning {
		t.Fatalf("有未创建单元的批次恢复后应回到 running，实际 %s", resumed.Status)
	}
	if got := countGenerateJobs(gap.ID); got != beforeGap+1 {
		t.Fatalf("「补齐缺口」必须恰好派一个新作业，之前 %d 之后 %d", beforeGap, got)
	}
	// 派出的作业必须能被执行侧认领（kind/batch_id 都要对）。
	var kind string
	var batchID int64
	if err := fixture.pool.QueryRow(ctx, `
    SELECT job_kind, batch_id FROM jobs
    WHERE batch_id = $1 AND status = 'pending' ORDER BY id DESC LIMIT 1`, gap.ID).
		Scan(&kind, &batchID); err != nil {
		t.Fatalf("读取派出的作业: %v", err)
	}
	if kind != model.JobKindBatchGenerate || batchID != gap.ID {
		t.Fatalf("派出的作业必须指向本批次的生成作业，实际 kind=%s batch=%d", kind, batchID)
	}
}

// TestBatchStepsProjectedFromItemFacts 覆盖 issue #212 的核心验收口径。
//
// 缺陷形态（实测 b_4）：`batch_steps` 全库 0 行，已完成 4/4 的批次详情显示
// 「还没有阶段记录。」—— 用空态宣称「这次没有执行任何阶段」。
//
// 断言的是 issue 明确要求的那条不变式：
// **跑完一个真实批次后 `batch_steps` 不为空，且各阶段 done_units == total_units**。
// 同时覆盖两条边界路径：
//   - 有失败单元时「生成」阶段必须 partial_failed 且失败数可见（**不得**宣称完成）；
//   - 维护循环的可达性：一个从未被 runner 碰过的存量批次也能被补出阶段行
//     （只修 runner 无法修好历史批次，#201/#202 的同一教训）。
func TestBatchStepsProjectedFromItemFacts(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 3))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	// 未执行的批次也必须给出阶段行（否则区块会退回「还没有阶段记录」）。
	initial, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchSteps(初始): %v", err)
	}
	if len(initial) != 2 {
		t.Fatalf("阶段投影必须恰好给出 2 行（规划/生成），实际 %d", len(initial))
	}
	for _, step := range initial {
		if step.UnitLabel == "" || strings.Contains(step.UnitLabel, step.Phase) {
			t.Fatalf("阶段 %q 的中文单位名不合法：%q", step.Phase, step.UnitLabel)
		}
	}

	// 正常路径：3 个单元全部产出成功。
	for _, key := range []string{"d/d#1", "d/d#2", "d/d#3"} {
		item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, key, nil)
		if err != nil {
			t.Fatalf("ensure %s: %v", key, err)
		}
		if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID,
			AppendSampleVersionInput{SampleKey: key, TargetKind: model.TargetKindSFT, Payload: sftPayload(key)}); err != nil {
			t.Fatalf("commit %s: %v", key, err)
		}
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}
	steps, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchSteps: %v", err)
	}
	byPhase := map[string]model.BatchStep{}
	for _, step := range steps {
		byPhase[step.Phase] = step
	}
	generate, ok := byPhase[model.BatchStepGenerate]
	if !ok {
		t.Fatal("缺少「生成」阶段行（#212 的实测形态就是这行为空）")
	}
	if generate.DoneUnits != generate.TotalUnits {
		t.Fatalf("全部产出后 生成阶段必须 done == total，实际 %d/%d",
			generate.DoneUnits, generate.TotalUnits)
	}
	if generate.Status != model.StepStatusCompleted {
		t.Fatalf("全部产出后生成阶段必须 completed，实际 %q", generate.Status)
	}
	// 落盘的事实也必须存在：表空 = #212 缺陷本题。
	var storedCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM batch_steps WHERE batch_id = $1`, batch.ID).Scan(&storedCount); err != nil {
		t.Fatalf("读取 batch_steps: %v", err)
	}
	if storedCount != 2 {
		t.Fatalf("batch_steps 必须落盘 2 行，实际 %d（全库 0 行正是本 issue 的缺陷形态）", storedCount)
	}

	// 边界路径：把其中一个单元改成失败重试后，阶段必须显式报告缺口。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE batch_items SET status = 'failed', error_class = 'timeout', error_message = '超时'
    WHERE batch_id = $1 AND item_key = 'd/d#3'`, batch.ID); err != nil {
		t.Fatalf("构造失败单元: %v", err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatalf("RefreshBatchCounts(失败后): %v", err)
	}
	afterFailure, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RefreshBatchSteps(失败后): %v", err)
	}
	var failedStep model.BatchStep
	for _, step := range afterFailure {
		if step.Phase == model.BatchStepGenerate {
			failedStep = step
		}
	}
	if failedStep.FailedUnits != 1 {
		t.Fatalf("失败数必须可见，实际 %d", failedStep.FailedUnits)
	}
	if failedStep.Status != model.StepStatusPartialFailed {
		t.Fatalf("有失败的阶段不得宣称完成，实际 %q", failedStep.Status)
	}
	if strings.TrimSpace(failedStep.ErrorSummary) == "" {
		t.Fatal("有失败的阶段必须给出可展示的中文说明")
	}
}

// TestListBatchIDsWithStaleStepsFindsHistoricalBatches 覆盖 #212 的**可达性**：
// 只修 runner 无法修好已经跑完的历史批次（它们永远不会再被 runner 碰到）。
func TestListBatchIDsWithStaleStepsFindsHistoricalBatches(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	item, _, _ := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID,
		AppendSampleVersionInput{SampleKey: "d/d#1", TargetKind: model.TargetKindSFT, Payload: sftPayload("完成")}); err != nil {
		t.Fatalf("commit success: %v", err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatalf("RefreshBatchCounts: %v", err)
	}

	// 缺陷形态：批次已定稿、batch_steps 仍是空的（历史批次就是这样）。
	ids, err := fixture.batches.ListBatchIDsWithStaleSteps(ctx, 500)
	if err != nil {
		t.Fatalf("ListBatchIDsWithStaleSteps: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == batch.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("阶段表为空的历史批次必须被扫出来，否则维护循环永远修不到它")
	}

	if _, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID); err != nil {
		t.Fatalf("RefreshBatchSteps: %v", err)
	}
	// 收敛之后不得再命中：否则维护循环每 30 秒改写一次而永不收敛（#202 的同一约束）。
	idsAfter, err := fixture.batches.ListBatchIDsWithStaleSteps(ctx, 500)
	if err != nil {
		t.Fatalf("ListBatchIDsWithStaleSteps(after): %v", err)
	}
	for _, id := range idsAfter {
		if id == batch.ID {
			t.Fatal("阶段已收敛的批次不得再被扫出（会导致维护循环永不收敛）")
		}
	}
}

func TestBatchStepHistoricalTimesAndStaleWrites(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()
	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID,
		AppendSampleVersionInput{SampleKey: "historical", TargetKind: model.TargetKindSFT, Payload: sftPayload("历史")}); err != nil {
		t.Fatal(err)
	}
	// 模拟早已执行的历史批次：维护时刻不能冒充历史执行时刻。
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	finished := started.Add(time.Minute)
	if _, err := fixture.pool.Exec(ctx, `UPDATE batch_items SET created_at=$2, started_at=$2, finished_at=$3 WHERE id=$1`,
		item.ID, started, finished); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	steps, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	generate := steps[1]
	if generate.StartedAt == nil || !generate.StartedAt.Equal(started) ||
		generate.FinishedAt == nil || !generate.FinishedAt.Equal(finished) || generate.ID == 0 {
		t.Fatalf("首次落盘必须保留真实执行时间和阶段身份，实际 %+v", generate)
	}
	if _, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	// 终态后的过期写入不能把已产出的单元退回到 running/0。
	stale := generate
	stale.Status, stale.DoneUnits = model.StepStatusRunning, 0
	if err := fixture.batches.UpsertBatchStep(ctx, batch.ID, stale); err != nil {
		t.Fatal(err)
	}
	var status string
	var done int
	var updated time.Time
	if err := fixture.pool.QueryRow(ctx, `SELECT status, done_units, updated_at FROM batch_steps WHERE id=$1`, generate.ID).
		Scan(&status, &done, &updated); err != nil {
		t.Fatal(err)
	}
	if status != model.StepStatusCompleted || done != 1 || !updated.Equal(generate.UpdatedAt) {
		t.Fatalf("重放和过期写入不能改写终态：status=%s done=%d updated=%v", status, done, updated)
	}
}

func TestBatchStepsConvergeAfterSuccessfulRetry(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()
	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.batches.CommitBatchItemFailure(ctx, batch.ID, item.ID, model.ErrorClassTimeout, "超时", true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before[1].Status != model.StepStatusPartialFailed || before[1].FinishedAt == nil {
		t.Fatalf("部分失败必须有真实结束时间，实际 %+v", before[1])
	}
	if _, err := fixture.batches.MarkRetryableItemsPending(ctx, fixture.projectID, batch.ID, fixture.editorID); err != nil {
		t.Fatal(err)
	}
	running, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running[1].Status != model.StepStatusRunning || running[1].FinishedAt != nil {
		t.Fatalf("合法重试必须重新开放阶段，实际 %+v", running[1])
	}
	if _, _, err := fixture.batches.CommitBatchItemSuccess(ctx, batch.ID, fixture.projectID, item.ID,
		AppendSampleVersionInput{SampleKey: "retry", TargetKind: model.TargetKindSFT, Payload: sftPayload("重试成功")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := fixture.batches.ListBatchIDsWithStaleSteps(ctx, 10000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		found = found || id == batch.ID
	}
	if !found {
		t.Fatal("合法重试后的计数变化必须能被维护循环发现")
	}
	after, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after[1].Status != model.StepStatusCompleted || after[1].DoneUnits != 1 || after[1].FailedUnits != 0 || after[1].FinishedAt == nil {
		t.Fatalf("重试成功必须收敛持久化阶段，实际 %+v", after[1])
	}
	if err := fixture.batches.UpsertBatchStep(ctx, batch.ID, before[1]); err != nil {
		t.Fatal(err)
	}
	var storedStatus string
	var storedDone, storedFailed int
	if err := fixture.pool.QueryRow(ctx, `SELECT status, done_units, failed_units FROM batch_steps WHERE id=$1`, after[1].ID).
		Scan(&storedStatus, &storedDone, &storedFailed); err != nil {
		t.Fatal(err)
	}
	if storedStatus != model.StepStatusCompleted || storedDone != 1 || storedFailed != 0 {
		t.Fatalf("重试前的过期失败投影不能覆盖成功事实：%s %d/%d", storedStatus, storedDone, storedFailed)
	}
}

func TestBatchStepsExplainSkippedUnits(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()
	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(t, model.BatchPurposePilot, 1))
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := fixture.batches.EnsureBatchItem(ctx, batch.ID, fixture.projectID, "d/d#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.batches.MarkBatchItemSkipped(ctx, batch.ID, item.ID, "原始素材无答案"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.batches.RefreshBatchCounts(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	steps, err := fixture.batches.RefreshBatchSteps(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if steps[1].DoneUnits != 0 || steps[1].FailedUnits != 0 || steps[1].Status != model.StepStatusPartialFailed ||
		!strings.Contains(steps[1].ErrorSummary, "1 个跳过") || steps[1].FinishedAt == nil {
		t.Fatalf("跳过必须解释缺口且不能冒充失败或产出：%+v", steps[1])
	}
}
