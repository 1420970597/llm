package store

import (
	"context"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// 本文件是样本的**分页查询**（Issue #160 T08 的读模型部分）。
//
// 为什么与 batch_store.go 分开：T05 的文件已经承担了「批次与样本的写入不变量」，
// 那是并发与事务密集的部分；读模型的分页条件会随 T17 的筛选需求持续增加，
// 混在一起会让「改动读模型」看起来像「改动写入语义」，从而抬高审查成本。

// SampleListQuery 是样本列表条件（契约 §3 的 `GET P/samples`）。
//
// 关于尚未支持的筛选项：契约列出了 `status` 与 `risk`，但两者的判据来自
// 人工判断与证据（T16/T17 才建表）。这里**不接受**它们，而不是接受后忽略 ——
// 「传了筛选但没有生效」会让用户以为自己看到的是筛过的结果，
// 而那种错误在界面上完全不可见（列表看起来很正常）。
type SampleListQuery struct {
	ProjectID int64
	// BatchID 按「来源批次或最近一次产出批次」过滤。
	BatchID int64
	// Search 在样本标题与样本键上做包含匹配。
	Search string
	// TargetKind 过滤 sft/grpo（两类项目的样本结构不同，混列没有意义）。
	TargetKind string
	Cursor     time.Time
	CursorID   int64
	Limit      int
}

// ListSamples 按「同项目内最近」列出样本；keyset 游标（契约 §1.5）。
//
// 排序键是 (created_at, id)：created_at 单独不唯一（同一批次的样本往往
// 在同一毫秒内创建），而游标比较必须全序，否则翻页会重复或漏行。
func (s *BatchStore) ListSamples(ctx context.Context, query SampleListQuery) ([]model.Sample, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var cursorTime *time.Time
	if !query.Cursor.IsZero() {
		truncated := query.Cursor
		cursorTime = &truncated
	}

	rows, err := s.db.Query(ctx, `
    SELECT id, project_id, sample_key, target_kind, title, origin_batch_id,
           latest_version, created_at, updated_at
    FROM samples
    WHERE project_id = $1
      AND ($2::bigint = 0 OR origin_batch_id = $2::bigint)
      AND ($3 = '' OR target_kind = $3)
      AND ($4 = '' OR title ILIKE '%' || $4 || '%' OR sample_key ILIKE '%' || $4 || '%')
      AND ($5::timestamptz IS NULL OR (created_at, id) < ($5::timestamptz, $6::bigint))
    ORDER BY created_at DESC, id DESC
    LIMIT $7`,
		query.ProjectID, query.BatchID, strings.TrimSpace(query.TargetKind),
		strings.TrimSpace(query.Search), cursorTime, query.CursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Sample{}
	for rows.Next() {
		var sample model.Sample
		if err := rows.Scan(&sample.ID, &sample.ProjectID, &sample.SampleKey, &sample.TargetKind,
			&sample.Title, &sample.OriginBatchID, &sample.LatestVersion,
			&sample.CreatedAt, &sample.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, sample)
	}
	return items, rows.Err()
}

// CountSamplesByProject 统计项目的样本数（概览与列表头用）。
func (s *BatchStore) CountSamplesByProject(ctx context.Context, projectID int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM samples WHERE project_id = $1`, projectID).Scan(&count)
	return count, err
}

// CountSampleVersionsByProject 统计项目的样本版本数。
//
// 与样本数分开：契约 §3.1 的「实际产出」按**样本版本**计（同一题重生成
// 会产生新版本），而「样本数」是题目身份数。两个数字混用会让
// 「生成了几条」在重生成后前后矛盾。
func (s *BatchStore) CountSampleVersionsByProject(ctx context.Context, projectID int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM sample_versions WHERE project_id = $1`, projectID).Scan(&count)
	return count, err
}

// GetSampleVersionByID 按**样本版本行 ID** 读取内容（Issue #160 T14 的执行侧需要）。
//
// 为什么需要它：`experiment_items` 冻结的是 `sample_version_id`（行 ID），
// 而原有的 `GetSampleVersion` 按 (样本, 版本号) 读取。实验执行必须按**冻结的
// 那个 ID** 取内容 —— 按版本号取会在样本当前指针已推进时读到另一份内容，
// 从而让「入队后改样本当前指针不改变实验」失效。
//
// `generator_config` 用 COALESCE：它是可空的（人工导入/迁移来的版本没有它），
// 而扫描 NULL 到 json.RawMessage 会直接报错 —— 那会让调用方把每一项都标成
// error，而真实原因只是「这一版没有生成配置」。
func (s *BatchStore) GetSampleVersionByID(ctx context.Context, projectID, versionID int64) (model.SampleVersion, error) {
	var item model.SampleVersion
	err := s.db.QueryRow(ctx, `
    SELECT id, sample_id, project_id, version, target_kind, schema_version, payload, content_hash,
           batch_id, batch_item_id, attempt, COALESCE(generator_config, '{}'::jsonb),
           standard_version_id, standard_content_hash, blueprint_version_id, blueprint_content_hash,
           created_by, created_at
    FROM sample_versions WHERE id = $1 AND project_id = $2`, versionID, projectID,
	).Scan(&item.ID, &item.SampleID, &item.ProjectID, &item.Version, &item.TargetKind,
		&item.SchemaVersion, &item.Payload, &item.ContentHash,
		&item.BatchID, &item.BatchItemID, &item.Attempt, &item.GeneratorConfig,
		&item.StandardVersionID, &item.StandardContentHash,
		&item.BlueprintVersionID, &item.BlueprintContentHash,
		&item.CreatedBy, &item.CreatedAt)
	return item, err
}
