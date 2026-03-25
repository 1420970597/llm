package store

import (
	"context"
	"encoding/json"
	"fmt"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DatasetStore struct {
	db  *pgxpool.Pool
	box *appcrypto.SecretBox
}

func NewDatasetStore(db *pgxpool.Pool, box *appcrypto.SecretBox) *DatasetStore {
	return &DatasetStore{db: db, box: box}
}

func (s *DatasetStore) Estimate(ctx context.Context, rootKeyword string, targetSize int, strategyID int64) (model.PlanEstimate, error) {
	estimate := model.PlanEstimate{DomainCount: 100, QuestionsPerDomain: 10, AnswerVariants: 1, RewardVariants: 1}
	if strategyID != 0 {
		if err := s.db.QueryRow(ctx, `
      SELECT domain_count, questions_per_domain, answer_variants, reward_variants
      FROM generation_strategies WHERE id = $1`, strategyID,
		).Scan(&estimate.DomainCount, &estimate.QuestionsPerDomain, &estimate.AnswerVariants, &estimate.RewardVariants); err != nil {
			return model.PlanEstimate{}, err
		}
	}
	estimate.DomainCount = max(1, estimate.DomainCount)
	estimate.QuestionsPerDomain = max(1, estimate.QuestionsPerDomain)
	estimate.AnswerVariants = max(1, estimate.AnswerVariants)
	estimate.RewardVariants = max(1, estimate.RewardVariants)
	if targetSize > 0 {
		estimate.DomainCount = min(max(10, targetSize/estimate.QuestionsPerDomain), 1000)
	}
	estimate.EstimatedQuestions = estimate.DomainCount * estimate.QuestionsPerDomain
	estimate.EstimatedSamples = estimate.EstimatedQuestions * estimate.AnswerVariants * estimate.RewardVariants
	if rootKeyword == "" {
		return model.PlanEstimate{}, fmt.Errorf("root keyword is required")
	}
	return estimate, nil
}

func (s *DatasetStore) CreateDataset(ctx context.Context, input model.Dataset) (model.Dataset, error) {
	payload, _ := json.Marshal(input.Estimate)
	var item model.Dataset
	err := s.db.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, target_size, status, strategy_id, provider_id, storage_profile_id, estimate_json, updated_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
    RETURNING id, name, root_keyword, target_size, status, strategy_id, provider_id, storage_profile_id, estimate_json, created_at, updated_at`,
		input.Name,
		input.RootKeyword,
		input.TargetSize,
		input.Status,
		input.StrategyID,
		input.ProviderID,
		input.StorageProfileID,
		payload,
	).Scan(&item.ID, &item.Name, &item.RootKeyword, &item.TargetSize, &item.Status, &item.StrategyID, &item.ProviderID, &item.StorageProfileID, &payload, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.Dataset{}, err
	}
	_ = json.Unmarshal(payload, &item.Estimate)
	return item, nil
}

func (s *DatasetStore) ListDatasets(ctx context.Context) ([]model.Dataset, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, name, root_keyword, target_size, status, strategy_id, provider_id, storage_profile_id, estimate_json, created_at, updated_at
    FROM datasets ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Dataset{}
	for rows.Next() {
		var item model.Dataset
		var payload []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.RootKeyword, &item.TargetSize, &item.Status, &item.StrategyID, &item.ProviderID, &item.StorageProfileID, &payload, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &item.Estimate)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *DatasetStore) GetDataset(ctx context.Context, id int64) (model.Dataset, error) {
	var item model.Dataset
	var payload []byte
	err := s.db.QueryRow(ctx, `
    SELECT id, name, root_keyword, target_size, status, strategy_id, provider_id, storage_profile_id, estimate_json, created_at, updated_at
    FROM datasets WHERE id = $1`, id,
	).Scan(&item.ID, &item.Name, &item.RootKeyword, &item.TargetSize, &item.Status, &item.StrategyID, &item.ProviderID, &item.StorageProfileID, &payload, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.Dataset{}, err
	}
	_ = json.Unmarshal(payload, &item.Estimate)
	return item, nil
}

func (s *DatasetStore) ReplaceDomains(ctx context.Context, datasetID int64, domains []model.Domain, edges []model.DomainEdge) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM domain_edges WHERE dataset_id = $1`, datasetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM domains WHERE dataset_id = $1`, datasetID); err != nil {
		return err
	}

	for _, domain := range domains {
		var item model.Domain
		if err := tx.QueryRow(ctx, `
      INSERT INTO domains (dataset_id, name, canonical_name, level, parent_id, source, review_status)
      VALUES ($1, $2, $3, $4, $5, $6, $7)
      RETURNING id, dataset_id, name, canonical_name, level, parent_id, source, review_status, created_at, updated_at`,
			datasetID,
			domain.Name,
			domain.Canonical,
			domain.Level,
			domain.ParentID,
			domain.Source,
			domain.ReviewStatus,
		).Scan(&item.ID, &item.DatasetID, &item.Name, &item.Canonical, &item.Level, &item.ParentID, &item.Source, &item.ReviewStatus, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return err
		}
	}

	for _, edge := range edges {
		if _, err := tx.Exec(ctx, `
      INSERT INTO domain_edges (dataset_id, source_domain_id, target_domain_id, relation_type)
      VALUES ($1, $2, $3, $4)`, datasetID, edge.SourceID, edge.TargetID, edge.Relation); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE datasets SET updated_at = NOW() WHERE id = $1`, datasetID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *DatasetStore) ListDomains(ctx context.Context, datasetID int64) ([]model.Domain, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, dataset_id, name, canonical_name, level, parent_id, source, review_status, created_at, updated_at
    FROM domains WHERE dataset_id = $1 ORDER BY id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Domain{}
	for rows.Next() {
		var item model.Domain
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.Name, &item.Canonical, &item.Level, &item.ParentID, &item.Source, &item.ReviewStatus, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *DatasetStore) ListDomainEdges(ctx context.Context, datasetID int64) ([]model.DomainEdge, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, dataset_id, source_domain_id, target_domain_id, relation_type, created_at
    FROM domain_edges WHERE dataset_id = $1 ORDER BY id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.DomainEdge{}
	for rows.Next() {
		var item model.DomainEdge
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.SourceID, &item.TargetID, &item.Relation, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *DatasetStore) UpdateGraph(ctx context.Context, datasetID int64, domains []model.Domain) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, domain := range domains {
		if _, err := tx.Exec(ctx, `
      UPDATE domains SET name = $2, canonical_name = $3, review_status = $4, updated_at = NOW()
      WHERE id = $1 AND dataset_id = $5`,
			domain.ID,
			domain.Name,
			domain.Canonical,
			domain.ReviewStatus,
			datasetID,
		); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE datasets SET updated_at = NOW() WHERE id = $1`, datasetID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *DatasetStore) ConfirmDomains(ctx context.Context, datasetID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE datasets SET status = 'domains_confirmed', updated_at = NOW() WHERE id = $1`, datasetID)
	return err
}

func (s *DatasetStore) UpdateStatus(ctx context.Context, datasetID int64, status string) error {
	_, err := s.db.Exec(ctx, `UPDATE datasets SET status = $2, updated_at = NOW() WHERE id = $1`, datasetID, status)
	return err
}

func (s *DatasetStore) PipelineProgress(ctx context.Context, datasetID int64) (model.PipelineProgress, error) {
	dataset, err := s.GetDataset(ctx, datasetID)
	if err != nil {
		return model.PipelineProgress{}, err
	}

	var domainCount, questionCount, reasoningCount, rewardCount, artifactCount int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM domains WHERE dataset_id = $1`, datasetID).Scan(&domainCount); err != nil {
		return model.PipelineProgress{}, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM questions WHERE dataset_id = $1`, datasetID).Scan(&questionCount); err != nil {
		return model.PipelineProgress{}, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM reasoning_records WHERE dataset_id = $1`, datasetID).Scan(&reasoningCount); err != nil {
		return model.PipelineProgress{}, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM reward_records WHERE dataset_id = $1`, datasetID).Scan(&rewardCount); err != nil {
		return model.PipelineProgress{}, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM artifacts WHERE dataset_id = $1`, datasetID).Scan(&artifactCount); err != nil {
		return model.PipelineProgress{}, err
	}

	rankByStatus := map[string]int{
		"draft":              0,
		"domains_confirmed":  1,
		"questions_queued":   1,
		"questions_generated": 2,
		"reasoning_queued":   2,
		"reasoning_generated": 3,
		"rewards_queued":     3,
		"rewards_generated":   4,
		"export_queued":      4,
		"export_generated":    5,
	}
	queuedStageByStatus := map[string]string{
		"questions_queued": "questions",
		"reasoning_queued": "reasoning",
		"rewards_queued":   "rewards",
		"export_queued":    "export",
	}
	statusRank := rankByStatus[dataset.Status]
	queuedStage := queuedStageByStatus[dataset.Status]

	stageState := func(key string, threshold int, evidenceCount int) string {
		if statusRank >= threshold {
			return "completed"
		}
		if queuedStage == key {
			return "queued"
		}
		if evidenceCount > 0 {
			return "in_progress"
		}
		return "pending"
	}

	exportState := "pending"
	exportSummary := "尚未产出导出工件"
	switch {
	case dataset.Status == "export_queued":
		exportState = "queued"
		if artifactCount > 0 {
			exportSummary = fmt.Sprintf("导出任务已入队，已有 %d 个历史工件，等待最新交付文件", artifactCount)
		} else {
			exportSummary = "导出任务已入队，等待写入交付文件"
		}
	case artifactCount > 0:
		exportState = "completed"
		exportSummary = fmt.Sprintf("已产出 %d 个工件", artifactCount)
	case dataset.Status == "export_generated":
		exportState = "in_progress"
		exportSummary = "导出状态已完成，正在确认交付文件"
	}

	stages := []model.PipelineStageStatus{
		{Key: "domains", Label: "领域整理", State: stageState("domains", 1, domainCount), Count: domainCount, Summary: fmt.Sprintf("已整理 %d 个领域", domainCount)},
		{Key: "questions", Label: "问题生成", State: stageState("questions", 2, questionCount), Count: questionCount, Summary: fmt.Sprintf("已生成 %d 条问题", questionCount)},
		{Key: "reasoning", Label: "推理生成", State: stageState("reasoning", 3, reasoningCount), Count: reasoningCount, Summary: fmt.Sprintf("已生成 %d 条推理", reasoningCount)},
		{Key: "rewards", Label: "奖励评估", State: stageState("rewards", 4, rewardCount), Count: rewardCount, Summary: fmt.Sprintf("已生成 %d 条评分", rewardCount)},
		{Key: "export", Label: "导出交付", State: exportState, Count: artifactCount, Summary: exportSummary},
	}

	completed := 0
	currentStage := "done"
	for _, stage := range stages {
		if stage.State == "completed" {
			completed++
			continue
		}
		if currentStage == "done" {
			currentStage = stage.Key
		}
	}

	completionByStatus := map[string]int{
		"draft":               15,
		"domains_confirmed":   35,
		"questions_queued":    35,
		"questions_generated": 55,
		"reasoning_queued":    55,
		"reasoning_generated": 75,
		"rewards_queued":      75,
		"rewards_generated":   90,
		"export_queued":       90,
		"export_generated":    100,
	}
	completionPercent, ok := completionByStatus[dataset.Status]
	if !ok {
		completionPercent = completed * 20
	}
	if dataset.Status == "export_generated" && artifactCount == 0 {
		completionPercent = 90
	}

	return model.PipelineProgress{
		DatasetID:         datasetID,
		DatasetStatus:     dataset.Status,
		CurrentStage:      currentStage,
		CompletionPercent: completionPercent,
		Stages:            stages,
		QuestionCount:     questionCount,
		ReasoningCount:    reasoningCount,
		RewardCount:       rewardCount,
		ArtifactCount:     artifactCount,
	}, nil
}

func (s *DatasetStore) ResolveProvider(ctx context.Context, providerID int64) (string, string, string, string, string, error) {
	var baseURL, modelName, providerType, reasoningEffort, encryptedKey string
	err := s.db.QueryRow(ctx, `
    SELECT base_url, model, provider_type, reasoning_effort, COALESCE(encrypted_api_key, '')
    FROM model_providers
    WHERE id = $1`, providerID,
	).Scan(&baseURL, &modelName, &providerType, &reasoningEffort, &encryptedKey)
	if err != nil {
		return "", "", "", "", "", err
	}
	key, err := s.box.Decrypt(encryptedKey)
	if err != nil {
		return "", "", "", "", "", err
	}
	return baseURL, modelName, providerType, reasoningEffort, key, nil
}

func (s *DatasetStore) ResolveStorageProfile(ctx context.Context, storageProfileID int64) (endpoint, region, bucket, accessKeyID, secretKey string, usePathStyle bool, err error) {
	var encryptedSecret string
	query := `
    SELECT endpoint, region, bucket, access_key_id, COALESCE(encrypted_secret_key, ''), use_path_style
    FROM storage_profiles
    WHERE is_active = TRUE`
	args := []any{}
	if storageProfileID != 0 {
		query += ` AND id = $1`
		args = append(args, storageProfileID)
	} else {
		query += ` ORDER BY is_default DESC, id DESC LIMIT 1`
	}
	row := s.db.QueryRow(ctx, query, args...)
	err = row.Scan(&endpoint, &region, &bucket, &accessKeyID, &encryptedSecret, &usePathStyle)
	if err != nil && storageProfileID != 0 && err == pgx.ErrNoRows {
		row = s.db.QueryRow(ctx, `
    SELECT endpoint, region, bucket, access_key_id, COALESCE(encrypted_secret_key, ''), use_path_style
    FROM storage_profiles
    WHERE is_active = TRUE
    ORDER BY is_default DESC, id DESC
    LIMIT 1`)
		err = row.Scan(&endpoint, &region, &bucket, &accessKeyID, &encryptedSecret, &usePathStyle)
	}
	if err != nil {
		return "", "", "", "", "", false, err
	}
	secretKey, err = s.box.Decrypt(encryptedSecret)
	if err != nil {
		return "", "", "", "", "", false, err
	}
	return endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
