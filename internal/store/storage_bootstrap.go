package store

import (
	"context"
	"errors"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
)

// EnsureStorageProfile 按 (name, endpoint, bucket) 幂等引导一个结果存储配置（issue #83）。
//
// 与 EnsureProvider 完全同构，理由也一样：引导走 HTTP 的同一个 upsert 入口
// （校验只该有一份），但「已存在」时**不覆盖**管理员在后台改过的字段 ——
// 否则每次重启都会把用户手工调过的 endpoint/密钥打回环境变量的值。
//
// 返回值：
//   - id：目标行的 id（新建或已存在）
//   - created：本次是否真的新建了
//
// 已存在时只补全缺失的密钥（与 EnsureProvider 一致），其余字段不动。
func (s *AdminStore) EnsureStorageProfile(ctx context.Context, input model.StorageProfile) (int64, bool, error) {
	var existingID int64
	var hasSecret bool
	err := s.db.QueryRow(ctx, `
    SELECT id, COALESCE(encrypted_secret_key, '') <> ''
    FROM storage_profiles
    WHERE name = $1 AND endpoint = $2 AND bucket = $3
    LIMIT 1`, input.Name, input.Endpoint, input.Bucket).Scan(&existingID, &hasSecret)
	switch {
	case err == nil:
		if hasSecret || input.SecretAccessKey == "" {
			return existingID, false, nil
		}
		encrypted, err := s.box.Encrypt(input.SecretAccessKey)
		if err != nil {
			return 0, false, err
		}
		if _, err := s.db.Exec(ctx, `
      UPDATE storage_profiles SET encrypted_secret_key = $2, secret_key_masked = $3, updated_at = NOW()
      WHERE id = $1`, existingID, encrypted, appcrypto.MaskSecret(input.SecretAccessKey)); err != nil {
			return 0, false, err
		}
		return existingID, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		created, err := s.UpsertStorageProfile(ctx, input)
		if err != nil {
			return 0, false, err
		}
		return created.ID, true, nil
	default:
		return 0, false, err
	}
}
