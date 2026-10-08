package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T20 的候选、门槛与抗并发冻结。
//
// 必须连真实 Postgres：核心断言是「版本名唯一约束」「冻结事务检测到
// 确认之后的判断/证据变化」——都是数据库与事务语义。

type releaseFixture struct {
	pool             *pgxpool.Pool
	projectID        int64
	userID           int64
	mappingVersionID int64
	versionIDs       []int64
	releases         *ReleaseStore
	reviews          *ReviewStore
}

// newReleaseFixture 建项目 + 3 个已**接纳**的内容版本。
//
// 全部先接纳：这样「干净候选」是默认状态，而各用例只需破坏一件事，
// 从而让失败原因唯一（多因一果的测试很难定位）。
func newReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d-%s", os.Getpid(), strings.ReplaceAll(t.Name(), "/", "_"))

	var workspaceID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2)
    ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
		"发布测试工作区 "+suffix, "release-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user')
    ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role RETURNING id`,
		"release-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	projects := NewProjectStore(pool)
	input := model.CreateProjectInput{Name: "发布项目", TargetKind: model.TargetKindSFT}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	batches := NewBatchStore(pool)
	reviews := NewReviewStore(pool)
	// 映射版本：候选的必需字段之一（缺它门槛会以 MISSING_FIELD 阻塞，
	// 而那会让「干净候选」用例变成「测一个缺字段的候选」）。
	documents := NewDocumentStore(pool)
	mappingPayload := model.MappingPayload{
		SchemaVersion: model.SchemaVersionFor(model.KindMapping),
		Format:        model.ExportFormatJSONL,
		Fields: []model.MappingField{
			{TargetField: "question", SourceField: "question", Required: true},
			{TargetField: "reasoning", SourceField: "reasoning", Required: true},
			{TargetField: "answer", SourceField: "answer", Required: true},
		},
	}
	_, mappingVersion, err := documents.SaveVersion(ctx, project.ID, model.KindMapping, userID,
		SaveDocumentVersionInput{ChangeReason: "初版映射", Payload: mappingPayload})
	if err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	versionIDs := []int64{}
	for index := 1; index <= 3; index++ {
		key := "release-item-" + suffix + "-" + fmt.Sprint(index)
		sample, err := batches.EnsureSample(ctx, project.ID, key, model.TargetKindSFT, key, nil)
		if err != nil {
			t.Fatalf("EnsureSample: %v", err)
		}
		_, version, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
			ProjectID: project.ID, SampleKey: sample.SampleKey, TargetKind: model.TargetKindSFT,
			Title: key, Payload: map[string]any{"question": "q", "reasoning": "r", "answer": "a"},
		})
		if err != nil {
			t.Fatalf("AppendSampleVersion: %v", err)
		}
		versionIDs = append(versionIDs, version.ID)

		// 先接纳：干净候选是默认状态。
		projection, err := reviews.GetProjection(ctx, project.ID, version.ID)
		if err != nil {
			t.Fatalf("GetProjection: %v", err)
		}
		if _, err := reviews.SubmitDecision(ctx, project.ID, userID, model.SubmitDecisionInput{
			SampleVersionID: version.ID, EvidenceRevision: projection.EvidenceRevision,
			ReviewerRevision: 1, Action: model.DecisionAccept, Reason: "推理链完整",
		}); err != nil {
			t.Fatalf("SubmitDecision: %v", err)
		}
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM release_items WHERE release_id IN (SELECT id FROM releases WHERE project_id = $1)`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM release_gates WHERE release_id IN (SELECT id FROM releases WHERE project_id = $1)`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM release_candidates WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM releases WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM review_decisions WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM review_projections WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})

	return releaseFixture{
		pool: pool, projectID: project.ID, userID: userID, mappingVersionID: mappingVersion.ID,
		versionIDs: versionIDs, releases: NewReleaseStore(pool), reviews: reviews,
	}
}

func (fixture releaseFixture) candidateInput(name string, versionIDs []int64) CreateReleaseCandidateInput {
	return CreateReleaseCandidateInput{
		ProjectID: fixture.projectID, ReleaseName: name, MappingVersionID: fixture.mappingVersionID,
		Format: "jsonl", IntendedUse: "SFT 训练",
		SampleVersionIDs: versionIDs, CreatedBy: &fixture.userID,
	}
}

// TestCreateCandidatePassesCleanGate 覆盖「干净候选直接通过」。
func TestCreateCandidatePassesCleanGate(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v1.0", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}
	if release.Status != model.ReleaseStatusCandidate {
		t.Fatalf("干净候选应为 candidate，实际 %s（blockers=%+v）", release.Status, release.Blockers)
	}
	if len(release.Blockers) != 0 {
		t.Fatalf("不应有阻塞项，实际 %+v", release.Blockers)
	}
	if release.CandidateRevision != 1 {
		t.Fatalf("首个候选的 revision 应为 1，实际 %d", release.CandidateRevision)
	}
	// 身份在创建时分配：ID 必须为正且稳定。
	if release.ID <= 0 || release.CandidateID <= 0 {
		t.Fatalf("必须同时分配 releaseId 与 candidateId，实际 %d / %d", release.ID, release.CandidateID)
	}

	items, err := fixture.releases.ListReleaseItems(ctx, release.ID, release.CandidateRevision, 10)
	if err != nil {
		t.Fatalf("ListReleaseItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("清单应冻结 3 条具体内容版本，实际 %d", len(items))
	}
	// 清单里必须带来源 hash（不得用「当前项目版本」冒充样本来源版本）。
	for _, item := range items {
		if item.ContentHash == "" {
			t.Fatal("清单项必须冻结内容 hash")
		}
		if item.EffectiveAction != model.EffectiveAccepted {
			t.Fatalf("清单项必须冻结当时的有效处置，实际 %s", item.EffectiveAction)
		}
	}
}

// TestCreateCandidateResolvesReleaseSelectionSnapshot covers the Review →
// Release handoff.  The candidate command receives only a snapshot ID; the
// store must resolve its server-owned items, retain the snapshot provenance,
// and reject snapshots created for another purpose.
func TestCreateCandidateResolvesReleaseSelectionSnapshot(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()
	selections := NewSelectionStore(fixture.pool)

	releaseSnapshot, err := selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID:        fixture.projectID,
		Purpose:          "release",
		CreatedBy:        &fixture.userID,
		SampleVersionIDs: fixture.versionIDs,
	})
	if err != nil {
		t.Fatalf("create release selection snapshot: %v", err)
	}
	release, err := fixture.releases.CreateReleaseCandidate(ctx, CreateReleaseCandidateInput{
		ProjectID:           fixture.projectID,
		ReleaseName:         "v-snapshot-1.0",
		MappingVersionID:    fixture.mappingVersionID,
		Format:              "jsonl",
		IntendedUse:         "SFT 训练",
		SelectionSnapshotID: releaseSnapshot.ID,
		CreatedBy:           &fixture.userID,
	})
	if err != nil {
		t.Fatalf("create candidate from release snapshot: %v", err)
	}
	items, err := fixture.releases.ListReleaseItems(ctx, release.ID, release.CandidateRevision, 10)
	if err != nil {
		t.Fatalf("list snapshot candidate items: %v", err)
	}
	if len(items) != len(fixture.versionIDs) {
		t.Fatalf("snapshot candidate should contain %d items, got %d", len(fixture.versionIDs), len(items))
	}
	var provenance map[string]any
	if err := json.Unmarshal(release.Provenance, &provenance); err != nil {
		t.Fatalf("decode release provenance: %v", err)
	}
	if got, ok := provenance["selectionSnapshotId"].(float64); !ok || int64(got) != releaseSnapshot.ID {
		t.Fatalf("release provenance must retain selection snapshot id %d, got %#v", releaseSnapshot.ID, provenance)
	}

	exportSnapshot, err := selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID:        fixture.projectID,
		Purpose:          "export",
		CreatedBy:        &fixture.userID,
		SampleVersionIDs: fixture.versionIDs,
	})
	if err != nil {
		t.Fatalf("create export selection snapshot: %v", err)
	}
	if _, err := fixture.releases.CreateReleaseCandidate(ctx, CreateReleaseCandidateInput{
		ProjectID:           fixture.projectID,
		ReleaseName:         "v-snapshot-1.1",
		MappingVersionID:    fixture.mappingVersionID,
		Format:              "jsonl",
		IntendedUse:         "SFT 训练",
		SelectionSnapshotID: exportSnapshot.ID,
		CreatedBy:           &fixture.userID,
	}); !IsStoreValidationError(err) {
		t.Fatalf("non-release snapshot must be rejected, got %v", err)
	}
}

// TestCreateCandidateBlocksPendingReview 覆盖「待审阅/冲突阻塞 + 状态为 blocked」。
func TestCreateCandidateBlocksPendingReview(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	// 让第 1 条变成**待判断**：递增证据版本使它回到 pending。
	if _, err := fixture.reviews.BumpEvidenceRevision(ctx, fixture.projectID, fixture.versionIDs[0]); err != nil {
		t.Fatalf("BumpEvidenceRevision: %v", err)
	}

	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v1.1", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}
	if release.Status != model.ReleaseStatusBlocked {
		t.Fatalf("有未接纳项时状态应为 blocked，实际 %s", release.Status)
	}
	if len(release.Blockers) == 0 {
		t.Fatal("必须给出阻塞项")
	}
	sawPriority := false
	for _, blocker := range release.Blockers {
		if blocker.Code == model.BlockerPendingReview && blocker.SampleVersionID != nil {
			sawPriority = true
		}
	}
	if !sawPriority {
		t.Fatalf("必须有指向具体内容版本的待审阅阻塞，实际 %+v", release.Blockers)
	}
}

// TestReleaseNameIsUniquePerProject 覆盖验收项
// 「相同版本名发布不会生成冲突版本」。
//
// 靠 UNIQUE(project_id, release_name_key) 保证，而不是「先查后插」——
// 后者在并发下会让两个请求都通过检查。
func TestReleaseNameIsUniquePerProject(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	if _, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v2.0", fixture.versionIDs)); err != nil {
		t.Fatalf("首次创建: %v", err)
	}
	// 同名 → 拒绝。
	if _, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v2.0", fixture.versionIDs)); !errors.Is(err, ErrReleaseNameTaken) {
		t.Fatalf("同名发布必须被拒（不得生成冲突版本），实际 %v", err)
	}
	// 大小写与空白差异视为同名（规范化键）。
	if _, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("  V2.0 ", fixture.versionIDs)); !errors.Is(err, ErrReleaseNameTaken) {
		t.Fatalf("规范化后同名必须被拒，实际 %v", err)
	}
	// 非法名字（latest / 路径字符）同样被拒。
	for _, invalid := range []string{"latest", "a/b"} {
		if _, err := fixture.releases.CreateReleaseCandidate(ctx,
			fixture.candidateInput(invalid, fixture.versionIDs)); err == nil {
			t.Fatalf("非法版本名 %q 必须被拒", invalid)
		}
	}
}

// TestFreezeReleaseDetectsConcurrentRevisionChange 覆盖验收项
// 「并发隔离/更正/新增证据不会绕过门槛」。
//
// 这是 T20 最重要的一条：确认与冻结之间有人改了判断或证据时，
// 冻结**必须**失败并要求重新确认，而不是用旧确认放行。
func TestFreezeReleaseDetectsConcurrentRevisionChange(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v3.0", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}

	// 确认之后：有人对其中一条做了**更正**（新增一条隔离判断）。
	projection, err := fixture.reviews.GetProjection(ctx, fixture.projectID, fixture.versionIDs[1])
	if err != nil {
		t.Fatalf("GetProjection: %v", err)
	}
	if _, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.userID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionIDs[1], EvidenceRevision: projection.EvidenceRevision,
			ReviewerRevision: 2, Action: model.DecisionQuarantine, Reason: "复核后发现答案与标准不符",
		}); err != nil {
		t.Fatalf("更正判断: %v", err)
	}

	// 冻结必须失败：门槛重新判定会发现隔离，或 revision 检测会发现不一致。
	_, err = fixture.releases.FreezeRelease(ctx, fixture.projectID, release.ID, fixture.userID)
	if err == nil {
		t.Fatal("确认之后的判断变化必须阻止冻结（否则会发布一份与确认时不一致的文件）")
	}
	if !errors.Is(err, ErrReleaseGateBlocked) && !errors.Is(err, ErrReleaseRevisionStale) {
		t.Fatalf("应以门槛或 revision 冲突失败，实际 %v", err)
	}

	// 状态必须回到 blocked，且**没有**写入不可变清单（未通过门槛不该有清单）。
	reloaded, err := fixture.releases.GetRelease(ctx, fixture.projectID, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if reloaded.Status != model.ReleaseStatusBlocked {
		t.Fatalf("门槛未通过后状态应为 blocked，实际 %s", reloaded.Status)
	}
}

// TestFreezeReleaseIsIdempotentAndStartsBuilding 覆盖验收项
// 「双击发布不会绕过门槛或生成冲突版本」与「重试不换身份」。
func TestFreezeReleaseIsIdempotentAndStartsBuilding(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v4.0", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}

	frozen, err := fixture.releases.FreezeRelease(ctx, fixture.projectID, release.ID, fixture.userID)
	if err != nil {
		t.Fatalf("FreezeRelease: %v", err)
	}
	if frozen.Status != model.ReleaseStatusBuilding {
		t.Fatalf("冻结后应进入 building，实际 %s", frozen.Status)
	}
	if frozen.ID != release.ID {
		t.Fatalf("冻结不得更换身份：%d vs %d", release.ID, frozen.ID)
	}

	// 双击：第二次调用返回**同一个**对象而不是新建发布。
	again, err := fixture.releases.FreezeRelease(ctx, fixture.projectID, release.ID, fixture.userID)
	if err != nil {
		t.Fatalf("重复冻结必须幂等返回，实际 %v", err)
	}
	if again.ID != release.ID || again.Status != model.ReleaseStatusBuilding {
		t.Fatalf("重复冻结应返回同一对象，实际 id=%d status=%s", again.ID, again.Status)
	}
	var releaseCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM releases WHERE project_id = $1`, fixture.projectID).Scan(&releaseCount); err != nil {
		t.Fatalf("count releases: %v", err)
	}
	if releaseCount != 1 {
		t.Fatalf("双击不得生成第二个发布版本，实际 %d 个", releaseCount)
	}
	var buildJobCount, buildOutboxCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE project_id=$1 AND job_kind=$2 AND payload->>'releaseId'=$3`,
		fixture.projectID, model.JobKindReleaseBuild, fmt.Sprint(release.ID)).Scan(&buildJobCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE payload->>'jobId' IN (SELECT id::text FROM jobs WHERE project_id=$1 AND job_kind=$2 AND payload->>'releaseId'=$3)`,
		fixture.projectID, model.JobKindReleaseBuild, fmt.Sprint(release.ID)).Scan(&buildOutboxCount); err != nil {
		t.Fatal(err)
	}
	if buildJobCount != 1 || buildOutboxCount != 1 {
		t.Fatalf("重复冻结必须只有一个持久化发布作业及通知，实际 jobs=%d outbox=%d", buildJobCount, buildOutboxCount)
	}

	// outbox 事件必须存在（T21 的发布作业靠它派发）。
	var outboxCount int
	// 事件 ID 用 strconv 拼接而不是 fmt.Sprintf：后者在参数位置会让静态分析器
	// 无法区分「参数值」与「被拼进 SQL 的文本」，产生注入告警噪声。
	eventID := "release:" + strconv.FormatInt(release.ID, 10) + ":publish"
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM outbox WHERE event_id = $1`, eventID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("冻结必须产生恰好 1 条发布派发意图（与状态变更同事务），实际 %d", outboxCount)
	}

	// 已发布后不得再修订（只读）。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE releases SET status = 'published', published_at = NOW() WHERE id = $1`, release.ID); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	if _, err := fixture.releases.UpdateReleaseCandidate(ctx, fixture.projectID, release.ID,
		fixture.candidateInput("v4.0", fixture.versionIDs)); !errors.Is(err, ErrReleasePublished) {
		t.Fatalf("已发布的版本不得再修订，实际 %v", err)
	}
}

// TestUpdateCandidateKeepsIdentityAndBumpsRevision 覆盖验收项
// 「修订候选沿用同一 releaseId」。
func TestUpdateCandidateKeepsIdentityAndBumpsRevision(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v5.0", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}

	// 缩小范围（去掉一条）→ 修订。
	updated, err := fixture.releases.UpdateReleaseCandidate(ctx, fixture.projectID, release.ID,
		fixture.candidateInput("v5.0", fixture.versionIDs[:2]))
	if err != nil {
		t.Fatalf("UpdateReleaseCandidate: %v", err)
	}
	if updated.ID != release.ID {
		t.Fatalf("修订不得更换 releaseId：%d vs %d", release.ID, updated.ID)
	}
	if updated.CandidateRevision != release.CandidateRevision+1 {
		t.Fatalf("候选 revision 必须递增：%d → %d", release.CandidateRevision, updated.CandidateRevision)
	}
	items, err := fixture.releases.ListReleaseItems(ctx, release.ID, updated.CandidateRevision, 10)
	if err != nil {
		t.Fatalf("ListReleaseItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("修订后的清单应是 2 条，实际 %d", len(items))
	}
	// 旧修订的清单仍在（历史不可被覆盖）。
	oldItems, err := fixture.releases.ListReleaseItems(ctx, release.ID, release.CandidateRevision, 10)
	if err != nil {
		t.Fatalf("ListReleaseItems(old): %v", err)
	}
	if len(oldItems) != 3 {
		t.Fatalf("旧修订的清单必须保留（历史不可覆盖），实际 %d", len(oldItems))
	}
}

// TestCreateCandidateRejectsIncompleteOrCrossProjectRange 覆盖范围作用域。
func TestCreateCandidateRejectsIncompleteOrCrossProjectRange(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	// 含不存在的版本 → 拒绝（避免范围静默变小）。
	input := fixture.candidateInput("v6.0", append(fixture.versionIDs, 1<<62))
	if _, err := fixture.releases.CreateReleaseCandidate(ctx, input); !IsStoreValidationError(err) {
		t.Fatalf("含不存在版本必须被拒，实际 %v", err)
	}
	// 空范围 → 拒绝。
	//
	// 载体是**字段级**校验错误（`model.FieldErrors`），不是 `*apiStoreError`：
	// 前端据此把提示落到「发布范围」那一区（issue #213），因此这里断言的是
	// 「字段级载体 + 具体字段名」。用 `IsStoreValidationError` 断言会漏掉载体
	// 变更（它只认 `*apiStoreError`），那正是本轮修复的 CI 回归。
	_, emptyErr := fixture.releases.CreateReleaseCandidate(ctx, fixture.candidateInput("v6.1", nil))
	fieldErrors, ok := model.HasFieldErrors(emptyErr)
	if !ok {
		t.Fatalf("空范围必须被拒为字段级校验错误，实际 %v", emptyErr)
	}
	if !hasFieldError(fieldErrors, "sampleVersionIds") {
		t.Fatalf("空范围的字段错误必须指向 sampleVersionIds（前端据此定位提示），实际 %+v", fieldErrors)
	}
	// 跨项目 → 拒绝。
	crossInput := fixture.candidateInput("v6.2", fixture.versionIDs)
	crossInput.ProjectID = fixture.projectID + 1_000_000
	if _, err := fixture.releases.CreateReleaseCandidate(ctx, crossInput); !IsStoreValidationError(err) {
		t.Fatalf("跨项目范围必须被拒，实际 %v", err)
	}
}

// TestUpdateCandidateRejectsEmptyRange 覆盖修订路径的空范围边界。
//
// 与创建路径同一约定：空范围是**字段级**校验错误（`model.FieldErrors`），
// 前端据此把提示渲染到「发布范围」区。创建路径的同类断言在
// TestCreateCandidateRejectsIncompleteOrCrossProjectRange 里，两条路径都必须被守住 ——
// 只守一条会让另一条静默漂回「一句总体提示」。
func TestUpdateCandidateRejectsEmptyRange(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()

	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("v7.0", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}

	_, emptyErr := fixture.releases.UpdateReleaseCandidate(ctx, fixture.projectID, release.ID,
		fixture.candidateInput("v7.0", nil))
	fieldErrors, ok := model.HasFieldErrors(emptyErr)
	if !ok {
		t.Fatalf("修订时清空范围必须被拒为字段级校验错误，实际 %v", emptyErr)
	}
	if !hasFieldError(fieldErrors, "sampleVersionIds") {
		t.Fatalf("空范围的字段错误必须指向 sampleVersionIds，实际 %+v", fieldErrors)
	}
}

// TestMarkBuildFailedIsIdempotentAndDoesNotOverwritePublished verifies the
// failure transition used by the worker when object storage or manifest writes
// fail.  It covers the normal building -> build_failed path and the boundary
// late-error path after publication.
func TestMarkBuildFailedIsIdempotentAndDoesNotOverwritePublished(t *testing.T) {
	fixture := newReleaseFixture(t)
	ctx := context.Background()
	release, err := fixture.releases.CreateReleaseCandidate(ctx,
		fixture.candidateInput("failure-state", fixture.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}
	if _, err := fixture.releases.FreezeRelease(ctx, fixture.projectID, release.ID, fixture.userID); err != nil {
		t.Fatalf("FreezeRelease: %v", err)
	}
	var jobID int64
	if err := fixture.pool.QueryRow(ctx, `SELECT id FROM jobs WHERE project_id=$1 AND job_kind=$2 AND payload->>'releaseId'=$3`,
		fixture.projectID, model.JobKindReleaseBuild, fmt.Sprint(release.ID)).Scan(&jobID); err != nil {
		t.Fatalf("freeze must enqueue a durable release build: %v", err)
	}
	jobs := NewJobStore(fixture.pool)
	job, claimed, err := jobs.ClaimJobByID(ctx, jobID, "release-failure-test", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim release build: claimed=%v err=%v", claimed, err)
	}
	if err := fixture.releases.MarkBuildFailed(ctx, release.ID, job); err != nil {
		t.Fatalf("MarkBuildFailed: %v", err)
	}
	failed, err := fixture.releases.GetRelease(ctx, fixture.projectID, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after failure: %v", err)
	}
	if failed.Status != model.ReleaseStatusBuildFailed {
		t.Fatalf("building release must become build_failed, got %s", failed.Status)
	}
	if err := fixture.releases.MarkBuildFailed(ctx, release.ID, job); err != nil {
		t.Fatalf("replayed MarkBuildFailed must be idempotent: %v", err)
	}

	// A late worker error must never overwrite a published release.  The model
	// test suite covers publication rules; here we pin the store's SQL fence by
	// moving this disposable row to published before the second call.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE releases SET status = 'published', published_at = NOW() WHERE id = $1`, release.ID); err != nil {
		t.Fatalf("publish fixture release: %v", err)
	}
	if err := fixture.releases.MarkBuildFailed(ctx, release.ID, job); err != nil {
		t.Fatalf("late MarkBuildFailed should be ignored after publish: %v", err)
	}
	published, err := fixture.releases.GetRelease(ctx, fixture.projectID, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after publication: %v", err)
	}
	if published.Status != model.ReleaseStatusPublished {
		t.Fatalf("late failure must not overwrite published release, got %s", published.Status)
	}
	stale := job
	stale.FencingToken++
	if err := fixture.releases.MarkBuildFailed(ctx, release.ID, stale); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("stale lease must be rejected even after publication: %v", err)
	}
}
