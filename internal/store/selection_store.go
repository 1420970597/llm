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

// 本文件实现大范围选择快照（Issue #160 T17）。
//
// 契约：docs/plans/atelier-implementation.md §3.2（`selection` 参数）、
// sql/migrations/0035_studio_selection_snapshots.sql 的文件头。
//
// 为什么必须服务端快照而不是 URL 里的 ID 列表：
//   * 数万 ID 会超出浏览器与代理的长度上限，表现为「点了发布什么都没发生」；
//   * URL 里的 ID 列表是**客户端可改的**，而发布范围必须是服务端认可的集合
//     （否则「我选中的」与「实际发布的」可以不一致）；
//   * 快照可被审计，URL 不行。

// MaxSelectionSnapshotItems 是单份快照的项数上限。
//
// 设上限的理由是「让超范围成为显式错误，而不是一次静默的半份快照」：
// 用户以为选了 12 万条、实际只冻结了 10 万条时，发布出去的版本会
// 与他以为的不一致 —— 而那是最难在事后发现的一类错误。
const MaxSelectionSnapshotItems = 100_000

// SampleVersionFilter 是「按筛选条件解析版本 ID」的条件。
type SampleVersionFilter struct {
	ProjectID    int64
	ReviewStatus string
	BatchID      *int64
	Search       string
}

// SelectionSnapshot 是一份选择快照。
type SelectionSnapshot struct {
	ID        int64           `json:"id"`
	ProjectID int64           `json:"projectId"`
	Purpose   string          `json:"purpose"`
	Filter    json.RawMessage `json:"filter"`
	ItemCount int             `json:"itemCount"`
	CreatedBy *int64          `json:"createdBy,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	ExpiresAt *time.Time      `json:"expiresAt,omitempty"`
}

// SelectionStore 提供选择快照读写。
type SelectionStore struct {
	db *pgxpool.Pool
}

// NewSelectionStore 构造选择快照 store。
func NewSelectionStore(db *pgxpool.Pool) *SelectionStore {
	return &SelectionStore{db: db}
}

// CreateSelectionSnapshotInput 是创建快照的请求。
type CreateSelectionSnapshotInput struct {
	ProjectID        int64
	Purpose          string
	CreatedBy        *int64
	SampleVersionIDs []int64
	Filter           json.RawMessage
	// ExpiresIn 为 0 时用默认有效期。
	ExpiresIn time.Duration
}

// DefaultSelectionTTL 是快照的默认有效期。
//
// 不过期会让「上周选的」在本周仍能直接用于发布，而「确认范围」正是要防这件事；
// 有效期太短又会让用户在填完发布表单后失效。取 2 小时：长于任何合理的表单填写，
// 短于「跨天误用」。
const DefaultSelectionTTL = 2 * time.Hour

// Create 冻结一份选择。
func (s *SelectionStore) Create(ctx context.Context, input CreateSelectionSnapshotInput) (SelectionSnapshot, error) {
	if len(input.SampleVersionIDs) == 0 {
		return SelectionSnapshot{}, &apiStoreError{Message: "选择范围不能为空"}
	}
	if len(input.SampleVersionIDs) > MaxSelectionSnapshotItems {
		return SelectionSnapshot{}, &apiStoreError{Message: fmt.Sprintf(
			"选择范围超过 %d 条上限，请缩小筛选条件后重试（避免冻结出一份你自己也没看清的范围）",
			MaxSelectionSnapshotItems)}
	}
	if input.Purpose == "" {
		input.Purpose = "release"
	}
	switch input.Purpose {
	case "release", "experiment", "export":
	default:
		return SelectionSnapshot{}, &apiStoreError{Message: "快照用途只能是 release、experiment 或 export"}
	}
	if input.ExpiresIn <= 0 {
		input.ExpiresIn = DefaultSelectionTTL
	}
	if len(input.Filter) == 0 {
		input.Filter = json.RawMessage(`{}`)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SelectionSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 只接受**属于本项目**的版本：跨项目引用必须被拒。
	// 用一次查询确认数量，避免「选了 10 条、实际只冻结 8 条」这种静默缩水。
	var validCount int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FROM sample_versions
    WHERE project_id = $1 AND id = ANY($2::bigint[])`,
		input.ProjectID, input.SampleVersionIDs).Scan(&validCount); err != nil {
		return SelectionSnapshot{}, err
	}
	if validCount != len(input.SampleVersionIDs) {
		return SelectionSnapshot{}, &apiStoreError{Message: fmt.Sprintf(
			"选择范围里有 %d 个样本版本不存在或不属于本项目，已拒绝创建（避免冻结范围静默变小）",
			len(input.SampleVersionIDs)-validCount)}
	}

	var snapshot SelectionSnapshot
	var expiresAt *time.Time
	if err := tx.QueryRow(ctx, `
    INSERT INTO sample_selection_snapshots
      (project_id, purpose, filter, item_count, created_by, expires_at)
    VALUES ($1, $2, $3, $4, $5, NOW() + make_interval(secs => $6))
    RETURNING id, project_id, purpose, filter, item_count, created_by, created_at, expires_at`,
		input.ProjectID, input.Purpose, input.Filter, len(input.SampleVersionIDs), input.CreatedBy,
		// make_interval(secs => $6) 而不是把秒数拼成 interval 字符串：
		// 拼字符串会让静态分析器无法区分「参数值」与「被拼进 SQL 的文本」，
		// 真正的注入点会因此淹没在噪声里（与本仓库既有的同类取舍一致）。
		input.ExpiresIn.Seconds(),
	).Scan(&snapshot.ID, &snapshot.ProjectID, &snapshot.Purpose, &snapshot.Filter,
		&snapshot.ItemCount, &snapshot.CreatedBy, &snapshot.CreatedAt, &expiresAt); err != nil {
		return SelectionSnapshot{}, err
	}
	snapshot.ExpiresAt = expiresAt

	for _, versionID := range input.SampleVersionIDs {
		if _, err := tx.Exec(ctx, `
      INSERT INTO sample_selection_items (snapshot_id, sample_version_id)
      VALUES ($1, $2) ON CONFLICT DO NOTHING`, snapshot.ID, versionID); err != nil {
			return SelectionSnapshot{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return SelectionSnapshot{}, err
	}
	return snapshot, nil
}

// Get 读取快照与其明细。
//
// 过期即视为不存在（返回 ErrSelectionSnapshotNotFound）：让「过期的选择」
// 在发布前就失败，而不是冻结出一份基于陈旧范围的版本。
func (s *SelectionStore) Get(ctx context.Context, projectID, snapshotID int64) (SelectionSnapshot, []int64, error) {
	var snapshot SelectionSnapshot
	var expiresAt *time.Time
	err := s.db.QueryRow(ctx, `
    SELECT id, project_id, purpose, filter, item_count, created_by, created_at, expires_at
    FROM sample_selection_snapshots
    WHERE id = $1 AND project_id = $2`, snapshotID, projectID,
	).Scan(&snapshot.ID, &snapshot.ProjectID, &snapshot.Purpose, &snapshot.Filter,
		&snapshot.ItemCount, &snapshot.CreatedBy, &snapshot.CreatedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SelectionSnapshot{}, nil, ErrSelectionSnapshotNotFound
	}
	if err != nil {
		return SelectionSnapshot{}, nil, err
	}
	snapshot.ExpiresAt = expiresAt
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return SelectionSnapshot{}, nil, ErrSelectionSnapshotNotFound
	}

	rows, err := s.db.Query(ctx, `
    SELECT sample_version_id FROM sample_selection_items
    WHERE snapshot_id = $1 ORDER BY sample_version_id`, snapshotID)
	if err != nil {
		return SelectionSnapshot{}, nil, err
	}
	defer rows.Close()

	items := []int64{}
	for rows.Next() {
		var versionID int64
		if err := rows.Scan(&versionID); err != nil {
			return SelectionSnapshot{}, nil, err
		}
		items = append(items, versionID)
	}
	// 明细行数与 item_count 不一致说明有人删过内容版本（RESTRICT 会挡住）
	// 或者写入不完整 —— 两种情况都必须暴露，不能让发布范围悄悄变小。
	if err := rows.Err(); err != nil {
		return SelectionSnapshot{}, nil, err
	}
	if len(items) != snapshot.ItemCount {
		return SelectionSnapshot{}, nil, &apiStoreError{Message: fmt.Sprintf(
			"选择范围的明细不完整（记录 %d 条、实际 %d 条），已拒绝使用；请重新确认发布范围",
			snapshot.ItemCount, len(items))}
	}
	return snapshot, items, nil
}

// ErrSelectionSnapshotNotFound 表示快照不存在、不属于本项目或已过期。
var ErrSelectionSnapshotNotFound = errors.New("选择范围不存在或已过期，请重新选择")

// ListSampleVersionIDsByFilter 按筛选条件在**服务端**解析出具体的版本 ID。
//
// 这一步是「禁止把数万 ID 塞 URL」的实现：前端只传条件，ID 由服务端解析。
// 上限命中时返回错误而不是截断 —— 截断会让用户以为范围是筛出来的全部。
func (s *BatchStore) ListSampleVersionIDsByFilter(ctx context.Context, filter SampleVersionFilter, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = MaxSelectionSnapshotItems
	}
	var batchID *int64
	if filter.BatchID != nil && *filter.BatchID > 0 {
		batchID = filter.BatchID
	}
	rows, err := s.db.Query(ctx, `
    SELECT sv.id
    FROM sample_versions sv
    JOIN samples sm ON sm.id = sv.sample_id
    LEFT JOIN review_projections rp ON rp.sample_version_id = sv.id
    WHERE sv.project_id = $1
      AND ($2 = '' OR COALESCE(rp.effective_action, 'pending') = $2)
      AND ($3::bigint IS NULL OR sv.batch_id = $3::bigint)
      AND ($4 = '' OR sm.title ILIKE '%' || $4 || '%' OR sm.sample_key ILIKE '%' || $4 || '%')
    ORDER BY sv.id
    LIMIT $5`,
		filter.ProjectID, strings.TrimSpace(filter.ReviewStatus), batchID,
		strings.TrimSpace(filter.Search), limit+1)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > limit {
		return nil, &apiStoreError{Message: fmt.Sprintf(
			"筛选出的样本版本超过 %d 条上限，请缩小条件后重试", limit)}
	}
	return ids, nil
}

// CountSelectionItems 统计快照的明细数（用于对账）。
func (s *SelectionStore) CountSelectionItems(ctx context.Context, snapshotID int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
    SELECT COUNT(*) FROM sample_selection_items WHERE snapshot_id = $1`, snapshotID).Scan(&count)
	return count, err
}

var _ = model.EffectivePending
