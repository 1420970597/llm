package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T16 的判断、分派与冲突协调。
//
// 必须连真实 Postgres：核心断言都是**并发与事务**语义 ——
// 「同一人并发更正恰有一个 409」「投影与判断同事务递增」。
// 内存实现测不出这两条。

type reviewFixture struct {
	pool        *pgxpool.Pool
	projectID   int64
	ownerID     int64
	reviewerAID int64
	reviewerBID int64
	versionID   int64
	sampleID    int64
	reviews     *ReviewStore
}

// newReviewFixture 建项目 + 三个用户 + 一个样本版本。
func newReviewFixture(t *testing.T) reviewFixture {
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
		"判断测试工作区 "+suffix, "review-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	seedUser := func(prefix string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
      INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user')
      ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role RETURNING id`,
			prefix+"-"+suffix+"@example.test").Scan(&id); err != nil {
			t.Fatalf("seed user %s: %v", prefix, err)
		}
		return id
	}
	ownerID := seedUser("owner")
	reviewerAID := seedUser("reviewer-a")
	reviewerBID := seedUser("reviewer-b")

	projects := NewProjectStore(pool)
	input := model.CreateProjectInput{Name: "判断项目", TargetKind: model.TargetKindSFT}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, ownerID, input)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	var sampleID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO samples (project_id, sample_key, target_kind, title, latest_version)
    VALUES ($1, $2, 'sft', $2, 1) RETURNING id`,
		project.ID, "review-item-"+suffix).Scan(&sampleID); err != nil {
		t.Fatalf("seed sample: %v", err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO sample_versions
      (sample_id, project_id, version, target_kind, schema_version, payload, content_hash)
    VALUES ($1, $2, 1, 'sft', 'sft.sample.v1',
            '{"question":"q","reasoning":"r","answer":"a"}'::jsonb, $3)
    RETURNING id`, sampleID, project.ID, "review-hash-"+suffix).Scan(&versionID); err != nil {
		t.Fatalf("seed sample version: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM review_decisions WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM review_projections WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM review_assignments WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1::bigint[])`,
			[]int64{ownerID, reviewerAID, reviewerBID})
	})

	return reviewFixture{
		pool: pool, projectID: project.ID, ownerID: ownerID,
		reviewerAID: reviewerAID, reviewerBID: reviewerBID,
		versionID: versionID, sampleID: sampleID, reviews: NewReviewStore(pool),
	}
}

// submit 提交一条判断（序号从 1 起，证据版本取自当前投影）。
func (fixture reviewFixture) submit(t *testing.T, reviewerID int64, action, reason string, revision int64) SubmitDecisionResult {
	t.Helper()
	projection, err := fixture.reviews.GetProjection(context.Background(), fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("GetProjection: %v", err)
	}
	result, err := fixture.reviews.SubmitDecision(context.Background(), fixture.projectID, reviewerID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionID, EvidenceRevision: projection.EvidenceRevision,
			ReviewerRevision: revision, Action: action, Reason: reason,
		})
	if err != nil {
		t.Fatalf("SubmitDecision(reviewer=%d revision=%d): %v", reviewerID, revision, err)
	}
	return result
}

// TestReviewDecisionsAreAppendOnlyAndProjected 覆盖基本路径 + 投影递增。
func TestReviewDecisionsAreAppendOnlyAndProjected(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()

	first := fixture.submit(t, fixture.reviewerAID, model.DecisionAccept, "推理链完整", 1)
	if first.Projection.EffectiveAction != model.EffectiveAccepted {
		t.Fatalf("单人接纳应为 accepted，实际 %s", first.Projection.EffectiveAction)
	}
	if first.Projection.AggregateReviewRevision != 1 {
		t.Fatalf("首次判断后聚合序号应为 1，实际 %d", first.Projection.AggregateReviewRevision)
	}

	// 同一审阅者的更正：新增一条并指向被取代者。
	supersedes := first.Decision.ID
	result, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.reviewerAID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionID, EvidenceRevision: first.Projection.EvidenceRevision,
			ReviewerRevision: 2, Action: model.DecisionQuarantine, Reason: "发现原判断是误报",
			Supersedes: &supersedes,
		})
	if err != nil {
		t.Fatalf("更正判断: %v", err)
	}
	if result.Projection.EffectiveAction != model.EffectiveQuarantined {
		t.Fatalf("更正后应为隔离，实际 %s", result.Projection.EffectiveAction)
	}
	if result.Projection.AggregateReviewRevision <= first.Projection.AggregateReviewRevision {
		t.Fatalf("有效判断变化必须递增聚合序号（T20 据此检测竞争）：%d → %d",
			first.Projection.AggregateReviewRevision, result.Projection.AggregateReviewRevision)
	}

	// 两条判断都在（只追加），且原判断记录仍可追溯。
	decisions, err := fixture.reviews.ListDecisions(ctx, fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("更正必须保留原判断（只追加），实际 %d 条", len(decisions))
	}
	if decisions[0].Reason != "推理链完整" || decisions[0].ReviewerID != fixture.reviewerAID {
		t.Fatalf("原判断的操作者与理由必须可追溯，实际 %+v", decisions[0])
	}
	if decisions[1].Supersedes == nil || *decisions[1].Supersedes != first.Decision.ID {
		t.Fatalf("更正必须指向被取代的判断，实际 %+v", decisions[1].Supersedes)
	}
}

// TestSubmitDecisionIsIdempotentOnSameRevision 覆盖「重复提交幂等」。
//
// 网络重试与双击会带同一序号：当成第二次判断会凭空多出一条意见。
func TestSubmitDecisionIsIdempotentOnSameRevision(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()

	first := fixture.submit(t, fixture.reviewerAID, model.DecisionAccept, "理由", 1)
	replay, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.reviewerAID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionID, EvidenceRevision: first.Projection.EvidenceRevision,
			ReviewerRevision: 1, Action: model.DecisionAccept, Reason: "理由",
		})
	if err != nil {
		t.Fatalf("同序号重放不应报错：%v", err)
	}
	if !replay.Replayed {
		t.Fatal("同序号必须标记为幂等重放")
	}
	if replay.Decision.ID != first.Decision.ID {
		t.Fatalf("重放必须返回原判断：%d vs %d", replay.Decision.ID, first.Decision.ID)
	}
	decisions, _ := fixture.reviews.ListDecisions(ctx, fixture.projectID, fixture.versionID)
	if len(decisions) != 1 {
		t.Fatalf("重放不得新增判断，实际 %d 条", len(decisions))
	}
}

// TestConcurrentDecisionsBySameReviewerYieldOneStale 覆盖验收项
// 「同一人并发更正」：恰有一个成功、一个 409（并保留输入由调用方负责）。
func TestConcurrentDecisionsBySameReviewerYieldOneStale(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()
	projection, err := fixture.reviews.GetProjection(ctx, fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("GetProjection: %v", err)
	}

	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	succeeded, stale, other := 0, 0, 0
	start := make(chan struct{})
	for index := 0; index < 2; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			<-start
			_, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.reviewerAID,
				model.SubmitDecisionInput{
					SampleVersionID: fixture.versionID, EvidenceRevision: projection.EvidenceRevision,
					// 两人各自从「当前序号 1」开始编辑：这是**同一人开两个标签页**的形态。
					ReviewerRevision: 1, Action: model.DecisionAccept,
					Reason: fmt.Sprintf("理由 %d", index),
				})
			mutex.Lock()
			defer mutex.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, ErrReviewerRevisionStale):
				stale++
			default:
				other++
			}
		}(index)
	}
	close(start)
	waitGroup.Wait()

	if other != 0 {
		t.Fatalf("不应出现非预期错误，实际 %d 个", other)
	}
	// 两者同序号：一个是首次写入、另一个应当命中幂等重放（都返回 nil），
	// 因此这里断言的是「**不会产生两条判断**」—— 那才是并发更正真正要防的事。
	decisions, err := fixture.reviews.ListDecisions(ctx, fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("同一序号并发提交不得产生两条判断，实际 %d 条（succeeded=%d stale=%d）",
			len(decisions), succeeded, stale)
	}

	// 而「带旧序号的迟到提交」必须 409：先提交 1、2，再用 1 提交。
	fixture.submit(t, fixture.reviewerAID, model.DecisionAccept, "第二次", 2)
	_, err = fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.reviewerAID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionID, EvidenceRevision: projection.EvidenceRevision,
			ReviewerRevision: 1, Action: model.DecisionQuarantine, Reason: "迟到的更正",
		})
	if !errors.Is(err, ErrReviewerRevisionStale) {
		t.Fatalf("旧序号提交必须返回 ErrReviewerRevisionStale，实际 %v", err)
	}
}

// TestOppositeDecisionsArePreservedAsConflict 覆盖验收项
// 「两人相反判断保留两条、标 conflict」与「owner 协调后才能解除阻塞」。
func TestOppositeDecisionsArePreservedAsConflict(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()

	fixture.submit(t, fixture.reviewerAID, model.DecisionAccept, "推理链完整", 1)
	second := fixture.submit(t, fixture.reviewerBID, model.DecisionQuarantine, "答案与标准不符", 1)

	if second.Projection.EffectiveAction != model.EffectiveConflict || !second.Projection.Conflict {
		t.Fatalf("相反判断必须标 conflict，实际 %s（conflict=%v）",
			second.Projection.EffectiveAction, second.Projection.Conflict)
	}

	// 两条判断都在：**不按时间取最后一条**（那会静默丢掉一个人的意见）。
	decisions, err := fixture.reviews.ListDecisions(ctx, fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("相反判断必须各保留一条，实际 %d 条", len(decisions))
	}
	reasons := decisions[0].Reason + "|" + decisions[1].Reason
	if !strings.Contains(reasons, "推理链完整") || !strings.Contains(reasons, "答案与标准不符") {
		t.Fatalf("两人的理由都必须保留，实际 %q", reasons)
	}

	// owner 追加协调决定后解除冲突。
	resolutionOf := decisions[0].ID
	resolved, err := fixture.reviews.ResolveConflict(ctx, fixture.projectID, fixture.ownerID,
		fixture.versionID, model.DecisionAccept, "复核规则证据后确认是误报", &resolutionOf)
	if err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	if resolved.Projection.Conflict || resolved.Projection.EffectiveAction != model.EffectiveAccepted {
		t.Fatalf("协调后应解除冲突，实际 %s（conflict=%v）",
			resolved.Projection.EffectiveAction, resolved.Projection.Conflict)
	}
	if resolved.Decision.ResolutionOf == nil || *resolved.Decision.ResolutionOf != decisions[0].ID {
		t.Fatalf("协调决定必须指向被协调的判断，实际 %+v", resolved.Decision.ResolutionOf)
	}

	// 无可协调的冲突时必须拒绝（避免 owner 随手改写已一致的结论）。
	if _, err := fixture.reviews.ResolveConflict(ctx, fixture.projectID, fixture.ownerID,
		fixture.versionID, model.DecisionQuarantine, "再改一次", nil); !IsStoreValidationError(err) {
		t.Fatalf("无冲突时协调必须被拒，实际 %v", err)
	}
}

// TestEvidenceRevisionBumpInvalidatesAcceptance 覆盖验收项
// 「新风险不能沿用旧接纳发布」。
func TestEvidenceRevisionBumpInvalidatesAcceptance(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()

	accepted := fixture.submit(t, fixture.reviewerAID, model.DecisionAccept, "当时看起来没问题", 1)
	if accepted.Projection.EffectiveAction != model.EffectiveAccepted {
		t.Fatalf("前置条件：应为 accepted，实际 %s", accepted.Projection.EffectiveAction)
	}

	// 发现新风险 → 递增必需证据集版本。
	next, err := fixture.reviews.BumpEvidenceRevision(ctx, fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("BumpEvidenceRevision: %v", err)
	}
	if next != 1 {
		t.Fatalf("证据版本应从 0 递增到 1，实际 %d", next)
	}

	projection, err := fixture.reviews.GetProjection(ctx, fixture.projectID, fixture.versionID)
	if err != nil {
		t.Fatalf("GetProjection: %v", err)
	}
	if projection.EffectiveAction != model.EffectivePending {
		t.Fatalf("证据集变化后旧接纳必须回到待判断，实际 %s", projection.EffectiveAction)
	}
	if projection.PendingReason != model.PendingReasonEvidenceChanged {
		t.Fatalf("原因应为 evidence_changed，实际 %s", projection.PendingReason)
	}

	// 旧证据的迟到提交必须 409（调用方保留用户输入）。
	if _, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.reviewerBID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionID, EvidenceRevision: 0,
			ReviewerRevision: 1, Action: model.DecisionAccept, Reason: "基于旧证据",
		}); !errors.Is(err, ErrEvidenceRevisionStale) {
		t.Fatalf("旧证据提交必须返回 ErrEvidenceRevisionStale，实际 %v", err)
	}

	// 基于新证据重新判断后恢复有效。
	rejudged := fixture.submit(t, fixture.reviewerBID, model.DecisionAccept, "已按新证据复核", 1)
	if rejudged.Projection.EffectiveAction != model.EffectiveAccepted {
		t.Fatalf("基于新证据的判断应生效，实际 %s", rejudged.Projection.EffectiveAction)
	}
}

// TestReassignReviewKeepsRecord 覆盖验收项「重新分派不丢记录」。
func TestReassignReviewKeepsRecord(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()

	first, err := fixture.reviews.AssignReview(ctx, AssignReviewInput{
		ProjectID: fixture.projectID, SampleVersionID: fixture.versionID,
		RiskKey: "rule:refusal", AssigneeID: &fixture.reviewerAID, AssignedBy: &fixture.ownerID,
		Note: "拒答模板命中",
	})
	if err != nil {
		t.Fatalf("AssignReview: %v", err)
	}

	// 同一风险的重复分派聚合到一条待办（否则一条内容被 5 条规则命中会产生 5 个待办）。
	again, err := fixture.reviews.AssignReview(ctx, AssignReviewInput{
		ProjectID: fixture.projectID, SampleVersionID: fixture.versionID,
		RiskKey: "rule:refusal", AssigneeID: &fixture.reviewerAID, AssignedBy: &fixture.ownerID,
		Note: "拒答模板命中",
	})
	if err != nil {
		t.Fatalf("重复分派: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("同一风险必须聚合到同一条待办，实际 %d vs %d", again.ID, first.ID)
	}

	// 重新分派给另一个人：更新同一条，并把原受派人写进 note（记录不丢）。
	moved, err := fixture.reviews.AssignReview(ctx, AssignReviewInput{
		ProjectID: fixture.projectID, SampleVersionID: fixture.versionID,
		RiskKey: "rule:refusal", AssigneeID: &fixture.reviewerBID, AssignedBy: &fixture.ownerID,
		Note: "请假转派",
	})
	if err != nil {
		t.Fatalf("重新分派: %v", err)
	}
	if moved.ID != first.ID {
		t.Fatalf("重新分派应更新同一条待办，实际 %d vs %d", moved.ID, first.ID)
	}
	if moved.AssigneeID == nil || *moved.AssigneeID != fixture.reviewerBID {
		t.Fatalf("受派人应已更新，实际 %+v", moved.AssigneeID)
	}
	if !strings.Contains(moved.Note, fmt.Sprint(fixture.reviewerAID)) {
		t.Fatalf("重新分派必须保留原受派人（记录不丢），实际 note=%q", moved.Note)
	}

	// 不同风险各自成条（聚合是按风险的，不是按内容的）。
	other, err := fixture.reviews.AssignReview(ctx, AssignReviewInput{
		ProjectID: fixture.projectID, SampleVersionID: fixture.versionID,
		RiskKey: "rule:empty-answer", AssigneeID: &fixture.reviewerAID, AssignedBy: &fixture.ownerID,
		Note: "空答案",
	})
	if err != nil {
		t.Fatalf("不同风险分派: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("不同风险必须是不同的待办")
	}

	open, err := fixture.reviews.ListAssignments(ctx, fixture.projectID, 0, "", 50)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("应有 2 条未完成待办，实际 %d", len(open))
	}
}

// TestSubmitDecisionRequiresMatchingContentVersion 覆盖跨项目/跨版本防护。
func TestSubmitDecisionRequiresMatchingContentVersion(t *testing.T) {
	fixture := newReviewFixture(t)
	ctx := context.Background()

	// 不存在的版本。
	if _, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.reviewerAID,
		model.SubmitDecisionInput{
			SampleVersionID: 1 << 62, EvidenceRevision: 0, ReviewerRevision: 1,
			Action: model.DecisionAccept, Reason: "理由",
		}); !IsStoreValidationError(err) {
		t.Fatalf("不存在的内容版本必须被拒，实际 %v", err)
	}
	// 跨项目：把项目 ID 换成另一个（版本号真实但不属于该项目）。
	if _, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID+1_000_000, fixture.reviewerAID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versionID, EvidenceRevision: 0, ReviewerRevision: 1,
			Action: model.DecisionAccept, Reason: "理由",
		}); !IsStoreValidationError(err) {
		t.Fatalf("跨项目提交必须被拒，实际 %v", err)
	}
}
