package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现旧数据导入台账的读写（Issue #160 T31）。
//
// 契约：sql/migrations/0034_studio_legacy_imports.sql 的文件头（表结构取舍）、
// #160 T31「`legacy_imports` 唯一来源键/游标/内容 hash，支持暂停、续跑、重复执行」。
//
// 三条与幂等直接相关的实现约定：
//
//  1. `BeginImport` 用 `INSERT ... ON CONFLICT DO NOTHING` + 回读：
//     重复执行命中的是**既有行**，因此「零重复导入」由唯一键保证，
//     而不是靠调用方先查一次（先查再写有 TOCTOU 窗口）。
//  2. `completed` 的台账**直接回放**（replay=true），调用方必须跳过全部副作用。
//     重跑一个已完成的导入不该产生任何新版本，也不该重新计费。
//  3. 计数分列存储：`imported_versions` 与 `skipped_existing` 是不同的事实。
//     合并成一个「处理了 N 条」会让重复执行看起来像又导了一遍。

// ErrLegacyImportNotFound 表示没有对应的导入台账。
var ErrLegacyImportNotFound = errors.New("导入台账不存在")

// LegacyImportCounts 是导入的分列计数。
type LegacyImportCounts struct {
	SourceItems      int `json:"sourceItems"`
	ImportedVersions int `json:"importedVersions"`
	SkippedExisting  int `json:"skippedExisting"`
	SkippedNoContent int `json:"skippedNoContent"`
	FailedItems      int `json:"failedItems"`
}

// LegacyImport 是一行导入台账。
type LegacyImport struct {
	ID              int64              `json:"id"`
	SourceKind      string             `json:"sourceKind"`
	SourceKey       string             `json:"sourceKey"`
	TargetProjectID *int64             `json:"targetProjectId,omitempty"`
	BatchID         *int64             `json:"batchId,omitempty"`
	Status          string             `json:"status"`
	Cursor          int64              `json:"cursor"`
	ContentHash     string             `json:"contentHash"`
	Counts          LegacyImportCounts `json:"counts"`
	BeforeSnapshot  json.RawMessage    `json:"beforeSnapshot"`
	AfterSnapshot   json.RawMessage    `json:"afterSnapshot"`
	Failures        json.RawMessage    `json:"failures"`
	ErrorMessage    string             `json:"errorMessage"`
	StartedAt       *time.Time         `json:"startedAt,omitempty"`
	FinishedAt      *time.Time         `json:"finishedAt,omitempty"`
	CreatedAt       time.Time          `json:"createdAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
}

// LegacyImportStore 提供导入台账的读写。
type LegacyImportStore struct {
	db *pgxpool.Pool
}

// NewLegacyImportStore 构造导入台账 store。
func NewLegacyImportStore(db *pgxpool.Pool) *LegacyImportStore {
	return &LegacyImportStore{db: db}
}

const legacyImportColumns = `id, source_kind, source_key, target_project_id, batch_id, status,
  cursor, content_hash, source_items, imported_versions, skipped_existing,
  skipped_no_content, failed_items, before_snapshot, after_snapshot, failures,
  error_message, started_at, finished_at, created_at, updated_at`

// BeginImport 开始（或命中既有）一次导入，返回 (台账, 是否已完成的回放)。
//
// replay=true 表示这次来源已经**完成**过：调用方必须跳过全部副作用。
// 已存在但未完成（running/paused/failed）时 replay=false，调用方从 cursor 续跑。
func (s *LegacyImportStore) BeginImport(ctx context.Context, sourceKind, sourceKey string) (LegacyImport, bool, error) {
	if sourceKind == "" || sourceKey == "" {
		return LegacyImport{}, false, &apiStoreError{Message: "导入台账需要来源类型与来源键"}
	}
	row := s.db.QueryRow(ctx, `
    INSERT INTO legacy_imports (source_kind, source_key, status, started_at)
    VALUES ($1, $2, 'running', NOW())
    ON CONFLICT (source_kind, source_key) DO NOTHING
    RETURNING `+legacyImportColumns, sourceKind, sourceKey)
	importRow, err := scanLegacyImport(row)
	if err == nil {
		return importRow, false, nil
	}
	if !errors.Is(err, ErrLegacyImportNotFound) {
		return LegacyImport{}, false, err
	}
	// 冲突命中：回读既有行（这就是幂等的落点）。
	existing, err := s.GetImport(ctx, sourceKind, sourceKey)
	if err != nil {
		return LegacyImport{}, false, err
	}
	return existing, existing.Status == "completed", nil
}

// GetImport 按来源键读取台账。
func (s *LegacyImportStore) GetImport(ctx context.Context, sourceKind, sourceKey string) (LegacyImport, error) {
	row := s.db.QueryRow(ctx, `SELECT `+legacyImportColumns+`
    FROM legacy_imports WHERE source_kind = $1 AND source_key = $2`, sourceKind, sourceKey)
	return scanLegacyImport(row)
}

// SetImportTarget 记录目标项目与批次。
func (s *LegacyImportStore) SetImportTarget(ctx context.Context, id int64, projectID, batchID *int64) error {
	_, err := s.db.Exec(ctx, `
    UPDATE legacy_imports
    SET target_project_id = COALESCE($2, target_project_id),
        batch_id = COALESCE($3, batch_id),
        updated_at = NOW()
    WHERE id = $1`, id, projectID, batchID)
	return err
}

// SaveProgress 保存游标与分列计数（续跑用）。
func (s *LegacyImportStore) SaveProgress(ctx context.Context, id int64, cursor int64, counts LegacyImportCounts) error {
	_, err := s.db.Exec(ctx, `
    UPDATE legacy_imports
    SET cursor = $2, source_items = $3, imported_versions = $4, skipped_existing = $5,
        skipped_no_content = $6, failed_items = $7, updated_at = NOW()
    WHERE id = $1`,
		id, cursor, counts.SourceItems, counts.ImportedVersions, counts.SkippedExisting,
		counts.SkippedNoContent, counts.FailedItems)
	return err
}

// FinishImport 写终态、对账快照与失败明细。
func (s *LegacyImportStore) FinishImport(ctx context.Context, id int64, status string, cursor int64,
	counts LegacyImportCounts, contentHash string, before, after, failures json.RawMessage, errorMessage string) error {
	if status != "completed" && status != "failed" && status != "paused" {
		return &apiStoreError{Message: "导入终态只能是 completed/failed/paused"}
	}
	_, err := s.db.Exec(ctx, `
    UPDATE legacy_imports
    SET status = $2, cursor = $3,
        source_items = $4, imported_versions = $5, skipped_existing = $6,
        skipped_no_content = $7, failed_items = $8,
        content_hash = $9, before_snapshot = $10, after_snapshot = $11, failures = $12,
        error_message = $13,
        finished_at = CASE WHEN $2 = 'paused' THEN NULL ELSE NOW() END,
        updated_at = NOW()
    WHERE id = $1`,
		id, status, cursor, counts.SourceItems, counts.ImportedVersions, counts.SkippedExisting,
		counts.SkippedNoContent, counts.FailedItems, contentHash,
		nullableJSON(before), nullableJSON(after), nullableJSON(failures), errorMessage)
	return err
}

// MarkImportedBatchCompleted 把导入快照批次标为已完成。
//
// 为什么需要它（而不是让批次停在 queued）：导入的快照**本来就是完整的**
// （内容已经在 sample_versions 里），停在 queued 会在界面上显示成「排队中」，
// 用户会等一个永远不会发生的生成。
//
// 为什么这不是「伪造一次运行」：`legacy_imports` 明确记录这批内容来自导入，
// `generation_config` 为空，且没有任何 batch_items 被写入 —— 日志与台账
// 都能区分「导入的快照」与「跑出来的批次」。
//
// 只对**没有任何已提交单元**的批次生效：若批次已经有 batch_items（说明它
// 真的在跑或跑过），改它的状态会掩盖真实进度。
func (s *LegacyImportStore) MarkImportedBatchCompleted(ctx context.Context, batchID int64) error {
	tag, err := s.db.Exec(ctx, `
    UPDATE batches
    SET status = 'completed', control_state = 'run',
        completed_units = planned_units, updated_at = NOW()
    WHERE id = $1
      AND NOT EXISTS (SELECT 1 FROM batch_items WHERE batch_id = $1)`, batchID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("批次 %d 已有单元或不存在，不能标为导入快照完成", batchID)
	}
	return nil
}

// FindProjectByLegacyDataset 按旧 dataset ID 反查已映射的项目（旧路由兼容用）。
//
// 返回 (0, nil) 表示尚无映射 —— 调用方据此给出「历史资产尚未迁移」的可读说明，
// 而不是假装跳到一个不存在的项目。
func (s *LegacyImportStore) FindProjectByLegacyDataset(ctx context.Context, datasetID int64) (int64, error) {
	var projectID int64
	err := s.db.QueryRow(ctx, `
    SELECT id FROM projects WHERE legacy_dataset_id = $1 ORDER BY id LIMIT 1`, datasetID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return projectID, nil
}

// SampleVersionContentHashExists 判断某个 sample_key 下是否已有同内容 hash 的版本。
//
// 这是「重复执行零重复导入」的第二个判据（第一个是台账的 completed 回放）：
// 台账可能因为中途失败而没有被标记完成，那时必须按**内容**判断，
// 否则重跑会给每条内容再追加一个新版本（内容相同但 version+1），
// 而「同一份历史内容被导入两次」在下游看起来像是两条不同的训练数据。
func (s *LegacyImportStore) SampleVersionContentHashExists(ctx context.Context, projectID int64, sampleKey, contentHash string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
    SELECT EXISTS(
      SELECT 1 FROM sample_versions sv
      JOIN samples s ON s.id = sv.sample_id
      WHERE s.project_id = $1 AND s.sample_key = $2 AND sv.content_hash = $3)`,
		projectID, sampleKey, contentHash).Scan(&exists)
	return exists, err
}

// CountProjectSamples 统计项目内的样本与内容版本数量（对账用）。
func (s *LegacyImportStore) CountProjectSamples(ctx context.Context, projectID int64) (samples, versions int64, err error) {
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM samples WHERE project_id = $1`, projectID).Scan(&samples); err != nil {
		return 0, 0, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM sample_versions WHERE project_id = $1`, projectID).Scan(&versions); err != nil {
		return 0, 0, err
	}
	return samples, versions, nil
}

// ContentHashDigest 计算项目内全部内容版本的联合摘要（对账用）。
//
// 排序再聚合：数据库返回顺序未定义，不排序会让「同一份内容」得到不同摘要，
// 于是对账结论不可复现（与 T21 的 manifest 规范化同一原则）。
func (s *LegacyImportStore) ContentHashDigest(ctx context.Context, projectID int64) (string, error) {
	var digest string
	err := s.db.QueryRow(ctx, `
    SELECT COALESCE(string_agg(content_hash, ',' ORDER BY sample_id, version), '')
    FROM sample_versions WHERE project_id = $1`, projectID).Scan(&digest)
	if err != nil {
		return "", err
	}
	if digest == "" {
		return "", nil
	}
	// 用 model.ContentHash（与样本版本写入时同一实现）：两处用不同 hash 会让
	// 对账结论与真实内容无关。
	hashed, err := model.ContentHash(digest)
	if err != nil {
		return "", err
	}
	return hashed, nil
}

func scanLegacyImport(row pgx.Row) (LegacyImport, error) {
	var importRow LegacyImport
	var before, after, failures []byte
	err := row.Scan(&importRow.ID, &importRow.SourceKind, &importRow.SourceKey,
		&importRow.TargetProjectID, &importRow.BatchID, &importRow.Status,
		&importRow.Cursor, &importRow.ContentHash,
		&importRow.Counts.SourceItems, &importRow.Counts.ImportedVersions,
		&importRow.Counts.SkippedExisting, &importRow.Counts.SkippedNoContent,
		&importRow.Counts.FailedItems,
		&before, &after, &failures,
		&importRow.ErrorMessage, &importRow.StartedAt, &importRow.FinishedAt,
		&importRow.CreatedAt, &importRow.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LegacyImport{}, ErrLegacyImportNotFound
	}
	if err != nil {
		return LegacyImport{}, err
	}
	importRow.BeforeSnapshot = json.RawMessage(before)
	importRow.AfterSnapshot = json.RawMessage(after)
	importRow.Failures = json.RawMessage(failures)
	return importRow, nil
}

// nullableJSON 把空 raw 归一成 '{}'（列是 NOT NULL）。
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
