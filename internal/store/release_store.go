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

// 本文件实现发布候选、门槛与抗并发冻结（Issue #160 T20）。
//
// 契约：docs/plans/atelier-api-contract.md §2.8/§2.9；
// docs/plans/atelier-implementation.md §2.5/§4.2/§4.3；
// sql/migrations/0031_studio_releases.sql（表结构取舍）。
//
// 三条本文件必须保证的性质：
//
//  1. **身份在候选创建时分配且永不变**：`candidateId` 与 **`releaseId`**
//     同事务分配，版本名同时预留。发布失败重试沿用同一个 releaseId
//     （T20 验收项「重试不换身份」）。
//  2. **清单是具体内容版本**：`release_items` 存 sample_version_id + hash，
//     不保存筛选条件 —— 筛选条件会随数据变化，那正是「确认时看到的」
//     与「实际导出的」不一致的成因。
//  3. **冻结抗并发**：冻结事务对**候选行**加锁并重新判定门槛；
//     判断或证据在确认之后变化时必须 409 并要求重新确认。
//     单靠「读一遍再写」会允许「确认与冻结之间有人隔离了一条」漏过。

// ReleaseStore 提供发布候选与冻结的读写。
type ReleaseStore struct {
	db *pgxpool.Pool
}

// NewReleaseStore 构造发布 store。
func NewReleaseStore(db *pgxpool.Pool) *ReleaseStore {
	return &ReleaseStore{db: db}
}

var (
	// ErrReleaseNameTaken 表示项目内版本名已被占用。
	ErrReleaseNameTaken = errors.New("该项目已有同名发布版本，请换一个版本名")
	// ErrReleaseRevisionStale 表示确认与冻结之间判断/证据发生了变化。
	ErrReleaseRevisionStale = errors.New(
		"发布范围内的判断或证据在你确认之后发生了变化，请重新确认范围后再发布")
	// ErrReleaseGateBlocked 表示门槛未通过。
	ErrReleaseGateBlocked = errors.New("发布门槛未通过，存在阻塞项")
	// ErrReleaseNotFound 表示发布不存在或不属于本项目。
	ErrReleaseNotFound = errors.New("发布版本不存在或不属于本项目")
	// ErrReleasePublished 表示该发布已发布（只读）。
	ErrReleasePublished = errors.New("该发布版本已发布，内容与数据卡只读")
)

// Release 是一次发布（稳定身份）。
type Release struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"projectId"`
	ReleaseName string `json:"releaseName"`
	Status      string `json:"status"`
	// TargetKind 来自项目的目标类型。
	//
	// 为什么发布需要它：SFT 与 GRPO 的样本 payload 结构不同（reasoning vs
	// levels/level_rubrics），而导出编码器必须按它选择字段。发布记录里存一份
	// 使「这次发布的是什么类型的数据」不依赖项目的**当前**目标类型。
	TargetKind      string          `json:"targetKind"`
	IntendedUse     string          `json:"intendedUse"`
	Limitations     []string        `json:"limitations"`
	Provenance      json.RawMessage `json:"provenance"`
	QualitySnapshot json.RawMessage `json:"qualitySnapshot"`
	CoverageSummary json.RawMessage `json:"coverageSummary"`
	// CandidateID/CandidateRevision 是**当前候选**：页面全程用 releaseId，
	// 而候选 ID 只用于修订命令（契约 §2.8 的 PATCH 路径）。
	CandidateID       int64                  `json:"candidateId"`
	CandidateRevision int64                  `json:"candidateRevision"`
	Blockers          []model.ReleaseBlocker `json:"blockers"`
	MappingVersionID  *int64                 `json:"mappingVersionId,omitempty"`
	Format            string                 `json:"format"`
	CreatedBy         *int64                 `json:"createdBy,omitempty"`
	PublishedAt       *time.Time             `json:"publishedAt,omitempty"`
	CreatedAt         time.Time              `json:"createdAt"`
	UpdatedAt         time.Time              `json:"updatedAt"`
}

// CreateReleaseCandidateInput 是创建候选的请求（契约 §2.8）。
type CreateReleaseCandidateInput struct {
	ProjectID        int64
	ReleaseName      string
	MappingVersionID int64
	Format           string
	IntendedUse      string
	Limitations      []string
	Provenance       map[string]any
	// SampleVersionIDs 是**具体范围**（不是筛选条件）。
	SampleVersionIDs []int64
	CreatedBy        *int64
}

// CreateReleaseCandidate 创建候选：同事务分配 releaseId + 候选 + 清单 + 门槛。
//
// 版本名在同一事务里 `INSERT`（靠 UNIQUE(project_id, release_name_key) 保证唯一）：
// 先查后插会在并发下让两个请求都通过检查，从而产出两个同名版本 ——
// 而那正是「相同版本名发布不会生成冲突版本」（T20 验收项）要防的事。
func (s *ReleaseStore) CreateReleaseCandidate(ctx context.Context, input CreateReleaseCandidateInput) (Release, error) {
	if err := model.ValidateReleaseName(input.ReleaseName); err != nil {
		return Release{}, err
	}
	if len(input.SampleVersionIDs) == 0 {
		return Release{}, &apiStoreError{Message: "发布范围不能为空，请先选择要发布的内容版本"}
	}
	if input.Format == "" {
		input.Format = "jsonl"
	}
	limitations, err := json.Marshal(input.Limitations)
	if err != nil {
		return Release{}, err
	}
	provenance, err := json.Marshal(input.Provenance)
	if err != nil {
		return Release{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// **先校验范围再写 release 行**（这是一次真实缺陷的修复，由测试发现）：
	// 初版先插入 releases 再在重建清单时校验范围，于是「项目不存在」或
	// 「版本不属于该项目」会以**外键违约**的形式报出来（`releases_project_id_fkey`），
	// 而调用方会把它当成 500。校验在前给出的是可展示的输入错误。
	if err := validateReleaseRangeTx(ctx, tx, input.ProjectID, input.SampleVersionIDs); err != nil {
		return Release{}, err
	}

	var releaseID int64
	err = tx.QueryRow(ctx, `
    INSERT INTO releases
      (project_id, release_name, release_name_key, status, intended_use, limitations, provenance, created_by)
    VALUES ($1, $2, $3, 'candidate', $4, $5, $6, $7)
    RETURNING id`,
		input.ProjectID, strings.TrimSpace(input.ReleaseName),
		model.NormalizeReleaseNameKey(input.ReleaseName),
		strings.TrimSpace(input.IntendedUse), limitations, provenance, input.CreatedBy,
	).Scan(&releaseID)
	if err != nil {
		if IsUniqueViolation(err) {
			return Release{}, ErrReleaseNameTaken
		}
		return Release{}, err
	}

	candidateID, err := insertCandidateTx(ctx, tx, releaseID, input, 1)
	if err != nil {
		return Release{}, err
	}

	gate, err := rebuildReleaseItemsTx(ctx, tx, releaseID, input.ProjectID, 1, input.SampleVersionIDs, input)
	if err != nil {
		return Release{}, err
	}
	if err := persistGateTx(ctx, tx, releaseID, 1, gate, input.CreatedBy, len(input.SampleVersionIDs)); err != nil {
		return Release{}, err
	}
	// 候选状态由门槛决定：blocked 与 candidate 都是「未发布」，
	// 但界面要能区分「还没检查」与「检查过但被挡住」。
	status := model.ReleaseStatusCandidate
	if !gate.Passed {
		status = model.ReleaseStatusBlocked
	}
	if _, err := tx.Exec(ctx, `
    UPDATE releases SET status = $2, quality_snapshot = $3, coverage_summary = $4, updated_at = NOW()
    WHERE id = $1`,
		releaseID, status, mustJSONBytes(map[string]any{
			"inspected": gate.Inspected, "acceptanceRate": gate.AcceptanceRate,
			"acceptanceRateDisplay": gate.AcceptanceRateDisplay,
		}), mustJSONBytes(map[string]any{
			"originalItemCount": len(input.SampleVersionIDs),
			"excludedItemCount": countExcludedItems(gate.Blockers),
		})); err != nil {
		return Release{}, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: derefInt64(input.CreatedBy), Action: "release_candidate_create",
		Resource: "release", ResourceID: fmt.Sprint(releaseID), ProjectID: input.ProjectID,
		Reason: fmt.Sprintf("name=%s items=%d passed=%v", input.ReleaseName, len(input.SampleVersionIDs), gate.Passed),
	}); err != nil {
		return Release{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	_ = candidateID
	return s.GetRelease(ctx, input.ProjectID, releaseID)
}

// UpdateReleaseCandidate 修订候选（沿用同一 releaseId，revision +1）。
func (s *ReleaseStore) UpdateReleaseCandidate(ctx context.Context, projectID, releaseID int64, input CreateReleaseCandidateInput) (Release, error) {
	current, err := s.GetRelease(ctx, projectID, releaseID)
	if err != nil {
		return Release{}, err
	}
	if current.Status == model.ReleaseStatusPublished {
		return Release{}, ErrReleasePublished
	}
	if err := model.ValidateReleaseName(input.ReleaseName); err != nil {
		return Release{}, err
	}
	if len(input.SampleVersionIDs) == 0 {
		return Release{}, &apiStoreError{Message: "发布范围不能为空"}
	}
	if input.Format == "" {
		input.Format = current.Format
	}
	limitations, err := json.Marshal(input.Limitations)
	if err != nil {
		return Release{}, err
	}
	if input.Provenance == nil {
		input.Provenance = map[string]any{}
	}
	provenance, err := json.Marshal(input.Provenance)
	if err != nil {
		return Release{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 锁 release 行：修订与冻结必须串行，否则「修订中的候选被冻结」
	// 会写出一份与任何已确认版本都不一致的清单。
	if _, err := tx.Exec(ctx, `
    SELECT id FROM releases WHERE id = $1 AND project_id = $2 FOR UPDATE`,
		releaseID, projectID); err != nil {
		return Release{}, err
	}

	// 版本名变更（如果改了）同样要占位：重命名也受项目内唯一约束。
	err = tx.QueryRow(ctx, `
    UPDATE releases
    SET release_name = $2, release_name_key = $3, intended_use = $4,
        limitations = $5, provenance = $6, updated_at = NOW()
    WHERE id = $1
    RETURNING id`,
		releaseID, strings.TrimSpace(input.ReleaseName),
		model.NormalizeReleaseNameKey(input.ReleaseName),
		strings.TrimSpace(input.IntendedUse), limitations, provenance).Scan(&releaseID)
	if err != nil {
		if IsUniqueViolation(err) {
			return Release{}, ErrReleaseNameTaken
		}
		return Release{}, err
	}

	nextRevision := current.CandidateRevision + 1
	input.ProjectID = projectID
	if _, err := insertCandidateTx(ctx, tx, releaseID, input, nextRevision); err != nil {
		return Release{}, err
	}
	gate, err := rebuildReleaseItemsTx(ctx, tx, releaseID, projectID, nextRevision, input.SampleVersionIDs, input)
	if err != nil {
		return Release{}, err
	}
	if err := persistGateTx(ctx, tx, releaseID, nextRevision, gate, input.CreatedBy, len(input.SampleVersionIDs)); err != nil {
		return Release{}, err
	}
	status := model.ReleaseStatusCandidate
	if !gate.Passed {
		status = model.ReleaseStatusBlocked
	}
	if _, err := tx.Exec(ctx, `
    UPDATE releases SET status = $2, quality_snapshot = $3, updated_at = NOW() WHERE id = $1`,
		releaseID, status, mustJSONBytes(map[string]any{
			"inspected": gate.Inspected, "acceptanceRate": gate.AcceptanceRate,
			"acceptanceRateDisplay": gate.AcceptanceRateDisplay,
		})); err != nil {
		return Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return s.GetRelease(ctx, projectID, releaseID)
}

// FreezeRelease 冻结并进入 building（契约 §2.9）。
//
// 冻结事务做三件事，缺一不可：
//
//  1. **锁候选与 release 行**，使「校验 revision」与「写清单」之间没有窗口；
//  2. **重新判定门槛**（用**当前**判断/证据），并检测「确认之后是否变过」；
//  3. 写 release_items（不可变清单）+ release_gates（确认记录）+
//     release=building + outbox（交给 T21 的发布作业）。
//
// 为什么必须重新判定而不是信任创建时的 blockers：
// blockers 是**创建那一刻**的检查结果，而发布可能发生在几分钟甚至几天后。
// 期间有人隔离了一条内容、或发现了新风险 —— 那时用旧结果放行，
// 就会发布出一份「确认时没问题、现在有问题」的文件。
func (s *ReleaseStore) FreezeRelease(ctx context.Context, projectID, releaseID, actorID int64) (Release, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var candidateRevision int64
	var mappingVersionID *int64
	if err := tx.QueryRow(ctx, `
    SELECT r.status, COALESCE(c.revision, 1), c.mapping_version_id
    FROM releases r
    LEFT JOIN LATERAL (
      SELECT revision, mapping_version_id FROM release_candidates
      WHERE release_id = r.id ORDER BY revision DESC LIMIT 1
    ) c ON TRUE
    WHERE r.id = $1 AND r.project_id = $2
    FOR UPDATE OF r`, releaseID, projectID,
	).Scan(&status, &candidateRevision, &mappingVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Release{}, ErrReleaseNotFound
		}
		return Release{}, err
	}

	// 幂等：已发布直接返回原对象（相同命令返回同一个 release，§2.9）。
	if status == model.ReleaseStatusPublished {
		if err := tx.Commit(ctx); err != nil {
			return Release{}, err
		}
		return s.GetRelease(ctx, projectID, releaseID)
	}
	// building 期间重复命令同样返回同一对象（双击发布不产生第二个发布作业）。
	if status == model.ReleaseStatusBuilding {
		if err := tx.Commit(ctx); err != nil {
			return Release{}, err
		}
		return s.GetRelease(ctx, projectID, releaseID)
	}

	// 用**当前**事实重新判定门槛。
	var intendedUse, format string
	if err := tx.QueryRow(ctx, `
    SELECT intended_use FROM releases WHERE id = $1`, releaseID).Scan(&intendedUse); err != nil {
		return Release{}, err
	}
	if err := tx.QueryRow(ctx, `
    SELECT format FROM release_candidates WHERE release_id = $1 ORDER BY revision DESC LIMIT 1`,
		releaseID).Scan(&format); err != nil {
		return Release{}, err
	}
	gate, err := evaluateGateForReleaseTx(ctx, tx, projectID, releaseID, candidateRevision, releaseGateFields{
		IntendedUse: intendedUse, Format: format, MappingVersionID: mappingVersionID,
	})
	if err != nil {
		return Release{}, err
	}
	if !gate.Passed {
		// 门槛未过：标记 blocked 并返回错误，**不**写入不可变清单。
		// 写入清单会让「被挡住的候选」拥有一份看起来可发布的文件范围。
		blockersJSON, _ := json.Marshal(gate.Blockers)
		if _, err := tx.Exec(ctx, `
      UPDATE releases SET status = 'blocked', updated_at = NOW() WHERE id = $1`,
			releaseID); err != nil {
			return Release{}, err
		}
		if _, err := tx.Exec(ctx, `
      UPDATE release_candidates SET blockers = $2, updated_at = NOW()
      WHERE release_id = $1 AND revision = $3`, releaseID, blockersJSON, candidateRevision); err != nil {
			return Release{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Release{}, err
		}
		return Release{}, ErrReleaseGateBlocked
	}

	// 校验每条冻结项的 revision 与**当前**一致：这是抗并发的核心检测。
	// 与 gate 的检查重叠是有意的 —— gate 用于报告，这里用于拒绝写入。
	var mismatched int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FROM release_items ri
    JOIN review_projections rp ON rp.sample_version_id = ri.sample_version_id
    WHERE ri.release_id = $1 AND ri.revision = $2
      AND (rp.aggregate_review_revision <> ri.aggregate_review_revision
           OR rp.evidence_revision <> ri.evidence_revision)`,
		releaseID, candidateRevision).Scan(&mismatched); err != nil {
		return Release{}, err
	}
	if mismatched > 0 {
		if err := tx.Commit(ctx); err != nil {
			return Release{}, err
		}
		return Release{}, ErrReleaseRevisionStale
	}

	if _, err := tx.Exec(ctx, `
    UPDATE releases SET status = 'building', updated_at = NOW() WHERE id = $1`, releaseID); err != nil {
		return Release{}, err
	}
	if _, err := tx.Exec(ctx, `
    UPDATE release_candidates SET frozen_at = NOW(), updated_at = NOW()
    WHERE release_id = $1 AND revision = $2`, releaseID, candidateRevision); err != nil {
		return Release{}, err
	}

	// outbox：**与状态变更同事务**，因此「已进入 building 但没有发布作业」
	// 这种状态在结构上不可能出现（T21 的发布作业由 dispatcher 派发）。
	if err := appendOutboxTx(ctx, tx, fmt.Sprintf("release:%d:publish", releaseID), "studio.releases",
		map[string]any{"releaseId": releaseID, "projectId": projectID, "revision": candidateRevision}); err != nil {
		return Release{}, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "release_freeze", Resource: "release",
		ResourceID: fmt.Sprint(releaseID), ProjectID: projectID,
		Reason: fmt.Sprintf("revision=%d inspected=%d", candidateRevision, gate.Inspected),
	}); err != nil {
		return Release{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return s.GetRelease(ctx, projectID, releaseID)
}

// GetRelease 读取发布（含当前候选与 blocker 快照）。
func (s *ReleaseStore) GetRelease(ctx context.Context, projectID, releaseID int64) (Release, error) {
	var release Release
	var blockersJSON, limitationsJSON, provenance, quality, coverage []byte
	var candidateRevision *int64
	err := s.db.QueryRow(ctx, `
    SELECT r.id, r.project_id, r.release_name, r.status, r.intended_use, r.limitations, r.provenance,
           r.quality_snapshot, r.coverage_summary, r.created_by, r.published_at, r.created_at, r.updated_at,
           c.id, c.revision, c.blockers, c.format, c.mapping_version_id,
           p.target_kind
    FROM releases r
    JOIN projects p ON p.id = r.project_id
    LEFT JOIN LATERAL (
      SELECT id, revision, blockers, format, mapping_version_id FROM release_candidates
      WHERE release_id = r.id ORDER BY revision DESC LIMIT 1
    ) c ON TRUE
    WHERE r.id = $1 AND r.project_id = $2`, releaseID, projectID,
	).Scan(&release.ID, &release.ProjectID, &release.ReleaseName, &release.Status,
		&release.IntendedUse, &limitationsJSON, &provenance, &quality, &coverage,
		&release.CreatedBy, &release.PublishedAt, &release.CreatedAt, &release.UpdatedAt,
		&release.CandidateID, &candidateRevision, &blockersJSON, &release.Format,
		&release.MappingVersionID, &release.TargetKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrReleaseNotFound
	}
	if err != nil {
		return Release{}, err
	}
	if candidateRevision != nil {
		release.CandidateRevision = *candidateRevision
	}
	release.Limitations = []string{}
	release.Blockers = []model.ReleaseBlocker{}
	_ = json.Unmarshal(limitationsJSON, &release.Limitations)
	_ = json.Unmarshal(blockersJSON, &release.Blockers)
	release.Provenance = json.RawMessage(provenance)
	release.QualitySnapshot = json.RawMessage(quality)
	release.CoverageSummary = json.RawMessage(coverage)
	return release, nil
}

// GetReleaseByID 按 ID 读取发布（**不经项目作用域**）。
//
// 存在的理由：发布作业由 outbox 派发，作业载荷里只有 releaseId 而没有
// 项目 ID（契约 §5 的事件载荷「只含对象 ID 与版本」）。worker 因此需要
// 一条按 ID 读取的路径 —— 它是系统级动作，不经过用户授权。
// **不要**在 API 层用它：那会绕过项目作用域校验（API 一律用 GetRelease）。
func (s *ReleaseStore) GetReleaseByID(ctx context.Context, releaseID int64) (Release, error) {
	var projectID int64
	if err := s.db.QueryRow(ctx, `
    SELECT project_id FROM releases WHERE id = $1`, releaseID).Scan(&projectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Release{}, ErrReleaseNotFound
		}
		return Release{}, err
	}
	return s.GetRelease(ctx, projectID, releaseID)
}

// ListReleases 列出项目的发布（候选与已发布都返回，界面自行区分）。
func (s *ReleaseStore) ListReleases(ctx context.Context, projectID int64, limit int) ([]Release, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
    SELECT id FROM releases WHERE project_id = $1
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
	items := make([]Release, 0, len(ids))
	for _, id := range ids {
		release, err := s.GetRelease(ctx, projectID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, release)
	}
	return items, nil
}

// ReleaseItem 是发布清单项。
type ReleaseItem struct {
	ID                      int64  `json:"id"`
	ReleaseID               int64  `json:"releaseId"`
	Revision                int64  `json:"revision"`
	SampleID                int64  `json:"sampleId"`
	SampleVersionID         int64  `json:"sampleVersionId"`
	ContentHash             string `json:"contentHash"`
	StandardVersionID       *int64 `json:"standardVersionId,omitempty"`
	StandardContentHash     string `json:"standardContentHash"`
	BlueprintVersionID      *int64 `json:"blueprintVersionId,omitempty"`
	BlueprintContentHash    string `json:"blueprintContentHash"`
	AggregateReviewRevision int64  `json:"aggregateReviewRevision"`
	EvidenceRevision        int64  `json:"evidenceRevision"`
	EffectiveAction         string `json:"effectiveAction"`
	ExcludedReason          string `json:"excludedReason"`
}

// ListReleaseItems 读取某个修订的清单。
func (s *ReleaseStore) ListReleaseItems(ctx context.Context, releaseID, revision int64, limit int) ([]ReleaseItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, release_id, revision, sample_id, sample_version_id, content_hash,
           standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
           aggregate_review_revision, evidence_revision, effective_action, excluded_reason
    FROM release_items
    WHERE release_id = $1 AND revision = $2
    ORDER BY id LIMIT $3`, releaseID, revision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReleaseItem{}
	for rows.Next() {
		var item ReleaseItem
		if err := rows.Scan(&item.ID, &item.ReleaseID, &item.Revision, &item.SampleID,
			&item.SampleVersionID, &item.ContentHash, &item.StandardVersionID,
			&item.StandardContentHash, &item.BlueprintVersionID, &item.BlueprintContentHash,
			&item.AggregateReviewRevision, &item.EvidenceRevision, &item.EffectiveAction,
			&item.ExcludedReason); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// validateReleaseRangeTx 在写入前校验项目与范围。
//
// 三件事一起查清楚：项目存在、范围非空、范围内每个版本都属于该项目。
// 分开查会让「项目不存在」先以 FK 违约报出来（不可展示的内部错误），
// 而用户需要知道的是「这个项目/这些内容没有被找到」。
func validateReleaseRangeTx(ctx context.Context, tx pgx.Tx, projectID int64, versionIDs []int64) error {
	var exists bool
	if err := tx.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1)`, projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return &apiStoreError{Message: "项目不存在或不属于当前工作区，无法创建发布候选"}
	}

	var validCount int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FROM sample_versions
    WHERE project_id = $1 AND id = ANY($2::bigint[])`, projectID, versionIDs).Scan(&validCount); err != nil {
		return err
	}
	if validCount != len(versionIDs) {
		return &apiStoreError{Message: fmt.Sprintf(
			"发布范围里有 %d 个内容版本不存在或不属于该项目，已拒绝创建（避免范围静默变小）",
			len(versionIDs)-validCount)}
	}
	return nil
}

// releaseGateFields 是门槛判定需要的候选字段。
type releaseGateFields struct {
	IntendedUse      string
	Format           string
	MappingVersionID *int64
}

// evaluateGateForReleaseTx 用**当前事实**判定门槛。
func evaluateGateForReleaseTx(ctx context.Context, tx pgx.Tx, projectID, releaseID, revision int64, fields releaseGateFields) (model.ReleaseGateResult, error) {
	// 当前清单 + 当前投影 revision。
	rows, err := tx.Query(ctx, `
    SELECT ri.sample_id, ri.sample_version_id, ri.aggregate_review_revision, ri.evidence_revision,
           COALESCE(rp.effective_action, 'pending'),
           COALESCE(rp.aggregate_review_revision, 0), COALESCE(rp.evidence_revision, 0),
           ri.excluded_reason
    FROM release_items ri
    LEFT JOIN review_projections rp ON rp.sample_version_id = ri.sample_version_id
    WHERE ri.release_id = $1 AND ri.revision = $2
    ORDER BY ri.id`, releaseID, revision)
	if err != nil {
		return model.ReleaseGateResult{}, err
	}
	items := []model.ReleaseItemFacts{}
	for rows.Next() {
		var item model.ReleaseItemFacts
		var excludedReason string
		if err := rows.Scan(&item.SampleID, &item.SampleVersionID,
			&item.AggregateReviewRevision, &item.EvidenceRevision, &item.EffectiveAction,
			&item.CurrentAggregateReviewRevision, &item.CurrentEvidenceRevision,
			&excludedReason); err != nil {
			rows.Close()
			return model.ReleaseGateResult{}, err
		}
		// 被排除的项：清单里以 effective_action 记录**当时**的处置，
		// 而排除原因非空即表示它是被有意排除的。
		item.Excluded = strings.TrimSpace(excludedReason) != ""
		item.ExcludedReason = excludedReason
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return model.ReleaseGateResult{}, err
	}

	// 项目质量目标。
	var target float64
	if err := tx.QueryRow(ctx, `SELECT acceptance_rate_target FROM projects WHERE id = $1`, projectID).
		Scan(&target); err != nil {
		return model.ReleaseGateResult{}, err
	}

	// 已接纳数：**冻结范围内**的接纳（含被排除但曾接纳的项）——
	// 用「最终纳入的条数」当分子会让排除变成提高接纳率的手段。
	accepted := 0
	for _, item := range items {
		if item.EffectiveAction == model.EffectiveAccepted {
			accepted++
		}
	}

	mappingID := int64(0)
	if fields.MappingVersionID != nil {
		mappingID = *fields.MappingVersionID
	}
	var releaseName string
	if err := tx.QueryRow(ctx, `SELECT release_name FROM releases WHERE id = $1`, releaseID).
		Scan(&releaseName); err != nil {
		return model.ReleaseGateResult{}, err
	}

	return model.EvaluateReleaseGate(model.ReleaseGateInput{
		ReleaseName: releaseName, MappingVersionID: mappingID, Format: fields.Format,
		IntendedUse: fields.IntendedUse, Items: items,
		AcceptanceRateTarget: target, AcceptedCount: accepted,
	}), nil
}

// insertCandidateTx 写入一条候选修订（并刷新 release 上的候选引用）。
func insertCandidateTx(ctx context.Context, tx pgx.Tx, releaseID int64, input CreateReleaseCandidateInput, revision int64) (int64, error) {
	var candidateID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO release_candidates
      (release_id, project_id, revision, mapping_version_id, format, created_by)
    VALUES ($1, $2, $3, $4, $5, $6)
    RETURNING id`,
		releaseID, input.ProjectID, revision, nullableVersionID(input.MappingVersionID),
		input.Format, input.CreatedBy).Scan(&candidateID); err != nil {
		return 0, err
	}
	return candidateID, nil
}

// rebuildReleaseItemsTx 重建清单（先删该修订的旧行再插）。
//
// 快照来自 **sample_versions 自身的来源字段**，而不是「当前项目采用版本」——
// 后者会让「发布时改一次蓝图」把历史内容的来源改写成新版本
// （T20 验收项明确禁止「以当前项目版本冒充样本来源版本」）。
func rebuildReleaseItemsTx(ctx context.Context, tx pgx.Tx, releaseID, projectID, revision int64, versionIDs []int64, input CreateReleaseCandidateInput) (model.ReleaseGateResult, error) {
	if _, err := tx.Exec(ctx, `
    DELETE FROM release_items WHERE release_id = $1 AND revision = $2`, releaseID, revision); err != nil {
		return model.ReleaseGateResult{}, err
	}

	rows, err := tx.Query(ctx, `
    SELECT sv.id, sv.sample_id, sv.content_hash,
           sv.standard_version_id, sv.standard_content_hash,
           sv.blueprint_version_id, sv.blueprint_content_hash,
           COALESCE(rp.effective_action, 'pending'),
           COALESCE(rp.aggregate_review_revision, 0), COALESCE(rp.evidence_revision, 0)
    FROM sample_versions sv
    LEFT JOIN review_projections rp ON rp.sample_version_id = sv.id
    WHERE sv.project_id = $1 AND sv.id = ANY($2::bigint[])
    ORDER BY sv.id`, projectID, versionIDs)
	if err != nil {
		return model.ReleaseGateResult{}, err
	}

	type row struct {
		versionID     int64
		sampleID      int64
		contentHash   string
		standardID    *int64
		standardHash  string
		blueprintID   *int64
		blueprintHash string
		action        string
		aggregate     int64
		evidence      int64
	}
	loaded := []row{}
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.versionID, &item.sampleID, &item.contentHash,
			&item.standardID, &item.standardHash, &item.blueprintID, &item.blueprintHash,
			&item.action, &item.aggregate, &item.evidence); err != nil {
			rows.Close()
			return model.ReleaseGateResult{}, err
		}
		loaded = append(loaded, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return model.ReleaseGateResult{}, err
	}
	// 范围不完整必须报错：静默少几条会让「选了 100 条、发布 98 条」
	// 而用户以为范围就是选的那些（与 T17 的选择快照同一原则）。
	if len(loaded) != len(versionIDs) {
		return model.ReleaseGateResult{}, &apiStoreError{Message: fmt.Sprintf(
			"发布范围里有 %d 个内容版本不存在或不属于本项目，已拒绝创建（避免范围静默变小）",
			len(versionIDs)-len(loaded))}
	}

	gateItems := make([]model.ReleaseItemFacts, 0, len(loaded))
	for _, item := range loaded {
		if _, err := tx.Exec(ctx, `
      INSERT INTO release_items
        (release_id, revision, sample_id, sample_version_id, content_hash,
         standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
         aggregate_review_revision, evidence_revision, effective_action)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			releaseID, revision, item.sampleID, item.versionID, item.contentHash,
			item.standardID, item.standardHash, item.blueprintID, item.blueprintHash,
			item.aggregate, item.evidence, item.action); err != nil {
			return model.ReleaseGateResult{}, err
		}
		gateItems = append(gateItems, model.ReleaseItemFacts{
			SampleID: item.sampleID, SampleVersionID: item.versionID,
			EffectiveAction:         item.action,
			AggregateReviewRevision: item.aggregate, EvidenceRevision: item.evidence,
			// 创建时刚写入，因此「当前」与「冻结」相同。
			CurrentAggregateReviewRevision: item.aggregate,
			CurrentEvidenceRevision:        item.evidence,
		})
	}

	accepted := 0
	for _, item := range gateItems {
		if item.EffectiveAction == model.EffectiveAccepted {
			accepted++
		}
	}
	var target float64
	if err := tx.QueryRow(ctx, `SELECT acceptance_rate_target FROM projects WHERE id = $1`, projectID).
		Scan(&target); err != nil {
		return model.ReleaseGateResult{}, err
	}
	var releaseName string
	if err := tx.QueryRow(ctx, `SELECT release_name FROM releases WHERE id = $1`, releaseID).
		Scan(&releaseName); err != nil {
		return model.ReleaseGateResult{}, err
	}
	return model.EvaluateReleaseGate(model.ReleaseGateInput{
		ReleaseName: releaseName, MappingVersionID: input.MappingVersionID, Format: input.Format,
		IntendedUse: input.IntendedUse, Items: gateItems,
		AcceptanceRateTarget: target, AcceptedCount: accepted,
	}), nil
}

// persistGateTx 记录门槛检查结果（blocker 快照 + 确认记录）。
func persistGateTx(ctx context.Context, tx pgx.Tx, releaseID, revision int64, gate model.ReleaseGateResult, actorID *int64, originalCount int) error {
	blockersJSON, err := json.Marshal(gate.Blockers)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
    UPDATE release_candidates SET blockers = $3, updated_at = NOW()
    WHERE release_id = $1 AND revision = $2`, releaseID, revision, blockersJSON); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
    INSERT INTO release_gates
      (release_id, revision, blockers, passed, original_item_count, excluded_item_count, confirmed_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		releaseID, revision, blockersJSON, gate.Passed, originalCount,
		countExcludedItems(gate.Blockers), actorID); err != nil {
		return err
	}
	return nil
}

func countExcludedItems(blockers []model.ReleaseBlocker) int {
	count := 0
	for _, blocker := range blockers {
		if blocker.Code == model.BlockerExclusionNoReason {
			count++
		}
	}
	return count
}

func mustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}
