package store

import (
	"context"
	"errors"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
)

// EnsureProvider 按 (name, base_url, model) 幂等引导一个 provider。
// 已存在时只补全缺失的密钥，不覆盖管理员在后台改过的其他字段。
func (s *AdminStore) EnsureProvider(ctx context.Context, input model.ModelProvider) (int64, bool, error) {
	var existingID int64
	var hasKey bool
	err := s.db.QueryRow(ctx, `
    SELECT id, COALESCE(encrypted_api_key, '') <> ''
    FROM model_providers
    WHERE name = $1 AND base_url = $2 AND model = $3
    LIMIT 1`, input.Name, input.BaseURL, input.Model).Scan(&existingID, &hasKey)
	switch {
	case err == nil:
		if hasKey || input.APIKey == "" {
			return existingID, false, nil
		}
		encrypted, err := s.box.Encrypt(input.APIKey)
		if err != nil {
			return 0, false, err
		}
		if _, err := s.db.Exec(ctx, `
      UPDATE model_providers SET encrypted_api_key = $2, api_key_masked = $3, updated_at = NOW()
      WHERE id = $1`, existingID, encrypted, appcrypto.MaskSecret(input.APIKey)); err != nil {
			return 0, false, err
		}
		return existingID, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		created, err := s.UpsertProvider(ctx, input)
		if err != nil {
			return 0, false, err
		}
		return created.ID, true, nil
	default:
		return 0, false, err
	}
}
