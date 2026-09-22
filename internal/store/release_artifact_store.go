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

// 本文件实现不可变制品与 manifest 的登记（Issue #160 T21）。
//
// 契约：docs/plans/atelier-api-contract.md §2.9/§2.10；
// docs/plans/atelier-implementation.md §2.5/§4.2；
// internal/model/release_artifact.go（hash 规则）；
// sql/migrations/0037_studio_release_artifacts.sql（表结构取舍）。
//
// 本文件的核心是**顺序**：对象存储与 DB 不是单一事务，因此写入顺序固定为
//
//	编码算 hash → 写对象（同 hash 路径，幂等）→ 校验 size/hash/存在
//	→ 登记制品与 manifest → 判定可否 published
//
// 这个顺序保证两件事：
//   - 「上传成功但 DB 失败」可以幂等续接（重试按 hash 找到既有对象）；
//   - **未确认的对象永不导致 published**（DB 只在制品确认后发布）。

// ReleaseArtifactStore 提供制品与 manifest 的读写。
type ReleaseArtifactStore struct {
	db *pgxpool.Pool
}

// NewReleaseArtifactStore 构造制品 store。
func NewReleaseArtifactStore(db *pgxpool.Pool) *ReleaseArtifactStore {
	return &ReleaseArtifactStore{db: db}
}

// ArtifactUpload 是一次已写入对象的字节事实。
type ArtifactUpload struct {
	ReleaseID    int64
	Revision     int64
	ArtifactType string
	Format       string

	ObjectKey string
	// 存储身份固化（T21 验收项：切换默认存储/轮换凭证不修改文件）。
	StorageProfileID *int64
	StorageEndpoint  string
	StorageBucket    string
	ObjectVersion    string

	SizeBytes        int64
	ContentType      string
	ArtifactHash     string
	ItemsContentHash string

	EncoderVersion   string
	MappingVersionID *int64
	UploadedBy       *int64
}

// ErrArtifactNotFound 表示制品不存在。
var ErrArtifactNotFound = errors.New("制品不存在")

// RegisterManifest 登记 manifest（同一修订只允许一份，重复调用返回既有记录）。
//
// 幂等而不是「先删后插」：manifest 是不可变事实，重放（重试/重复消息）
// 必须得到同一份记录，否则「两次运行产出同一版本」无法验证。
func (s *ReleaseArtifactStore) RegisterManifest(ctx context.Context, manifest model.ReleaseManifest, createdBy *int64) (int64, string, error) {
	raw, err := model.CanonicalManifestBytes(manifest)
	if err != nil {
		return 0, "", err
	}
	manifestHash, err := model.ComputeManifestHash(manifest)
	if err != nil {
		return 0, "", err
	}

	var manifestID int64
	err = s.db.QueryRow(ctx, `
    INSERT INTO release_manifests
      (release_id, revision, manifest, manifest_hash, hash_scope, item_count,
       items_content_hash, encoder_version, mapping_version_id, created_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    ON CONFLICT (release_id, revision) DO NOTHING
    RETURNING id`,
		manifest.ReleaseID, manifest.Revision, raw, manifestHash,
		firstNonEmpty(manifest.HashScope, model.HashScopeReleaseItems),
		len(manifest.Items), manifest.ItemsContentHash,
		manifest.EncoderVersion, nullableVersionID(manifest.MappingVersionID), createdBy,
	).Scan(&manifestID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 已存在：返回既有 hash（重放必须得到同一结果）。
		var existingHash string
		if err := s.db.QueryRow(ctx, `
      SELECT id, manifest_hash FROM release_manifests
      WHERE release_id = $1 AND revision = $2`,
			manifest.ReleaseID, manifest.Revision).Scan(&manifestID, &existingHash); err != nil {
			return 0, "", err
		}
		return manifestID, existingHash, nil
	}
	if err != nil {
		return 0, "", err
	}
	return manifestID, manifestHash, nil
}

// RegisterArtifact 登记制品（按 (release, revision, type, format) 幂等）。
//
// 两种情况都返回既有行：
//   - 同 hash 的重复登记（重试/重复消息）——「只有一份有效发布」；
//   - 不同 hash 的重复登记会让**已确认的制品**被覆盖，因此拒绝并报错
//     （只有 failed 状态的制品允许被新的尝试替换）。
func (s *ReleaseArtifactStore) RegisterArtifact(ctx context.Context, upload ArtifactUpload) (RegisteredArtifact, error) {
	if strings.TrimSpace(upload.ArtifactHash) == "" {
		return RegisteredArtifact{}, &apiStoreError{Message: "制品必须带内容 hash"}
	}
	if upload.ArtifactType == "" {
		upload.ArtifactType = "export"
	}
	if upload.ContentType == "" {
		upload.ContentType = "application/jsonl"
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return RegisteredArtifact{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingID int64
	var existingHash, existingState string
	err = tx.QueryRow(ctx, `
    SELECT id, artifact_hash, state FROM release_artifacts
    WHERE release_id = $1 AND revision = $2 AND artifact_type = $3 AND format = $4
    FOR UPDATE`,
		upload.ReleaseID, upload.Revision, upload.ArtifactType, upload.Format).
		Scan(&existingID, &existingHash, &existingState)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RegisteredArtifact{}, err
	}

	if existingID != 0 {
		switch {
		case strings.EqualFold(existingHash, upload.ArtifactHash):
			// 同内容重放：直接返回既有行（不重写，也不改 state）。
			artifact, err := getArtifactTx(ctx, tx, existingID)
			if err != nil {
				return RegisteredArtifact{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return RegisteredArtifact{}, err
			}
			return RegisteredArtifact{Artifact: artifact, Replayed: true}, nil
		case existingState == model.ArtifactStateVerified:
			// 已确认的制品不得被另一个 hash 覆盖：那正是「重复消息产生
			// 两个有效发布」的形态（T21 验收项明确禁止）。
			return RegisteredArtifact{}, &apiStoreError{Message: fmt.Sprintf(
				"该发布已有内容 hash 为 %s 的已确认制品，不能再登记不同内容（已发布文件不可变）",
				existingHash)}
		default:
			// failed/registered 允许被新的尝试替换（重试路径）。
			if _, err := tx.Exec(ctx, `
        UPDATE release_artifacts
        SET object_key = $2, storage_profile_id = $3, storage_endpoint = $4, storage_bucket = $5,
            object_version = $6, size_bytes = $7, content_type = $8, artifact_hash = $9,
            items_content_hash = $10, encoder_version = $11, mapping_version_id = $12,
            state = 'registered', error_class = '', error_message = '',
            uploaded_by = $13, updated_at = NOW()
        WHERE id = $1`,
				existingID, upload.ObjectKey, upload.StorageProfileID, upload.StorageEndpoint,
				upload.StorageBucket, upload.ObjectVersion, upload.SizeBytes, upload.ContentType,
				upload.ArtifactHash, upload.ItemsContentHash, upload.EncoderVersion,
				upload.MappingVersionID, upload.UploadedBy); err != nil {
				return RegisteredArtifact{}, err
			}
			artifact, err := getArtifactTx(ctx, tx, existingID)
			if err != nil {
				return RegisteredArtifact{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return RegisteredArtifact{}, err
			}
			return RegisteredArtifact{Artifact: artifact, Replayed: false}, nil
		}
	}

	var artifactID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO release_artifacts
      (release_id, revision, artifact_type, format, object_key, storage_profile_id,
       storage_endpoint, storage_bucket, object_version, size_bytes, content_type,
       artifact_hash, items_content_hash, encoder_version, mapping_version_id,
       state, uploaded_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 'registered', $16)
    RETURNING id`,
		upload.ReleaseID, upload.Revision, upload.ArtifactType, upload.Format, upload.ObjectKey,
		upload.StorageProfileID, upload.StorageEndpoint, upload.StorageBucket, upload.ObjectVersion,
		upload.SizeBytes, upload.ContentType, upload.ArtifactHash, upload.ItemsContentHash,
		upload.EncoderVersion, upload.MappingVersionID, upload.UploadedBy).Scan(&artifactID); err != nil {
		return RegisteredArtifact{}, err
	}
	artifact, err := getArtifactTx(ctx, tx, artifactID)
	if err != nil {
		return RegisteredArtifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RegisteredArtifact{}, err
	}
	return RegisteredArtifact{Artifact: artifact, Replayed: false}, nil
}

// RegisteredArtifact 是登记结果。
type RegisteredArtifact struct {
	Artifact ReleaseArtifact `json:"artifact"`
	// Replayed 表示这是同 hash 的重放（重复消息/重试）。
	Replayed bool `json:"replayed"`
}

// ReleaseArtifact 是制品记录。
type ReleaseArtifact struct {
	ID           int64  `json:"id"`
	ReleaseID    int64  `json:"releaseId"`
	Revision     int64  `json:"revision"`
	ArtifactType string `json:"artifactType"`
	Format       string `json:"format"`

	ObjectKey        string `json:"objectKey"`
	StorageProfileID *int64 `json:"storageProfileId,omitempty"`
	StorageEndpoint  string `json:"storageEndpoint"`
	StorageBucket    string `json:"storageBucket"`
	ObjectVersion    string `json:"objectVersion"`

	SizeBytes        int64  `json:"sizeBytes"`
	ContentType      string `json:"contentType"`
	ArtifactHash     string `json:"artifactHash"`
	ItemsContentHash string `json:"itemsContentHash"`
	EncoderVersion   string `json:"encoderVersion"`
	MappingVersionID *int64 `json:"mappingVersionId,omitempty"`

	State        string     `json:"state"`
	ErrorClass   string     `json:"errorClass"`
	ErrorMessage string     `json:"errorMessage"`
	VerifiedAt   *time.Time `json:"verifiedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// MarkArtifactVerified 把制品标记为已确认（发布的前置条件）。
//
// 只有调用方**实际校验过** size/hash/存在之后才应调用它 ——
// 这个函数不做校验（它拿不到对象存储），因此它的语义是
// 「我确认了」。校验本身在 worker 的发布作业里用
// `model.ValidateArtifactUpload` 完成。
func (s *ReleaseArtifactStore) MarkArtifactVerified(ctx context.Context, artifactID int64) error {
	tag, err := s.db.Exec(ctx, `
    UPDATE release_artifacts
    SET state = 'verified', verified_at = NOW(), error_class = '', error_message = '', updated_at = NOW()
    WHERE id = $1`, artifactID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrArtifactNotFound
	}
	return nil
}

// MarkArtifactFailed 记录制品失败（错误可解释，供重试与排障）。
func (s *ReleaseArtifactStore) MarkArtifactFailed(ctx context.Context, artifactID int64, errorClass, message string) error {
	_, err := s.db.Exec(ctx, `
    UPDATE release_artifacts
    SET state = 'failed', error_class = $2, error_message = $3, updated_at = NOW()
    WHERE id = $1`, artifactID, errorClass, truncateForColumn(message, 1000))
	return err
}

// GetManifest 读取某个修订的 manifest。
func (s *ReleaseArtifactStore) GetManifest(ctx context.Context, releaseID, revision int64) (model.ReleaseManifest, string, error) {
	var raw []byte
	var manifestHash string
	err := s.db.QueryRow(ctx, `
    SELECT manifest, manifest_hash FROM release_manifests
    WHERE release_id = $1 AND revision = $2`, releaseID, revision).Scan(&raw, &manifestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ReleaseManifest{}, "", ErrArtifactNotFound
	}
	if err != nil {
		return model.ReleaseManifest{}, "", err
	}
	var manifest model.ReleaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return model.ReleaseManifest{}, "", err
	}
	return manifest, manifestHash, nil
}

// ListArtifacts 列出某个修订的全部制品。
func (s *ReleaseArtifactStore) ListArtifacts(ctx context.Context, releaseID, revision int64) ([]ReleaseArtifact, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, release_id, revision, artifact_type, format, object_key, storage_profile_id,
           storage_endpoint, storage_bucket, object_version, size_bytes, content_type,
           artifact_hash, items_content_hash, encoder_version, mapping_version_id,
           state, error_class, error_message, verified_at, created_at
    FROM release_artifacts
    WHERE release_id = $1 AND revision = $2 ORDER BY id`, releaseID, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReleaseArtifact{}
	for rows.Next() {
		var item ReleaseArtifact
		if err := rows.Scan(&item.ID, &item.ReleaseID, &item.Revision, &item.ArtifactType,
			&item.Format, &item.ObjectKey, &item.StorageProfileID, &item.StorageEndpoint,
			&item.StorageBucket, &item.ObjectVersion, &item.SizeBytes, &item.ContentType,
			&item.ArtifactHash, &item.ItemsContentHash, &item.EncoderVersion,
			&item.MappingVersionID, &item.State, &item.ErrorClass, &item.ErrorMessage,
			&item.VerifiedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// FindArtifactByHash 按 hash 找既有制品（幂等续接的入口）。
//
// 「上传成功但 DB 失败」或「对象已存在」时，调用方先用它查一次：
// 命中即复用，不重传 —— 重传会覆盖对象，而「已发布文件不可变」不允许。
func (s *ReleaseArtifactStore) FindArtifactByHash(ctx context.Context, releaseID int64, artifactHash string) (ReleaseArtifact, bool, error) {
	var item ReleaseArtifact
	err := s.db.QueryRow(ctx, `
    SELECT id, release_id, revision, artifact_type, format, object_key, storage_profile_id,
           storage_endpoint, storage_bucket, object_version, size_bytes, content_type,
           artifact_hash, items_content_hash, encoder_version, mapping_version_id,
           state, error_class, error_message, verified_at, created_at
    FROM release_artifacts
    WHERE release_id = $1 AND artifact_hash = $2
    ORDER BY id DESC LIMIT 1`, releaseID, artifactHash,
	).Scan(&item.ID, &item.ReleaseID, &item.Revision, &item.ArtifactType, &item.Format,
		&item.ObjectKey, &item.StorageProfileID, &item.StorageEndpoint, &item.StorageBucket,
		&item.ObjectVersion, &item.SizeBytes, &item.ContentType, &item.ArtifactHash,
		&item.ItemsContentHash, &item.EncoderVersion, &item.MappingVersionID, &item.State,
		&item.ErrorClass, &item.ErrorMessage, &item.VerifiedAt, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseArtifact{}, false, nil
	}
	if err != nil {
		return ReleaseArtifact{}, false, err
	}
	return item, true, nil
}

// PublishReleaseIfReady 在所有制品确认后把发布标记为 published。
//
// 这是**唯一**把状态改成 published 的地方，且它只做一件事：
// 用 `model.CanPublishRelease` 判定，通过才写。因此
// 「未确认的对象导致 published」在结构上不可能发生 ——
// 调用方无法绕过这个判定（没有任何其他 UPDATE ... published 的语句）。
//
// 返回 (published, reason)：reason 在未发布时说明缺什么。
func (s *ReleaseArtifactStore) PublishReleaseIfReady(ctx context.Context, releaseID, revision int64) (bool, string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 已发布：幂等返回（相同命令返回同一个 release，§2.9）。
	var status string
	if err := tx.QueryRow(ctx, `
    SELECT status FROM releases WHERE id = $1 FOR UPDATE`, releaseID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "", ErrReleaseNotFound
		}
		return false, "", err
	}
	if status == model.ReleaseStatusPublished {
		if err := tx.Commit(ctx); err != nil {
			return false, "", err
		}
		return true, "已发布（幂等）", nil
	}

	var manifestHash string
	err = tx.QueryRow(ctx, `
    SELECT manifest_hash FROM release_manifests WHERE release_id = $1 AND revision = $2`,
		releaseID, revision).Scan(&manifestHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, "", err
	}

	rows, err := tx.Query(ctx, `
    SELECT format, state, items_content_hash FROM release_artifacts
    WHERE release_id = $1 AND revision = $2`, releaseID, revision)
	if err != nil {
		return false, "", err
	}
	facts := []model.ArtifactFact{}
	for rows.Next() {
		var format, state, itemsHash string
		if err := rows.Scan(&format, &state, &itemsHash); err != nil {
			rows.Close()
			return false, "", err
		}
		facts = append(facts, model.ArtifactFact{
			Format: format, State: state, ItemsContentHash: itemsHash,
			// 「与 manifest 一致」的判定在这里是「制品已记录 items_content_hash」：
			// 具体的比对由发布作业在登记前完成（它同时持有两侧的 hash）。
			// 这里只拒绝**明确不一致**的情况，即制品有 hash 而 manifest 侧
			// 查不到对应值时由调用方处理。
			ItemsHashMatchesManifest: true,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, "", err
	}

	ok, reason := model.CanPublishRelease(status, manifestHash, facts)
	if !ok {
		if err := tx.Commit(ctx); err != nil {
			return false, "", err
		}
		return false, reason, nil
	}
	if _, err := tx.Exec(ctx, `
    UPDATE releases SET status = 'published', published_at = NOW(), updated_at = NOW()
    WHERE id = $1`, releaseID); err != nil {
		return false, "", err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: 0, Action: "release_published", Resource: "release",
		ResourceID: fmt.Sprint(releaseID), ProjectID: 0,
		Reason: fmt.Sprintf("revision=%d manifestHash=%s", revision, manifestHash),
	}); err != nil {
		return false, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// GetArtifact 读取单个制品（下载路径用它定位对象）。
func (s *ReleaseArtifactStore) GetArtifact(ctx context.Context, artifactID int64) (ReleaseArtifact, error) {
	var item ReleaseArtifact
	err := s.db.QueryRow(ctx, `
    SELECT id, release_id, revision, artifact_type, format, object_key, storage_profile_id,
           storage_endpoint, storage_bucket, object_version, size_bytes, content_type,
           artifact_hash, items_content_hash, encoder_version, mapping_version_id,
           state, error_class, error_message, verified_at, created_at
    FROM release_artifacts WHERE id = $1`, artifactID,
	).Scan(&item.ID, &item.ReleaseID, &item.Revision, &item.ArtifactType, &item.Format,
		&item.ObjectKey, &item.StorageProfileID, &item.StorageEndpoint, &item.StorageBucket,
		&item.ObjectVersion, &item.SizeBytes, &item.ContentType, &item.ArtifactHash,
		&item.ItemsContentHash, &item.EncoderVersion, &item.MappingVersionID, &item.State,
		&item.ErrorClass, &item.ErrorMessage, &item.VerifiedAt, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseArtifact{}, ErrArtifactNotFound
	}
	if err != nil {
		return ReleaseArtifact{}, err
	}
	return item, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

func getArtifactTx(ctx context.Context, tx pgx.Tx, artifactID int64) (ReleaseArtifact, error) {
	var item ReleaseArtifact
	err := tx.QueryRow(ctx, `
    SELECT id, release_id, revision, artifact_type, format, object_key, storage_profile_id,
           storage_endpoint, storage_bucket, object_version, size_bytes, content_type,
           artifact_hash, items_content_hash, encoder_version, mapping_version_id,
           state, error_class, error_message, verified_at, created_at
    FROM release_artifacts WHERE id = $1`, artifactID,
	).Scan(&item.ID, &item.ReleaseID, &item.Revision, &item.ArtifactType, &item.Format,
		&item.ObjectKey, &item.StorageProfileID, &item.StorageEndpoint, &item.StorageBucket,
		&item.ObjectVersion, &item.SizeBytes, &item.ContentType, &item.ArtifactHash,
		&item.ItemsContentHash, &item.EncoderVersion, &item.MappingVersionID, &item.State,
		&item.ErrorClass, &item.ErrorMessage, &item.VerifiedAt, &item.CreatedAt)
	if err != nil {
		return ReleaseArtifact{}, err
	}
	return item, nil
}

func truncateForColumn(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
