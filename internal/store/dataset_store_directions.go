package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
)

// 本文件为 L1「关键词 → n 领域 → m 方向」提供 store 能力与断点续跑游标。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.1 节。
//
// 为什么以「同包方法」形式补充而不是改既有文件：
// dataset_store.go 与 generation_run_store.go 属于 foundation 冻结文件，
// lane 只能新增文件。Go 允许同包内跨文件为已有类型定义方法，因此这里
// 为 DatasetStore / GenerationRunStore 补充方向相关的读写。

// DirectionStage 方向生成阶段的名称，用于 generation_runs.stage。
const DirectionStage = "directions"

// DirectionCursor 方向生成的断点续跑游标。
// 持久化在 generation_runs.cursor，每次完成一个领域即回写。
type DirectionCursor struct {
	// CompletedDomainIDs 已成功产出方向的领域 id（level=1 的 domain）。
	CompletedDomainIDs []int64 `json:"completedDomainIds"`
	// FailedDomainIDs 最近一次尝试中生成失败的领域 id。
	FailedDomainIDs []int64 `json:"failedDomainIds"`
	// DirectionCount 本次运行使用的每领域方向数（m），续跑时沿用原值。
	DirectionCount int `json:"directionCount"`
	// ProducedDirections 已实际写入的方向总数，用于进度展示与校验。
	ProducedDirections int `json:"producedDirections"`
}

// DecodeDirectionCursor 从 generation_runs.cursor 还原游标。
// cursor 经 JSONB 往返后数字会变成 float64，因此统一走 JSON 反序列化。
func DecodeDirectionCursor(cursor map[string]any) (DirectionCursor, error) {
	decoded := DirectionCursor{}
	if len(cursor) == 0 {
		return decoded, nil
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return DirectionCursor{}, fmt.Errorf("序列化方向生成游标失败: %w", err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return DirectionCursor{}, fmt.Errorf("解析方向生成游标失败: %w", err)
	}
	return decoded, nil
}

// EncodeDirectionCursor 把游标转成可直接交给 SaveCursor 的 map。
func EncodeDirectionCursor(cursor DirectionCursor) map[string]any {
	completed := cursor.CompletedDomainIDs
	if completed == nil {
		completed = []int64{}
	}
	failed := cursor.FailedDomainIDs
	if failed == nil {
		failed = []int64{}
	}
	return map[string]any{
		"completedDomainIds": completed,
		"failedDomainIds":    failed,
		"directionCount":     cursor.DirectionCount,
		"producedDirections": cursor.ProducedDirections,
	}
}

// PendingDomainIDs 返回尚未完成的领域 id，保持 domains 的输入顺序。
// 这是断点续跑的核心：续跑时只处理这里返回的领域，已完成的不会被重跑。
func PendingDomainIDs(domains []model.Domain, completedDomainIDs []int64) []int64 {
	done := make(map[int64]struct{}, len(completedDomainIDs))
	for _, id := range completedDomainIDs {
		done[id] = struct{}{}
	}
	pending := make([]int64, 0, len(domains))
	for _, domain := range domains {
		if _, exists := done[domain.ID]; exists {
			continue
		}
		pending = append(pending, domain.ID)
	}
	return pending
}

// ResumeRun 把已有运行记录重新置为 running 并累加 attempts，供断点续跑使用。
// error_summary 保留，作为上一次失败的证据；成功结束时由 FinishRun 清空。
func (s *GenerationRunStore) ResumeRun(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `
    UPDATE generation_runs
    SET status = 'running', attempts = attempts + 1, finished_at = NULL, updated_at = NOW()
    WHERE id = $1`, id)
	return err
}

// ListRootDomains 列出数据集的领域（level=1），即需求中的 n 个领域。
func (s *DatasetStore) ListRootDomains(ctx context.Context, datasetID int64) ([]model.Domain, error) {
	return s.ListDomainsByLevel(ctx, datasetID, 1)
}

// ListDirections 列出数据集的方向（level=2），即需求中的 n*m 方向层。
func (s *DatasetStore) ListDirections(ctx context.Context, datasetID int64) ([]model.Domain, error) {
	return s.ListDomainsByLevel(ctx, datasetID, 2)
}

// ListDirectionsByParent 列出某个领域下的全部方向，用于生成前构造去重集合。
func (s *DatasetStore) ListDirectionsByParent(ctx context.Context, datasetID, parentID int64) ([]model.Domain, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, dataset_id, name, canonical_name, level, parent_id, source, review_status, created_at, updated_at
    FROM domains WHERE dataset_id = $1 AND level = 2 AND parent_id = $2 ORDER BY id ASC`, datasetID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Domain{}
	for rows.Next() {
		var item model.Domain
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.Name, &item.Canonical, &item.Level, &item.ParentID,
			&item.Source, &item.ReviewStatus, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpsertDirections 批量写入方向（level=2）。
// 同一数据集 + 同一父领域下按 canonical_name 去重：已存在的跳过，只返回新插入的行。
// 返回的插入数即为本次真实新增的方向数，供游标累加。
func (s *DatasetStore) UpsertDirections(ctx context.Context, datasetID int64, directions []model.Domain) ([]model.Domain, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	inserted := make([]model.Domain, 0, len(directions))
	for _, direction := range directions {
		if direction.ParentID == nil {
			return nil, fmt.Errorf("方向 %q 缺少父领域，无法写入", direction.Name)
		}
		canonical := direction.Canonical
		if canonical == "" {
			canonical = CanonicalDomainName(direction.Name)
		}
		if canonical == "" {
			continue
		}

		var existingID int64
		err := tx.QueryRow(ctx, `
      SELECT id FROM domains
      WHERE dataset_id = $1 AND level = 2 AND parent_id = $2 AND canonical_name = $3
      LIMIT 1`, datasetID, *direction.ParentID, canonical).Scan(&existingID)
		switch {
		case err == nil:
			continue
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return nil, err
		}

		source := direction.Source
		if source == "" {
			source = "ai"
		}
		reviewStatus := direction.ReviewStatus
		if reviewStatus == "" {
			reviewStatus = "draft"
		}

		var item model.Domain
		if err := tx.QueryRow(ctx, `
      INSERT INTO domains (dataset_id, name, canonical_name, level, parent_id, source, review_status)
      VALUES ($1, $2, $3, $4, $5, $6, $7)
      RETURNING id, dataset_id, name, canonical_name, level, parent_id, source, review_status, created_at, updated_at`,
			datasetID, direction.Name, canonical, 2, *direction.ParentID, source, reviewStatus,
		).Scan(&item.ID, &item.DatasetID, &item.Name, &item.Canonical, &item.Level, &item.ParentID,
			&item.Source, &item.ReviewStatus, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		inserted = append(inserted, item)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return inserted, nil
}

// CanonicalDomainName 归一化领域/方向名称，用于去重比较。
func CanonicalDomainName(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	lowered = strings.ReplaceAll(lowered, "_", " ")
	return strings.Join(strings.Fields(lowered), " ")
}
