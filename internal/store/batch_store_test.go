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
func (fixture batchFixture) createBatchInput(purpose string, units int) model.CreateBatchInput {
	input := model.CreateBatchInput{
		Purpose:                purpose,
		BlueprintVersionID:     fixture.blueprintID,
		CoverageVersionID:      fixture.coverageID,
		StandardVersionID:      fixture.standardID,
		QualityPolicyVersionID: fixture.policyID,
		MappingVersionID:       fixture.mappingID,
		UnitCount:              units,
	}
	return input
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
		fixture.createBatchInput(model.BatchPurposePilot, 5))
	if err != nil {
		t.Fatalf("create pilot A: %v", err)
	}
	pilotB, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(model.BatchPurposePilot, 8))
	if err != nil {
		t.Fatalf("create pilot B: %v", err)
	}
	scale, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.editorID, model.TargetKindSFT,
		fixture.createBatchInput(model.BatchPurposeScale, 500))
	if err != nil {
		t.Fatalf("create scale: %v", err)
	}

	if pilotA.ID == pilotB.ID || pilotB.ID == scale.ID || pilotA.ID == scale.ID {
		t.Fatalf("三个批次必须有独立 ID，实际 %d / %d / %d", pilotA.ID, pilotB.ID, scale.ID)
	}
	if pilotA.Purpose != model.BatchPurposePilot || scale.Purpose != model.BatchPurposeScale {
		t.Fatalf("用途必须被保留：%s / %s", pilotA.Purpose, scale.Purpose)
	}
	if pilotA.PlannedUnits != 5 || pilotB.PlannedUnits != 8 || scale.PlannedUnits != 500 {
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
		fixture.createBatchInput(model.BatchPurposePilot, 3))
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
	_, newVersion, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindBlueprint, fixture.editorID,
		SaveDocumentVersionInput{ExpectedRevision: 2, ChangeReason: "提高并发", Payload: newBlueprint})
	if err != nil {
		t.Fatalf("save new blueprint: %v", err)
	}

	reloaded, err := fixture.batches.GetBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if reloaded.Snapshot.BlueprintContentHash != originalHash {
		t.Fatalf("保存新蓝图不得改变旧快照：期望 %s，实际 %s",
			originalHash, reloaded.Snapshot.BlueprintContentHash)
	}
	if reloaded.Snapshot.BlueprintVersionID != fixture.blueprintID {
		t.Fatalf("快照必须仍指向原版本行 %d，实际 %d",
			fixture.blueprintID, reloaded.Snapshot.BlueprintVersionID)
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

	input := fixture.createBatchInput(model.BatchPurposePilot, 2)
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 3))
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
		fixture.createBatchInput(model.BatchPurposePilot, 2))
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 2))
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
			input := fixture.createBatchInput(testCase.purpose, testCase.units)
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
		fixture.createBatchInput(model.BatchPurposePilot, 1))
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
		fixture.createBatchInput(model.BatchPurposePilot, 4))
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
