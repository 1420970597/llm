package store

import (
	"context"
	"fmt"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AdminStore struct {
	db  *pgxpool.Pool
	box *appcrypto.SecretBox
}

func NewAdminStore(db *pgxpool.Pool, box *appcrypto.SecretBox) *AdminStore {
	return &AdminStore{db: db, box: box}
}

func (s *AdminStore) ListProviders(ctx context.Context) ([]model.ModelProvider, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, name, base_url, model, provider_type, reasoning_effort, max_concurrency, timeout_seconds, is_active, api_key_masked, created_at, updated_at
    FROM model_providers
    ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	providers := []model.ModelProvider{}
	for rows.Next() {
		var item model.ModelProvider
		if err := rows.Scan(&item.ID, &item.Name, &item.BaseURL, &item.Model, &item.ProviderType, &item.ReasoningEffort, &item.MaxConcurrency, &item.TimeoutSeconds, &item.IsActive, &item.APIKeyMasked, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		providers = append(providers, item)
	}
	return providers, rows.Err()
}

func (s *AdminStore) UpsertProvider(ctx context.Context, input model.ModelProvider) (model.ModelProvider, error) {
	encryptedKey, err := s.box.Encrypt(input.APIKey)
	if err != nil {
		return model.ModelProvider{}, err
	}

	var item model.ModelProvider
	if input.ID == 0 {
		err = s.db.QueryRow(ctx, `
      INSERT INTO model_providers (
        name, base_url, model, provider_type, reasoning_effort, max_concurrency, timeout_seconds, is_active, encrypted_api_key, api_key_masked
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CASE WHEN $9 = '' THEN NULL ELSE $9 END, $10)
      RETURNING id, name, base_url, model, provider_type, reasoning_effort, max_concurrency, timeout_seconds, is_active, api_key_masked, created_at, updated_at`,
			input.Name,
			input.BaseURL,
			input.Model,
			input.ProviderType,
			input.ReasoningEffort,
			input.MaxConcurrency,
			input.TimeoutSeconds,
			input.IsActive,
			encryptedKey,
			appcrypto.MaskSecret(input.APIKey),
		).Scan(&item.ID, &item.Name, &item.BaseURL, &item.Model, &item.ProviderType, &item.ReasoningEffort, &item.MaxConcurrency, &item.TimeoutSeconds, &item.IsActive, &item.APIKeyMasked, &item.CreatedAt, &item.UpdatedAt)
	} else {
		err = s.db.QueryRow(ctx, `
      UPDATE model_providers SET
        name = $2,
        base_url = $3,
        model = $4,
        provider_type = $5,
        reasoning_effort = $6,
        max_concurrency = $7,
        timeout_seconds = $8,
        is_active = $9,
        encrypted_api_key = COALESCE(NULLIF($10, ''), encrypted_api_key),
        api_key_masked = CASE WHEN $11 = '' THEN api_key_masked ELSE $11 END,
        updated_at = NOW()
      WHERE id = $1
      RETURNING id, name, base_url, model, provider_type, reasoning_effort, max_concurrency, timeout_seconds, is_active, api_key_masked, created_at, updated_at`,
			input.ID,
			input.Name,
			input.BaseURL,
			input.Model,
			input.ProviderType,
			input.ReasoningEffort,
			input.MaxConcurrency,
			input.TimeoutSeconds,
			input.IsActive,
			encryptedKey,
			appcrypto.MaskSecret(input.APIKey),
		).Scan(&item.ID, &item.Name, &item.BaseURL, &item.Model, &item.ProviderType, &item.ReasoningEffort, &item.MaxConcurrency, &item.TimeoutSeconds, &item.IsActive, &item.APIKeyMasked, &item.CreatedAt, &item.UpdatedAt)
	}

	return item, err
}

func (s *AdminStore) GetProviderWithSecret(ctx context.Context, providerID int64) (model.ModelProvider, error) {
	var item model.ModelProvider
	var encryptedKey string
	err := s.db.QueryRow(ctx, `
    SELECT id, name, base_url, model, provider_type, reasoning_effort, max_concurrency, timeout_seconds, is_active, COALESCE(encrypted_api_key, ''), api_key_masked, created_at, updated_at
    FROM model_providers
    WHERE id = $1`,
		providerID,
	).Scan(
		&item.ID,
		&item.Name,
		&item.BaseURL,
		&item.Model,
		&item.ProviderType,
		&item.ReasoningEffort,
		&item.MaxConcurrency,
		&item.TimeoutSeconds,
		&item.IsActive,
		&encryptedKey,
		&item.APIKeyMasked,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return model.ModelProvider{}, err
	}

	item.APIKey, err = s.box.Decrypt(encryptedKey)
	if err != nil {
		return model.ModelProvider{}, err
	}
	return item, nil
}

func (s *AdminStore) ListStorageProfiles(ctx context.Context) ([]model.StorageProfile, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, name, provider, endpoint, region, bucket, access_key_id, secret_key_masked, use_path_style, is_active, is_default, created_at, updated_at
    FROM storage_profiles
    ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.StorageProfile{}
	for rows.Next() {
		var item model.StorageProfile
		if err := rows.Scan(&item.ID, &item.Name, &item.Provider, &item.Endpoint, &item.Region, &item.Bucket, &item.AccessKeyID, &item.SecretKeyMasked, &item.UsePathStyle, &item.IsActive, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *AdminStore) UpsertStorageProfile(ctx context.Context, input model.StorageProfile) (model.StorageProfile, error) {
	encryptedSecret, err := s.box.Encrypt(input.SecretAccessKey)
	if err != nil {
		return model.StorageProfile{}, err
	}

	var item model.StorageProfile
	if input.ID == 0 {
		err = s.db.QueryRow(ctx, `
      INSERT INTO storage_profiles (
        name, provider, endpoint, region, bucket, access_key_id, encrypted_secret_key, secret_key_masked, use_path_style, is_active, is_default
      ) VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $7 = '' THEN NULL ELSE $7 END, $8, $9, $10, $11)
      RETURNING id, name, provider, endpoint, region, bucket, access_key_id, secret_key_masked, use_path_style, is_active, is_default, created_at, updated_at`,
			input.Name,
			input.Provider,
			input.Endpoint,
			input.Region,
			input.Bucket,
			input.AccessKeyID,
			encryptedSecret,
			appcrypto.MaskSecret(input.SecretAccessKey),
			input.UsePathStyle,
			input.IsActive,
			input.IsDefault,
		).Scan(&item.ID, &item.Name, &item.Provider, &item.Endpoint, &item.Region, &item.Bucket, &item.AccessKeyID, &item.SecretKeyMasked, &item.UsePathStyle, &item.IsActive, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	} else {
		err = s.db.QueryRow(ctx, `
      UPDATE storage_profiles SET
        name = $2,
        provider = $3,
        endpoint = $4,
        region = $5,
        bucket = $6,
        access_key_id = $7,
        encrypted_secret_key = COALESCE(NULLIF($8, ''), encrypted_secret_key),
        secret_key_masked = CASE WHEN $9 = '' THEN secret_key_masked ELSE $9 END,
        use_path_style = $10,
        is_active = $11,
        is_default = $12,
        updated_at = NOW()
      WHERE id = $1
      RETURNING id, name, provider, endpoint, region, bucket, access_key_id, secret_key_masked, use_path_style, is_active, is_default, created_at, updated_at`,
			input.ID,
			input.Name,
			input.Provider,
			input.Endpoint,
			input.Region,
			input.Bucket,
			input.AccessKeyID,
			encryptedSecret,
			appcrypto.MaskSecret(input.SecretAccessKey),
			input.UsePathStyle,
			input.IsActive,
			input.IsDefault,
		).Scan(&item.ID, &item.Name, &item.Provider, &item.Endpoint, &item.Region, &item.Bucket, &item.AccessKeyID, &item.SecretKeyMasked, &item.UsePathStyle, &item.IsActive, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	}

	return item, err
}

func (s *AdminStore) ListStrategies(ctx context.Context) ([]model.GenerationStrategy, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, name, description, domain_count, questions_per_domain, answer_variants, reward_variants, planning_mode, is_default, created_at, updated_at
    FROM generation_strategies
    ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.GenerationStrategy{}
	for rows.Next() {
		var item model.GenerationStrategy
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.DomainCount, &item.QuestionsPerDomain, &item.AnswerVariants, &item.RewardVariants, &item.PlanningMode, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *AdminStore) UpsertStrategy(ctx context.Context, input model.GenerationStrategy) (model.GenerationStrategy, error) {
	var item model.GenerationStrategy
	var err error
	if input.ID == 0 {
		err = s.db.QueryRow(ctx, `
      INSERT INTO generation_strategies (
        name, description, domain_count, questions_per_domain, answer_variants, reward_variants, planning_mode, is_default
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
      RETURNING id, name, description, domain_count, questions_per_domain, answer_variants, reward_variants, planning_mode, is_default, created_at, updated_at`,
			input.Name,
			input.Description,
			input.DomainCount,
			input.QuestionsPerDomain,
			input.AnswerVariants,
			input.RewardVariants,
			input.PlanningMode,
			input.IsDefault,
		).Scan(&item.ID, &item.Name, &item.Description, &item.DomainCount, &item.QuestionsPerDomain, &item.AnswerVariants, &item.RewardVariants, &item.PlanningMode, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	} else {
		err = s.db.QueryRow(ctx, `
      UPDATE generation_strategies SET
        name = $2,
        description = $3,
        domain_count = $4,
        questions_per_domain = $5,
        answer_variants = $6,
        reward_variants = $7,
        planning_mode = $8,
        is_default = $9,
        updated_at = NOW()
      WHERE id = $1
      RETURNING id, name, description, domain_count, questions_per_domain, answer_variants, reward_variants, planning_mode, is_default, created_at, updated_at`,
			input.ID,
			input.Name,
			input.Description,
			input.DomainCount,
			input.QuestionsPerDomain,
			input.AnswerVariants,
			input.RewardVariants,
			input.PlanningMode,
			input.IsDefault,
		).Scan(&item.ID, &item.Name, &item.Description, &item.DomainCount, &item.QuestionsPerDomain, &item.AnswerVariants, &item.RewardVariants, &item.PlanningMode, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	}
	return item, err
}

func (s *AdminStore) ListPrompts(ctx context.Context) ([]model.PromptTemplate, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, name, stage, version, system_prompt, user_prompt, is_active, created_at, updated_at
    FROM prompt_templates
    ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.PromptTemplate{}
	for rows.Next() {
		var item model.PromptTemplate
		if err := rows.Scan(&item.ID, &item.Name, &item.Stage, &item.Version, &item.SystemPrompt, &item.UserPrompt, &item.IsActive, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *AdminStore) UpsertPrompt(ctx context.Context, input model.PromptTemplate) (model.PromptTemplate, error) {
	var item model.PromptTemplate
	var err error
	if input.ID == 0 {
		err = s.db.QueryRow(ctx, `
      INSERT INTO prompt_templates (
        name, stage, version, system_prompt, user_prompt, is_active
      ) VALUES ($1, $2, $3, $4, $5, $6)
      RETURNING id, name, stage, version, system_prompt, user_prompt, is_active, created_at, updated_at`,
			input.Name,
			input.Stage,
			input.Version,
			input.SystemPrompt,
			input.UserPrompt,
			input.IsActive,
		).Scan(&item.ID, &item.Name, &item.Stage, &item.Version, &item.SystemPrompt, &item.UserPrompt, &item.IsActive, &item.CreatedAt, &item.UpdatedAt)
	} else {
		err = s.db.QueryRow(ctx, `
      UPDATE prompt_templates SET
        name = $2,
        stage = $3,
        version = $4,
        system_prompt = $5,
        user_prompt = $6,
        is_active = $7,
        updated_at = NOW()
      WHERE id = $1
      RETURNING id, name, stage, version, system_prompt, user_prompt, is_active, created_at, updated_at`,
			input.ID,
			input.Name,
			input.Stage,
			input.Version,
			input.SystemPrompt,
			input.UserPrompt,
			input.IsActive,
		).Scan(&item.ID, &item.Name, &item.Stage, &item.Version, &item.SystemPrompt, &item.UserPrompt, &item.IsActive, &item.CreatedAt, &item.UpdatedAt)
	}
	return item, err
}

func (s *AdminStore) ListAuditLogs(ctx context.Context, limit int) ([]model.AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, actor, action, resource_type, resource_id, detail, created_at
    FROM audit_logs
    ORDER BY id DESC
    LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.AuditLog{}
	for rows.Next() {
		var item model.AuditLog
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &item.ResourceType, &item.ResourceID, &item.Detail, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *AdminStore) WriteAuditLog(ctx context.Context, actor, action, resourceType, resourceID, detail string) error {
	_, err := s.db.Exec(ctx, `
    INSERT INTO audit_logs (actor, action, resource_type, resource_id, detail)
    VALUES ($1, $2, $3, $4, $5)`, actor, action, resourceType, resourceID, detail)
	return err
}

func (s *AdminStore) Dashboard(ctx context.Context) (model.AdminDashboard, error) {
	counters := []struct {
		query string
		dest  *int
	}{}

	dashboard := model.AdminDashboard{}
	counters = append(counters,
		struct {
			query string
			dest  *int
		}{`SELECT COUNT(*) FROM model_providers`, &dashboard.ProviderCount},
		struct {
			query string
			dest  *int
		}{`SELECT COUNT(*) FROM model_providers WHERE is_active = TRUE`, &dashboard.ActiveProviderCount},
		struct {
			query string
			dest  *int
		}{`SELECT COUNT(*) FROM storage_profiles`, &dashboard.StorageProfileCount},
		struct {
			query string
			dest  *int
		}{`SELECT COUNT(*) FROM generation_strategies`, &dashboard.StrategyCount},
		struct {
			query string
			dest  *int
		}{`SELECT COUNT(*) FROM prompt_templates`, &dashboard.PromptCount},
		struct {
			query string
			dest  *int
		}{`SELECT COUNT(*) FROM audit_logs`, &dashboard.AuditLogCount},
	)

	for _, counter := range counters {
		if err := s.db.QueryRow(ctx, counter.query).Scan(counter.dest); err != nil {
			return model.AdminDashboard{}, fmt.Errorf("dashboard query failed: %w", err)
		}
	}

	return dashboard, nil
}
