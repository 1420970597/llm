package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChainStandardStore 管理长链思维标准步骤及其版本历史。
// 表结构见 sql/migrations/0013_chain_standards.sql。
type ChainStandardStore struct {
	db *pgxpool.Pool
}

func NewChainStandardStore(db *pgxpool.Pool) *ChainStandardStore {
	return &ChainStandardStore{db: db}
}

// DirectionKeyOf 归一化方向名，用于展示与去重比对。
func DirectionKeyOf(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	return strings.Join(strings.Fields(lowered), " ")
}

// chainStandardSelect 是标准步骤的读取基语句。
//
// 设计要点：`chain_standards` 表本身**不存步骤内容**（见迁移 0013），
// 步骤只存在 `chain_standard_versions.steps`。因此「当前步骤」必须
// 通过 standard_id + version = current_version 关联取回，版本表是唯一事实源。
const chainStandardSelect = `
  SELECT s.id, s.dataset_id, s.domain_id, COALESCE(d.name, ''), s.direction_key,
         s.current_version, s.status, s.created_at, s.updated_at,
         COALESCE(v.steps, '[]'::jsonb)
  FROM chain_standards s
  LEFT JOIN domains d ON d.id = s.domain_id
  LEFT JOIN chain_standard_versions v ON v.standard_id = s.id AND v.version = s.current_version`

// scanChainStandard 读取一行标准步骤并解析 steps JSONB。
func scanChainStandard(row pgx.Row) (model.ChainStandard, error) {
	var item model.ChainStandard
	var steps []byte
	err := row.Scan(&item.ID, &item.DatasetID, &item.DomainID, &item.DomainName, &item.DirectionKey,
		&item.CurrentVersion, &item.Status, &item.CreatedAt, &item.UpdatedAt, &steps)
	if err != nil {
		return model.ChainStandard{}, err
	}
	item.Steps, err = decodeChainSteps(steps)
	if err != nil {
		return model.ChainStandard{}, err
	}
	return item, nil
}

// decodeChainSteps 解析步骤 JSONB；空值返回空切片而非 nil，保证 JSON 输出为 []。
func decodeChainSteps(raw []byte) ([]model.ChainStep, error) {
	steps := []model.ChainStep{}
	if len(raw) == 0 {
		return steps, nil
	}
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, fmt.Errorf("decode chain steps: %w", err)
	}
	if steps == nil {
		steps = []model.ChainStep{}
	}
	return steps, nil
}

// encodeChainSteps 把步骤编码为 JSONB 可写形式。
func encodeChainSteps(steps []model.ChainStep) ([]byte, error) {
	if steps == nil {
		steps = []model.ChainStep{}
	}
	return json.Marshal(steps)
}

// UpsertFromAI 写入 AI 生成的标准步骤。
// 首次写入 current_version=1；已存在则版本自增，两种情况都会留一条 source='ai' 的版本记录。
func (s *ChainStandardStore) UpsertFromAI(ctx context.Context, datasetID, domainID int64, directionKey string, steps []model.ChainStep) (model.ChainStandard, error) {
	if datasetID <= 0 || domainID <= 0 {
		return model.ChainStandard{}, fmt.Errorf("dataset id and domain id are required")
	}
	if len(steps) == 0 {
		return model.ChainStandard{}, fmt.Errorf("chain steps must not be empty")
	}
	payload, err := encodeChainSteps(steps)
	if err != nil {
		return model.ChainStandard{}, err
	}
	if strings.TrimSpace(directionKey) == "" {
		directionKey = DirectionKeyOf(fmt.Sprintf("%d", domainID))
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.ChainStandard{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var standardID int64
	var nextVersion int
	err = tx.QueryRow(ctx, `
    INSERT INTO chain_standards (dataset_id, domain_id, direction_key, current_version, status, updated_at)
    VALUES ($1, $2, $3, 1, 'generated', NOW())
    ON CONFLICT (dataset_id, domain_id) DO UPDATE
      SET current_version = chain_standards.current_version + 1,
          direction_key = EXCLUDED.direction_key,
          status = 'generated',
          updated_at = NOW()
    RETURNING id, current_version`,
		datasetID, domainID, directionKey).Scan(&standardID, &nextVersion)
	if err != nil {
		return model.ChainStandard{}, err
	}

	if _, err := tx.Exec(ctx, `
    INSERT INTO chain_standard_versions (standard_id, version, steps, source, change_note, created_by)
    VALUES ($1, $2, $3, 'ai', 'AI 生成', 0)`,
		standardID, nextVersion, payload); err != nil {
		return model.ChainStandard{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.ChainStandard{}, err
	}
	return s.GetByDomain(ctx, datasetID, domainID)
}

// GetByDomain 读取某个方向的当前标准步骤。
func (s *ChainStandardStore) GetByDomain(ctx context.Context, datasetID, domainID int64) (model.ChainStandard, error) {
	return scanChainStandard(s.db.QueryRow(ctx, chainStandardSelect+`
    WHERE s.dataset_id = $1 AND s.domain_id = $2`, datasetID, domainID))
}

// GetByDataset 列出数据集下全部方向的当前标准步骤，供前端一次性渲染。
func (s *ChainStandardStore) GetByDataset(ctx context.Context, datasetID int64) ([]model.ChainStandard, error) {
	rows, err := s.db.Query(ctx, chainStandardSelect+`
    WHERE s.dataset_id = $1
    ORDER BY s.domain_id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ChainStandard{}
	for rows.Next() {
		item, err := scanChainStandard(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateStepsWithVersion 保存用户编辑，current_version 自增并留一条 source='user' 版本记录。
func (s *ChainStandardStore) UpdateStepsWithVersion(ctx context.Context, datasetID, domainID int64, steps []model.ChainStep, changeNote string, createdBy int64) (model.ChainStandard, error) {
	if len(steps) == 0 {
		return model.ChainStandard{}, fmt.Errorf("chain steps must not be empty")
	}
	payload, err := encodeChainSteps(steps)
	if err != nil {
		return model.ChainStandard{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.ChainStandard{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var standardID int64
	var nextVersion int
	err = tx.QueryRow(ctx, `
    UPDATE chain_standards
    SET current_version = current_version + 1, status = 'edited', updated_at = NOW()
    WHERE dataset_id = $1 AND domain_id = $2
    RETURNING id, current_version`, datasetID, domainID).Scan(&standardID, &nextVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ChainStandard{}, fmt.Errorf("chain standard not found for dataset %d domain %d", datasetID, domainID)
		}
		return model.ChainStandard{}, err
	}

	if _, err := tx.Exec(ctx, `
    INSERT INTO chain_standard_versions (standard_id, version, steps, source, change_note, created_by)
    VALUES ($1, $2, $3, 'user', $4, $5)`,
		standardID, nextVersion, payload, changeNote, createdBy); err != nil {
		return model.ChainStandard{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.ChainStandard{}, err
	}
	return s.GetByDomain(ctx, datasetID, domainID)
}

// ListVersions 列出某方向的全部历史版本，按版本号倒序。
func (s *ChainStandardStore) ListVersions(ctx context.Context, datasetID, domainID int64) ([]model.ChainStandardVersion, error) {
	rows, err := s.db.Query(ctx, `
    SELECT v.id, v.standard_id, v.version, v.steps, v.source, v.change_note, v.created_by, v.created_at
    FROM chain_standard_versions v
    JOIN chain_standards s ON s.id = v.standard_id
    WHERE s.dataset_id = $1 AND s.domain_id = $2
    ORDER BY v.version DESC`, datasetID, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ChainStandardVersion{}
	for rows.Next() {
		var item model.ChainStandardVersion
		var steps []byte
		if err := rows.Scan(&item.ID, &item.StandardID, &item.Version, &steps, &item.Source,
			&item.ChangeNote, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		decoded, err := decodeChainSteps(steps)
		if err != nil {
			return nil, err
		}
		item.Steps = decoded
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetStandardID 返回某方向的标准步骤主键；不存在返回 pgx.ErrNoRows。
func (s *ChainStandardStore) GetStandardID(ctx context.Context, datasetID, domainID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
    SELECT id FROM chain_standards WHERE dataset_id = $1 AND domain_id = $2`, datasetID, domainID).Scan(&id)
	return id, err
}
