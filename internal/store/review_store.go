package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现追加式人工判断、分派与冲突协调（Issue #160 T16）。
//
// 契约：docs/plans/atelier-api-contract.md §2.7；docs/plans/atelier-implementation.md
// §4.1/§4.3；sql/migrations/0030_studio_review.sql 的文件头（表结构取舍）。
//
// 三条本文件必须保证的性质：
//
//  1. **只追加**：没有任何 UPDATE review_decisions 的语句。更正 = 新增一条
//     指向被取代者（supersedes）。因此「谁在什么时候判了什么、为什么」
//     永远可追溯，而投影只反映**当前有效**的那部分。
//  2. **投影在事务里递增**：aggregate_review_revision 与决策插入同一事务，
//     供 T20 冻结候选时检测竞争（「我确认的是这一版判断吗」）。
//  3. **两套 revision 各管一件事**：
//     reviewer_revision 防「同一人并发更正」（后到者 409 并保留输入）；
//     evidence_revision 防「旧证据的迟到提交」（证据集变了就必须重新判断）。

var (
	// ErrReviewerRevisionStale 表示同一审阅者的个人序号已过期。
	// 调用方必须返回 409 **并保留用户输入**（契约 §2.7）。
	ErrReviewerRevisionStale = errors.New("你的判断已被同一账号的另一次提交取代，请重新加载后保留理由再提交")
	// ErrEvidenceRevisionStale 表示提交时确认的必需证据集版本已过期。
	// 这是「旧证据的迟到提交」：证据集变了就必须基于新证据重新判断。
	ErrEvidenceRevisionStale = errors.New("必需证据集已变化，请基于最新证据重新判断后再提交")
	// ErrReviewConflictUnresolved 表示存在未协调的相反判断。
	ErrReviewConflictUnresolved = errors.New("存在相反的人工判断，需要项目负责人追加协调决定")
)

// ReviewStore 提供判断、分派与投影读写。
type ReviewStore struct {
	db *pgxpool.Pool
}

// NewReviewStore 构造判断 store。
func NewReviewStore(db *pgxpool.Pool) *ReviewStore {
	return &ReviewStore{db: db}
}

// SubmitDecisionResult 是一次提交的结果。
type SubmitDecisionResult struct {
	Decision   model.ReviewDecision   `json:"decision"`
	Projection model.ReviewProjection `json:"projection"`
	// Replayed 表示这是同一序号的幂等重放（网络重试），不是第二次判断。
	Replayed bool `json:"replayed"`
}

// SubmitDecision 追加一条判断并更新投影。
//
// 事务内的顺序刻意是「锁投影行 → 校验两套 revision → 插入 → 重算投影」：
//   - 先锁投影行，使「校验 revision」与「写入」之间没有窗口
//     （否则两个并发请求可以同时通过校验，各自写入 —— 而它们本该有一个 409）；
//   - 重算投影用**同一批有效判断**，因此冲突判定不会读到半个状态。
func (s *ReviewStore) SubmitDecision(ctx context.Context, projectID, reviewerID int64, input model.SubmitDecisionInput) (SubmitDecisionResult, error) {
	if err := model.ValidateSubmitDecision(input); err != nil {
		return SubmitDecisionResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SubmitDecisionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 校验内容版本属于本项目（跨项目引用必须被拒）。
	var sampleID int64
	var contentHash string
	var versionNumber int
	if err := tx.QueryRow(ctx, `
    SELECT sv.sample_id, sv.content_hash, sv.version
    FROM sample_versions sv
    WHERE sv.id = $1 AND sv.project_id = $2`, input.SampleVersionID, projectID,
	).Scan(&sampleID, &contentHash, &versionNumber); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SubmitDecisionResult{}, &apiStoreError{Message: "要判断的内容版本不存在或不属于本项目"}
		}
		return SubmitDecisionResult{}, err
	}

	projection, err := lockProjectionTx(ctx, tx, projectID, input.SampleVersionID, contentHash)
	if err != nil {
		return SubmitDecisionResult{}, err
	}

	// 幂等重放：同一 (版本, 审阅者, 序号) 已存在 → 回放原结果，不新增判断。
	// 网络重试与双击都会走到这里；当成第二次判断会凭空多出一条相反意见。
	var existingID int64
	err = tx.QueryRow(ctx, `
    SELECT id FROM review_decisions
    WHERE sample_version_id = $1 AND reviewer_id = $2 AND reviewer_revision = $3`,
		input.SampleVersionID, reviewerID, input.ReviewerRevision).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SubmitDecisionResult{}, err
	}
	if existingID != 0 {
		decision, err := getDecisionTx(ctx, tx, existingID)
		if err != nil {
			return SubmitDecisionResult{}, err
		}
		// 「同序号 + 同内容」才是幂等重放；「同序号 + **不同**内容」必须 409。
		//
		// 这里与契约 §1.3 的幂等语义一致（同键同请求回放、同键不同请求 409），
		// 而 reviewer_revision 就是这条判断的幂等键。
		// 无条件回放（初版实现）会**静默丢弃**用户真正想提交的那次更正 ——
		// 而这正是「过期返回 409 并保留输入」要防的（由测试发现）。
		if decision.Action != input.Action || strings.TrimSpace(decision.Reason) != strings.TrimSpace(input.Reason) {
			return SubmitDecisionResult{}, ErrReviewerRevisionStale
		}
		if err := tx.Commit(ctx); err != nil {
			return SubmitDecisionResult{}, err
		}
		return SubmitDecisionResult{Decision: decision, Projection: projection, Replayed: true}, nil
	}

	// 个人序号检查：只接受**恰好下一个**序号。
	// 允许「跳过」会让并发更正的空隙被填上，而那个空隙正是要暴露的冲突。
	var maxRevision int64
	if err := tx.QueryRow(ctx, `
    SELECT COALESCE(MAX(reviewer_revision), 0) FROM review_decisions
    WHERE sample_version_id = $1 AND reviewer_id = $2`,
		input.SampleVersionID, reviewerID).Scan(&maxRevision); err != nil {
		return SubmitDecisionResult{}, err
	}
	if input.ReviewerRevision != maxRevision+1 {
		return SubmitDecisionResult{}, ErrReviewerRevisionStale
	}

	// 证据版本检查：提交者必须确认**当前**的证据集。
	if input.EvidenceRevision != projection.EvidenceRevision {
		return SubmitDecisionResult{}, ErrEvidenceRevisionStale
	}

	// supersedes 必须指向同一内容版本上的判断（不能跨版本/跨项目更正）。
	if input.Supersedes != nil {
		var targetVersionID int64
		if err := tx.QueryRow(ctx, `
      SELECT sample_version_id FROM review_decisions WHERE id = $1 AND project_id = $2`,
			*input.Supersedes, projectID).Scan(&targetVersionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return SubmitDecisionResult{}, &apiStoreError{Message: "要更正的那条判断不存在或不属于本项目"}
			}
			return SubmitDecisionResult{}, err
		}
		if targetVersionID != input.SampleVersionID {
			return SubmitDecisionResult{}, &apiStoreError{Message: "只能更正同一内容版本上的判断"}
		}
	}

	var decisionID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO review_decisions
      (project_id, sample_id, sample_version_id, content_hash, evidence_revision,
       reviewer_id, reviewer_revision, action, reason, supersedes)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    RETURNING id`,
		projectID, sampleID, input.SampleVersionID, contentHash, input.EvidenceRevision,
		reviewerID, input.ReviewerRevision, input.Action, strings.TrimSpace(input.Reason),
		input.Supersedes).Scan(&decisionID); err != nil {
		return SubmitDecisionResult{}, err
	}

	updated, err := recomputeProjectionTx(ctx, tx, projectID, input.SampleVersionID, contentHash, projection)
	if err != nil {
		return SubmitDecisionResult{}, err
	}

	// 审计与判断同事务：不允许「写了判断但没有审计记录」。
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: reviewerID, Action: "review_decision", Resource: "sample_version",
		ResourceID: fmt.Sprint(input.SampleVersionID), ProjectID: projectID,
		Reason: fmt.Sprintf("action=%s revision=%d effective=%s",
			input.Action, input.ReviewerRevision, updated.EffectiveAction),
	}); err != nil {
		return SubmitDecisionResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return SubmitDecisionResult{}, err
	}

	decision, err := s.getDecision(ctx, decisionID)
	if err != nil {
		return SubmitDecisionResult{}, err
	}
	return SubmitDecisionResult{Decision: decision, Projection: updated}, nil
}

// ResolveConflict 追加一条**协调决定**（项目 owner 专用）。
//
// 为什么协调决定也用同一张表：它同样是一种「谁在什么时候基于什么理由做了什么
// 裁定」的追加式事实，因此必须有理由、必须可追溯、同样不可原地修改。
// 它与普通判断的区别只在 `resolution_of` 标记（指向被协调的那条判断）。
func (s *ReviewStore) ResolveConflict(ctx context.Context, projectID, actorID, sampleVersionID int64, action, reason string, resolutionOf *int64) (SubmitDecisionResult, error) {
	if strings.TrimSpace(reason) == "" {
		return SubmitDecisionResult{}, &apiStoreError{Message: "协调决定必须填写理由"}
	}
	switch action {
	case model.DecisionAccept, model.DecisionQuarantine:
	default:
		return SubmitDecisionResult{}, &apiStoreError{Message: "协调决定只能是 accepted 或 quarantined"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SubmitDecisionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sampleID int64
	var contentHash string
	if err := tx.QueryRow(ctx, `
    SELECT sample_id, content_hash FROM sample_versions
    WHERE id = $1 AND project_id = $2`, sampleVersionID, projectID).Scan(&sampleID, &contentHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SubmitDecisionResult{}, &apiStoreError{Message: "要协调的内容版本不存在或不属于本项目"}
		}
		return SubmitDecisionResult{}, err
	}

	projection, err := lockProjectionTx(ctx, tx, projectID, sampleVersionID, contentHash)
	if err != nil {
		return SubmitDecisionResult{}, err
	}
	if !projection.Conflict {
		return SubmitDecisionResult{}, &apiStoreError{Message: "当前没有未协调的判断冲突"}
	}

	// 协调决定的个人序号同样递增：owner 也可能并发更正自己的裁定。
	var maxRevision int64
	if err := tx.QueryRow(ctx, `
    SELECT COALESCE(MAX(reviewer_revision), 0) FROM review_decisions
    WHERE sample_version_id = $1 AND reviewer_id = $2`, sampleVersionID, actorID).Scan(&maxRevision); err != nil {
		return SubmitDecisionResult{}, err
	}

	var decisionID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO review_decisions
      (project_id, sample_id, sample_version_id, content_hash, evidence_revision,
       reviewer_id, reviewer_revision, action, reason, resolution_of)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    RETURNING id`,
		projectID, sampleID, sampleVersionID, contentHash, projection.EvidenceRevision,
		actorID, maxRevision+1, action, strings.TrimSpace(reason), resolutionOf).Scan(&decisionID); err != nil {
		return SubmitDecisionResult{}, err
	}

	updated, err := recomputeProjectionTx(ctx, tx, projectID, sampleVersionID, contentHash, projection)
	if err != nil {
		return SubmitDecisionResult{}, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "review_conflict_resolved", Resource: "sample_version",
		ResourceID: fmt.Sprint(sampleVersionID), ProjectID: projectID,
		Reason: fmt.Sprintf("resolution=%s effective=%s", action, updated.EffectiveAction),
	}); err != nil {
		return SubmitDecisionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SubmitDecisionResult{}, err
	}

	decision, err := s.getDecision(ctx, decisionID)
	if err != nil {
		return SubmitDecisionResult{}, err
	}
	return SubmitDecisionResult{Decision: decision, Projection: updated}, nil
}

// BumpEvidenceRevision 递增某内容版本的必需证据集版本。
//
// 触发场景（T16 验收项）：实验补齐、发现新风险、修订必需策略。
// 递增后**旧接纳不再构成有效接纳**（投影回到待判断）——
// 这正是「新风险不能沿用旧接纳发布」的实现方式。
//
// 返回新的证据版本与是否更新了投影（内容版本无投影时只返回版本号，
// 因为它还没有人判断过）。
func (s *ReviewStore) BumpEvidenceRevision(ctx context.Context, projectID, sampleVersionID int64) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var contentHash string
	if err := tx.QueryRow(ctx, `
    SELECT content_hash FROM sample_versions WHERE id = $1 AND project_id = $2`,
		sampleVersionID, projectID).Scan(&contentHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, &apiStoreError{Message: "内容版本不存在或不属于本项目"}
		}
		return 0, err
	}

	projection, err := lockProjectionTx(ctx, tx, projectID, sampleVersionID, contentHash)
	if err != nil {
		return 0, err
	}
	next := projection.EvidenceRevision + 1
	if _, err := tx.Exec(ctx, `
    UPDATE review_projections SET evidence_revision = $2, updated_at = NOW()
    WHERE sample_version_id = $1`, sampleVersionID, next); err != nil {
		return 0, err
	}
	projection.EvidenceRevision = next

	// 递增证据版本会改变「有效接纳」的判定，因此也要递增聚合序号：
	// T20 冻结候选时必须能发现「证据在确认之后变了」。
	if _, err := recomputeProjectionTx(ctx, tx, projectID, sampleVersionID, contentHash, projection); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return next, nil
}

// GetProjection 读取投影（不存在时返回一个「尚无判断」的零值投影）。
//
// 返回零值而不是错误：一块从未被判断过的内容的投影状态就是「待判断」，
// 而把「没有行」当成错误会让发布门槛计算对未判断内容报 500。
func (s *ReviewStore) GetProjection(ctx context.Context, projectID, sampleVersionID int64) (model.ReviewProjection, error) {
	var projection model.ReviewProjection
	err := s.db.QueryRow(ctx, `
    SELECT sample_version_id, project_id, content_hash, evidence_revision,
           aggregate_review_revision, effective_action, conflict, decision_count,
           pending_reason, updated_at
    FROM review_projections
    WHERE sample_version_id = $1 AND project_id = $2`, sampleVersionID, projectID,
	).Scan(&projection.SampleVersionID, &projection.ProjectID, &projection.ContentHash,
		&projection.EvidenceRevision, &projection.AggregateReviewRevision,
		&projection.EffectiveAction, &projection.Conflict, &projection.DecisionCount,
		&projection.PendingReason, &projection.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ReviewProjection{
			SampleVersionID: sampleVersionID, ProjectID: projectID,
			EffectiveAction: model.EffectivePending,
			PendingReason:   model.PendingReasonNoDecision,
		}, nil
	}
	if err != nil {
		return model.ReviewProjection{}, err
	}
	return projection, nil
}

// ListDecisions 列出一个内容版本上的全部判断（含被取代的）。
//
// 刻意返回**全部**（而不是只返回有效的）：界面要能显示「这条原来判过什么、
// 后来谁更正了」，而那正是 `supersedes` 链的价值。
func (s *ReviewStore) ListDecisions(ctx context.Context, projectID, sampleVersionID int64) ([]model.ReviewDecision, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, project_id, sample_id, sample_version_id, content_hash, evidence_revision,
           reviewer_id, reviewer_revision, action, reason, supersedes, resolution_of, created_at
    FROM review_decisions
    WHERE project_id = $1 AND sample_version_id = $2
    ORDER BY id`, projectID, sampleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ReviewDecision{}
	for rows.Next() {
		var decision model.ReviewDecision
		if err := rows.Scan(&decision.ID, &decision.ProjectID, &decision.SampleID,
			&decision.SampleVersionID, &decision.ContentHash, &decision.EvidenceRevision,
			&decision.ReviewerID, &decision.ReviewerRevision, &decision.Action,
			&decision.Reason, &decision.Supersedes, &decision.ResolutionOf,
			&decision.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, decision)
	}
	return items, rows.Err()
}

// ListProjections 按有效处置列出投影（审阅队列的服务端筛选）。
func (s *ReviewStore) ListProjections(ctx context.Context, projectID, effectiveAction string, cursorID int64, limit int) ([]model.ReviewProjection, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT sample_version_id, project_id, content_hash, evidence_revision,
           aggregate_review_revision, effective_action, conflict, decision_count,
           pending_reason, updated_at
    FROM review_projections
    WHERE project_id = $1
      AND ($2 = '' OR effective_action = $2)
      AND ($3 = 0 OR sample_version_id > $3)
    ORDER BY sample_version_id
    LIMIT $4`, projectID, strings.TrimSpace(effectiveAction), cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ReviewProjection{}
	for rows.Next() {
		var projection model.ReviewProjection
		if err := rows.Scan(&projection.SampleVersionID, &projection.ProjectID, &projection.ContentHash,
			&projection.EvidenceRevision, &projection.AggregateReviewRevision,
			&projection.EffectiveAction, &projection.Conflict, &projection.DecisionCount,
			&projection.PendingReason, &projection.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, projection)
	}
	return items, rows.Err()
}

// AssignReviewInput 是一次分派请求。
type AssignReviewInput struct {
	ProjectID       int64
	SampleVersionID int64
	// RiskKey 是**风险聚合键**：同一内容版本上的同一风险只保留一个待办。
	// 一条内容被 5 条规则命中时不做聚合就会生成 5 个待办，把队列淹掉。
	RiskKey    string
	AssigneeID *int64
	AssignedBy *int64
	Note       string
}

// AssignReview 分派（或重新分派）一条待办。
//
// 重新分派**更新**已有的未完成待办而不是新增（uniq_review_assignment_open 保证），
// 并把旧受派人写进 note —— 这样「重新分派不丢记录」成立：
// 谁曾在什么时候被派过这件事有痕迹，而不是被静默覆盖。
func (s *ReviewStore) AssignReview(ctx context.Context, input AssignReviewInput) (model.ReviewAssignment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.ReviewAssignment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sampleID int64
	if err := tx.QueryRow(ctx, `
    SELECT sample_id FROM sample_versions WHERE id = $1 AND project_id = $2`,
		input.SampleVersionID, input.ProjectID).Scan(&sampleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ReviewAssignment{}, &apiStoreError{Message: "内容版本不存在或不属于本项目"}
		}
		return model.ReviewAssignment{}, err
	}

	note := strings.TrimSpace(input.Note)
	// 先看有没有同风险的未完成待办：有则更新（并把旧受派人记进 note）。
	var existingID int64
	var previousAssignee *int64
	err = tx.QueryRow(ctx, `
    SELECT id, assignee_id FROM review_assignments
    WHERE sample_version_id = $1 AND risk_key = $2 AND status = 'open'
    ORDER BY id LIMIT 1`, input.SampleVersionID, strings.TrimSpace(input.RiskKey)).
		Scan(&existingID, &previousAssignee)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return model.ReviewAssignment{}, err
	}

	if existingID != 0 {
		if previousAssignee != nil && (input.AssigneeID == nil || *previousAssignee != *input.AssigneeID) {
			note = strings.TrimSpace(fmt.Sprintf("%s（原指派给用户 %d，已于 %s 重新分派）",
				note, *previousAssignee, time.Now().UTC().Format(time.RFC3339)))
		}
		if _, err := tx.Exec(ctx, `
      UPDATE review_assignments
      SET assignee_id = $2, assigned_by = $3, note = $4
      WHERE id = $1`, existingID, input.AssigneeID, input.AssignedBy, note); err != nil {
			return model.ReviewAssignment{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return model.ReviewAssignment{}, err
		}
		return s.getAssignment(ctx, existingID)
	}

	var assignmentID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO review_assignments
      (project_id, sample_id, sample_version_id, risk_key, assignee_id, assigned_by, note)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    RETURNING id`,
		input.ProjectID, sampleID, input.SampleVersionID, strings.TrimSpace(input.RiskKey),
		input.AssigneeID, input.AssignedBy, note).Scan(&assignmentID); err != nil {
		return model.ReviewAssignment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.ReviewAssignment{}, err
	}
	return s.getAssignment(ctx, assignmentID)
}

// ListAssignments 列出待办（可按受派人筛选）。
func (s *ReviewStore) ListAssignments(ctx context.Context, projectID int64, assigneeID int64, status string, limit int) ([]model.ReviewAssignment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if strings.TrimSpace(status) == "" {
		status = model.AssignmentOpen
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, project_id, sample_id, sample_version_id, risk_key, assignee_id, assigned_by,
           status, note, created_at, resolved_at
    FROM review_assignments
    WHERE project_id = $1 AND status = $2
      AND ($3 = 0 OR assignee_id = $3)
    ORDER BY id LIMIT $4`, projectID, status, assigneeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ReviewAssignment{}
	for rows.Next() {
		var assignment model.ReviewAssignment
		if err := rows.Scan(&assignment.ID, &assignment.ProjectID, &assignment.SampleID,
			&assignment.SampleVersionID, &assignment.RiskKey, &assignment.AssigneeID,
			&assignment.AssignedBy, &assignment.Status, &assignment.Note,
			&assignment.CreatedAt, &assignment.ResolvedAt); err != nil {
			return nil, err
		}
		items = append(items, assignment)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// lockProjectionTx 确保投影行存在并加行锁。
//
// 加锁的目的是让「校验 revision」与「写入判断」之间没有窗口：
// 没有锁时两个并发请求可以同时通过个人序号校验，各自写入 ——
// 而它们本该有一个拿到 409。这正是 T16 验收项「同一人并发更正」要测的场景。
func lockProjectionTx(ctx context.Context, tx pgx.Tx, projectID, sampleVersionID int64, contentHash string) (model.ReviewProjection, error) {
	if _, err := tx.Exec(ctx, `
    INSERT INTO review_projections (sample_version_id, project_id, content_hash)
    VALUES ($1, $2, $3)
    ON CONFLICT (sample_version_id) DO NOTHING`, sampleVersionID, projectID, contentHash); err != nil {
		return model.ReviewProjection{}, err
	}

	var projection model.ReviewProjection
	if err := tx.QueryRow(ctx, `
    SELECT sample_version_id, project_id, content_hash, evidence_revision,
           aggregate_review_revision, effective_action, conflict, decision_count,
           pending_reason, updated_at
    FROM review_projections WHERE sample_version_id = $1 FOR UPDATE`, sampleVersionID,
	).Scan(&projection.SampleVersionID, &projection.ProjectID, &projection.ContentHash,
		&projection.EvidenceRevision, &projection.AggregateReviewRevision,
		&projection.EffectiveAction, &projection.Conflict, &projection.DecisionCount,
		&projection.PendingReason, &projection.UpdatedAt); err != nil {
		return model.ReviewProjection{}, err
	}
	return projection, nil
}

// recomputeProjectionTx 由判断事实重算投影并递增聚合序号。
//
// 每次调用都递增 aggregate_review_revision（包括证据版本变化）：
// 它表达的是「这一版内容的有效判断状态变过」，
// 而 T20 冻结候选时用它检测「确认与冻结之间有没有人改过判断」。
func recomputeProjectionTx(ctx context.Context, tx pgx.Tx, projectID, sampleVersionID int64, contentHash string, previous model.ReviewProjection) (model.ReviewProjection, error) {
	rows, err := tx.Query(ctx, `
    SELECT id, project_id, sample_id, sample_version_id, content_hash, evidence_revision,
           reviewer_id, reviewer_revision, action, reason, supersedes, resolution_of, created_at
    FROM review_decisions
    WHERE project_id = $1 AND sample_version_id = $2
    ORDER BY id`, projectID, sampleVersionID)
	if err != nil {
		return model.ReviewProjection{}, err
	}
	all := []model.ReviewDecision{}
	for rows.Next() {
		var decision model.ReviewDecision
		if err := rows.Scan(&decision.ID, &decision.ProjectID, &decision.SampleID,
			&decision.SampleVersionID, &decision.ContentHash, &decision.EvidenceRevision,
			&decision.ReviewerID, &decision.ReviewerRevision, &decision.Action,
			&decision.Reason, &decision.Supersedes, &decision.ResolutionOf,
			&decision.CreatedAt); err != nil {
			rows.Close()
			return model.ReviewProjection{}, err
		}
		all = append(all, decision)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return model.ReviewProjection{}, err
	}

	effective := model.EffectiveDecisions(all)
	action, pendingReason, conflict := model.ComputeProjection(model.DecisionFacts{
		Decisions:                 effective,
		EvidenceRevision:          previous.EvidenceRevision,
		ContentHash:               contentHash,
		ProjectedContentHash:      previous.ContentHash,
		ProjectedEvidenceRevision: previous.EvidenceRevision,
	})

	projection := previous
	projection.ContentHash = contentHash
	projection.EffectiveAction = action
	projection.PendingReason = pendingReason
	projection.Conflict = conflict
	projection.DecisionCount = len(effective)
	projection.AggregateReviewRevision = previous.AggregateReviewRevision + 1

	if _, err := tx.Exec(ctx, `
    UPDATE review_projections
    SET content_hash = $2, effective_action = $3, conflict = $4, decision_count = $5,
        pending_reason = $6, aggregate_review_revision = $7, updated_at = NOW()
    WHERE sample_version_id = $1`,
		sampleVersionID, contentHash, action, conflict, len(effective), pendingReason,
		projection.AggregateReviewRevision); err != nil {
		return model.ReviewProjection{}, err
	}
	return projection, nil
}

func (s *ReviewStore) getDecision(ctx context.Context, decisionID int64) (model.ReviewDecision, error) {
	return getDecisionTx(ctx, s.db, decisionID)
}

func getDecisionTx(ctx context.Context, querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, decisionID int64) (model.ReviewDecision, error) {
	var decision model.ReviewDecision
	err := querier.QueryRow(ctx, `
    SELECT id, project_id, sample_id, sample_version_id, content_hash, evidence_revision,
           reviewer_id, reviewer_revision, action, reason, supersedes, resolution_of, created_at
    FROM review_decisions WHERE id = $1`, decisionID,
	).Scan(&decision.ID, &decision.ProjectID, &decision.SampleID, &decision.SampleVersionID,
		&decision.ContentHash, &decision.EvidenceRevision, &decision.ReviewerID,
		&decision.ReviewerRevision, &decision.Action, &decision.Reason,
		&decision.Supersedes, &decision.ResolutionOf, &decision.CreatedAt)
	if err != nil {
		return model.ReviewDecision{}, err
	}
	return decision, nil
}

func (s *ReviewStore) getAssignment(ctx context.Context, assignmentID int64) (model.ReviewAssignment, error) {
	var assignment model.ReviewAssignment
	err := s.db.QueryRow(ctx, `
    SELECT id, project_id, sample_id, sample_version_id, risk_key, assignee_id, assigned_by,
           status, note, created_at, resolved_at
    FROM review_assignments WHERE id = $1`, assignmentID,
	).Scan(&assignment.ID, &assignment.ProjectID, &assignment.SampleID, &assignment.SampleVersionID,
		&assignment.RiskKey, &assignment.AssigneeID, &assignment.AssignedBy, &assignment.Status,
		&assignment.Note, &assignment.CreatedAt, &assignment.ResolvedAt)
	if err != nil {
		return model.ReviewAssignment{}, err
	}
	return assignment, nil
}
