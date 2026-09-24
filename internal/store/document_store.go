package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 五类版本化文档与乐观锁（Issue #160 T04）。
//
// 契约：docs/plans/atelier-implementation.md §4.1、§4.3、§5；
// docs/plans/atelier-api-contract.md §2.2。
//
// 不变式：
//  1. **保存产生新版本**，旧版本只读，可读/比较/复制（§2.2）。
//  2. **expectedRevision 不匹配返回冲突**（409），且不写任何行。
//  3. 并发保存由 `UNIQUE (document_id, version)` 收敛：两个编辑者同时保存时
//     恰有一个成功。应用层的 SELECT-then-INSERT 无法保证这一点。
//  4. 引用必须**同项目**（迁移 0024 的复合外键强制）。
//  5. 内容 hash 在写入时对规范化 JSON 计算一次并存储（§2.2）。

// ErrRevisionConflict 表示乐观锁冲突。
//
// 单独一个哨兵错误：handler 必须把它映射成 409 而不是 500，并且要能
// 告知前端「版本已变化，请重新加载后再保存」（契约 §1.2 的原文）。
// 用文本匹配来找这个分支是脆弱的，而这里恰好是用户最容易遇到的冲突路径。
var ErrRevisionConflict = errors.New("版本已变化，请重新加载后再保存")

// ErrDocumentKindMismatch 表示请求的文档类型与目标不符。
var ErrDocumentKindMismatch = errors.New("文档类型不一致，请刷新页面后重试")

// VersionedDocument 是文档头 + 当前采用版本。
type VersionedDocument struct {
	ID             int64              `json:"id"`
	ProjectID      int64              `json:"projectId"`
	Kind           model.DocumentKind `json:"kind"`
	LogicalID      string             `json:"logicalId"`
	CurrentVersion int                `json:"currentVersion"`
	// RowVersion 是 OPTIMISTIC LOCK 的版本号（契约 §4.3 的 expectedRevision）。
	RowVersion int64            `json:"revision"`
	CreatedBy  *int64           `json:"createdBy,omitempty"`
	CreatedAt  time.Time        `json:"createdAt"`
	UpdatedAt  time.Time        `json:"updatedAt"`
	Current    *DocumentVersion `json:"current,omitempty"`
}

// DocumentVersion 是一个不可变版本。
type DocumentVersion struct {
	ID            int64              `json:"id"`
	DocumentID    int64              `json:"documentId"`
	ProjectID     int64              `json:"projectId"`
	Kind          model.DocumentKind `json:"kind"`
	Version       int                `json:"version"`
	SchemaVersion string             `json:"schemaVersion"`
	Payload       json.RawMessage    `json:"payload"`
	ContentHash   string             `json:"contentHash"`
	ChangeReason  string             `json:"changeReason"`
	CreatedBy     *int64             `json:"createdBy,omitempty"`
	CreatedAt     time.Time          `json:"createdAt"`
}

// SaveDocumentVersionInput 是保存新版本的请求。
//
// ExpectedRevision 来自请求体（契约 §2.2）。它的语义是「我基于哪一次头记录状态编辑」，
// 而不是「上一版版本号」—— 因为「头记录被改过」涵盖的变更比「产生了新版本」更多
// （例如将来切换当前采用版本，那也改了头记录）。
type SaveDocumentVersionInput struct {
	ExpectedRevision int64
	LogicalID        string
	ChangeReason     string
	// Payload 是已经过 typed 校验的文档内容。
	Payload any
}

// DefaultLogicalID 是逻辑文档的默认标识（契约 §2.2 的请求体默认值）。
const DefaultLogicalID = "main"

// maxChangeReasonLength 限制变更理由长度。
// 理由会写进版本历史并展示给协作者，不限长会让一次请求写入任意大小的一行。
const maxChangeReasonLength = 500

type DocumentStore struct {
	db *pgxpool.Pool
}

func NewDocumentStore(db *pgxpool.Pool) *DocumentStore {
	return &DocumentStore{db: db}
}

// BootstrapProjectDocuments creates the five native Atelier documents for a
// project that was created without a recipe. It is idempotent: existing
// versions are preserved and only missing document heads are initialized.
// Keeping this in the document store makes the bootstrap atomic, so a project
// never presents a half-created set of references after a failed request.
func (s *DocumentStore) BootstrapProjectDocuments(ctx context.Context, projectID, actorID int64, targetKind, name, goal string) error {
	coverage, standard, quality, mapping, blueprint := model.DefaultProjectDocuments(targetKind, name, goal)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ids := map[model.DocumentKind]int64{}
	for _, entry := range []struct {
		kind    model.DocumentKind
		payload any
	}{{model.KindCoverage, coverage}, {model.KindStandard, standard}, {model.KindQualityPolicy, quality}, {model.KindMapping, mapping}} {
		id, exists, err := currentDocumentVersionIDTx(ctx, tx, projectID, entry.kind, DefaultLogicalID)
		if err != nil {
			return err
		}
		if !exists {
			_, version, err := saveVersionTx(ctx, tx, projectID, entry.kind, actorID, SaveDocumentVersionInput{
				ChangeReason: "项目创建：初始化 Atelier 配置",
				Payload:      entry.payload,
			})
			if err != nil {
				return err
			}
			id = version.ID
		}
		ids[entry.kind] = id
	}

	blueprint.Nodes.Coverage.CoverageVersionID = ids[model.KindCoverage]
	blueprint.Nodes.Standard.StandardVersionID = ids[model.KindStandard]
	blueprint.Nodes.Rules.QualityPolicyVersionID = ids[model.KindQualityPolicy]
	blueprint.Nodes.Delivery.MappingVersionID = ids[model.KindMapping]
	if id, exists, err := currentDocumentVersionIDTx(ctx, tx, projectID, model.KindBlueprint, DefaultLogicalID); err != nil {
		return err
	} else if !exists {
		if _, _, err := saveVersionTx(ctx, tx, projectID, model.KindBlueprint, actorID, SaveDocumentVersionInput{
			ChangeReason: "项目创建：初始化 Atelier 生产蓝图",
			Payload:      blueprint,
		}); err != nil {
			return err
		}
	} else if id == 0 {
		return errors.New("蓝图文档状态无效")
	}

	return tx.Commit(ctx)
}

func currentDocumentVersionIDTx(ctx context.Context, tx pgx.Tx, projectID int64, kind model.DocumentKind, logicalID string) (int64, bool, error) {
	var versionID int64
	err := tx.QueryRow(ctx, `
    SELECT v.id
    FROM versioned_documents d
    JOIN document_versions v ON v.document_id = d.id AND v.version = d.current_version
    WHERE d.project_id = $1 AND d.kind = $2 AND d.logical_id = $3`,
		projectID, string(kind), logicalID).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return versionID, true, nil
}

// validatePayloadForKind 按类型分派到 typed 校验并返回 schema 版本。
//
// 分派集中在一处：新增文档类型时编译器会强制在这里加一个分支（switch 无 default
// 且返回错误），而不是让某个调用点静默跳过校验。
func validatePayloadForKind(kind model.DocumentKind, payload any) (string, error) {
	switch kind {
	case model.KindBlueprint:
		typed, ok := payload.(model.BlueprintPayload)
		if !ok {
			return "", &apiStoreError{Message: "蓝图内容格式不正确"}
		}
		if err := model.ValidateBlueprintPayload(typed); err != nil {
			return "", err
		}
	case model.KindCoverage:
		typed, ok := payload.(model.CoveragePayload)
		if !ok {
			return "", &apiStoreError{Message: "覆盖计划内容格式不正确"}
		}
		if err := model.ValidateCoveragePayload(typed); err != nil {
			return "", err
		}
	case model.KindStandard:
		typed, ok := payload.(model.StandardPayload)
		if !ok {
			return "", &apiStoreError{Message: "思维标准内容格式不正确"}
		}
		if err := model.ValidateStandardPayload(typed); err != nil {
			return "", err
		}
	case model.KindQualityPolicy:
		typed, ok := payload.(model.QualityPolicyPayload)
		if !ok {
			return "", &apiStoreError{Message: "质量策略内容格式不正确"}
		}
		if err := model.ValidateQualityPolicyPayload(typed); err != nil {
			return "", err
		}
	case model.KindMapping:
		typed, ok := payload.(model.MappingPayload)
		if !ok {
			return "", &apiStoreError{Message: "字段映射内容格式不正确"}
		}
		if err := model.ValidateMappingPayload(typed); err != nil {
			return "", err
		}
	default:
		return "", &apiStoreError{Message: "不支持的文档类型"}
	}
	return model.SchemaVersionFor(kind), nil
}

// SaveVersion 保存一个新版本（追加式，不修改任何已有版本）。
//
// 事务内顺序：
//  1. 锁定/创建文档头（`INSERT ... ON CONFLICT DO NOTHING` + `SELECT ... FOR UPDATE`）；
//  2. 校验 expectedRevision（不匹配 → ErrRevisionConflict，事务回滚，不写任何行）；
//  3. 追加版本行（版本号由 DB 从 MAX(version)+1 计算，不是应用层读-改-写）；
//  4. 更新头记录的 current_version / row_version；
//  5. 重建该版本的引用边（删除旧边 + 插入新边），由复合外键强制同项目。
//  6. 写审计（同事务：变更与审计不可分离）。
//
// SaveVersion 保存一个新版本（在**自己的事务**里提交）。
func (s *DocumentStore) SaveVersion(ctx context.Context, projectID int64, kind model.DocumentKind, actorID int64, input SaveDocumentVersionInput) (VersionedDocument, DocumentVersion, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	document, version, err := saveVersionTx(ctx, tx, projectID, kind, actorID, input)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		// 提交阶段的唯一约束冲突与内部插入同源：并发保存的另一个编辑者赢了。
		if IsUniqueViolation(err) {
			return VersionedDocument{}, DocumentVersion{}, ErrRevisionConflict
		}
		return VersionedDocument{}, DocumentVersion{}, err
	}
	document.Current = &version
	return document, version, nil
}

// saveVersionTx 在**调用方的事务**里保存一个文档版本（不提交）。
//
// 抽出来的理由（T26）：以方案创建项目要在**同一个事务**里写入五类文档 ——
// 「项目建好了但方案里的蓝图没复制进来」是一个看起来正常、实则缺配置的项目，
// 而它只在用户点「开始试制」时才失败。分次提交做不到这一点。
func saveVersionTx(ctx context.Context, tx pgx.Tx, projectID int64, kind model.DocumentKind, actorID int64, input SaveDocumentVersionInput) (VersionedDocument, DocumentVersion, error) {
	if !model.IsValidDocumentKind(kind) {
		return VersionedDocument{}, DocumentVersion{}, &apiStoreError{Message: "不支持的文档类型"}
	}

	logicalID := strings.TrimSpace(input.LogicalID)
	if logicalID == "" {
		logicalID = DefaultLogicalID
	}
	if len([]rune(logicalID)) > 100 {
		return VersionedDocument{}, DocumentVersion{}, model.FieldErrors{
			{Field: "logicalId", Message: "不能超过 100 个字符"},
		}
	}

	reason := strings.TrimSpace(input.ChangeReason)
	if len([]rune(reason)) > maxChangeReasonLength {
		reason = string([]rune(reason)[:maxChangeReasonLength])
	}

	schemaVersion, err := validatePayloadForKind(kind, input.Payload)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}
	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}
	contentHash, err := model.ContentHash(input.Payload)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}

	// 事务由调用方提供（SaveVersion 或「以方案创建项目」）。

	// 步骤 1：确保文档头存在，并锁住它。
	//
	// 为什么用两条语句而不是 `INSERT ... ON CONFLICT DO UPDATE ... RETURNING`：
	// 后者在冲突时会**更新**行，也就是每次保存都隐式改动头记录（并且需要
	// 一个无意义的 SET 才能 RETURNING）。而这里需要的是「拿到行并锁住它」。
	tag, err := tx.Exec(ctx, `
    INSERT INTO versioned_documents (project_id, kind, logical_id, created_by)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (project_id, kind, logical_id) DO NOTHING`,
		projectID, string(kind), logicalID, actorID)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}
	_ = tag

	document, err := lockDocumentTx(ctx, tx, projectID, kind, logicalID)
	if err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}

	// 步骤 2：乐观锁。
	//
	// 语义细节：新建文档（current_version = 0）时，请求的 expectedRevision 允许是
	// 0（「我基于一个还不存在的文档编辑」）或 1（前端拿到刚创建的头的 row_version）。
	// 两者都接受是有意的 —— 严格只接受一个值会让「先建草稿再保存内容」这条
	// 常见流程在第一版就报冲突，而那不是用户理解的冲突。
	if document.CurrentVersion > 0 && input.ExpectedRevision != document.RowVersion {
		return VersionedDocument{}, DocumentVersion{}, ErrRevisionConflict
	}

	// 步骤 3：追加版本。版本号由 DB 计算，避免应用层读-改-写。
	var version DocumentVersion
	err = tx.QueryRow(ctx, `
    INSERT INTO document_versions
      (document_id, version, project_id, kind, schema_version, payload, content_hash, change_reason, created_by)
    VALUES ($1,
            (SELECT COALESCE(MAX(version), 0) + 1 FROM document_versions WHERE document_id = $1),
            $2, $3, $4, $5, $6, $7, $8)
    RETURNING id, document_id, project_id, kind, version, schema_version, payload, content_hash, change_reason, created_by, created_at`,
		document.ID, projectID, string(kind), schemaVersion, payloadJSON, contentHash, reason, actorID,
	).Scan(&version.ID, &version.DocumentID, &version.ProjectID, &version.Kind, &version.Version,
		&version.SchemaVersion, &version.Payload, &version.ContentHash, &version.ChangeReason,
		&version.CreatedBy, &version.CreatedAt)
	if err != nil {
		// 唯一约束冲突 = 另一个编辑者刚刚插入了同一个版本号。
		// 这是**期望的**并发收敛路径（恰有一个成功），不是服务端故障。
		if IsUniqueViolation(err) {
			return VersionedDocument{}, DocumentVersion{}, ErrRevisionConflict
		}
		return VersionedDocument{}, DocumentVersion{}, err
	}

	// 步骤 4：更新头记录（当前采用指针 + 乐观锁版本）。
	if err := tx.QueryRow(ctx, `
    UPDATE versioned_documents
    SET current_version = $2, row_version = row_version + 1, updated_at = NOW()
    WHERE id = $1
    RETURNING id, project_id, kind, logical_id, current_version, row_version, created_by, created_at, updated_at`,
		document.ID, version.Version,
	).Scan(&document.ID, &document.ProjectID, &document.Kind, &document.LogicalID,
		&document.CurrentVersion, &document.RowVersion, &document.CreatedBy,
		&document.CreatedAt, &document.UpdatedAt); err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}

	// 步骤 5：重建引用边。
	if err := rebuildDocumentReferencesTx(ctx, tx, kind, projectID, version.ID, input.Payload); err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}

	// 步骤 6：审计（同事务）。
	//
	// revision 记的是**保存后**的头记录版本号：审计要能回答「这次变更之后
	// 对象处于哪个 revision」，而不是「变更前」。
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:    actorID,
		Action:     string(kind) + "_version_created",
		Resource:   string(kind) + "_version",
		ResourceID: strconv.FormatInt(version.ID, 10),
		ProjectID:  projectID,
		Revision:   document.RowVersion,
		Reason:     reason,
		Detail:     fmt.Sprintf("logicalId=%s version=%d hash=%s", logicalID, version.Version, contentHash),
	}); err != nil {
		return VersionedDocument{}, DocumentVersion{}, err
	}

	return document, version, nil
}

// lockDocumentTx 读取并锁定文档头。
func lockDocumentTx(ctx context.Context, tx pgx.Tx, projectID int64, kind model.DocumentKind, logicalID string) (VersionedDocument, error) {
	var document VersionedDocument
	err := tx.QueryRow(ctx, `
    SELECT id, project_id, kind, logical_id, current_version, row_version, created_by, created_at, updated_at
    FROM versioned_documents
    WHERE project_id = $1 AND kind = $2 AND logical_id = $3
    FOR UPDATE`, projectID, string(kind), logicalID,
	).Scan(&document.ID, &document.ProjectID, &document.Kind, &document.LogicalID,
		&document.CurrentVersion, &document.RowVersion, &document.CreatedBy,
		&document.CreatedAt, &document.UpdatedAt)
	return document, err
}

// documentReference 是一条待写入的引用边。
type documentReference struct {
	NodeKey         string
	TargetVersionID int64
}

// rebuildDocumentReferencesTx 重建一个版本的引用边。
//
// 为什么「删除旧边再插入」而不是增量 diff：引用边是**该版本 payload 的投影**，
// 不是独立数据。版本不可变，因此它的边也应当一次性写定；增量 diff 会引入
// 「payload 与边不一致」的状态，而那种不一致正是边表要防的东西。
//
// 目标版本不存在或跨项目时，复合外键会拒绝（迁移 0024）。这里刻意**不**预检查：
// 数据库约束是唯一真相，预检查会多一次查询且仍不能防止竞态（目标被并发删除）。
// 调用方（handler）把外键冲突翻译成字段错误。
func rebuildDocumentReferencesTx(ctx context.Context, tx pgx.Tx, kind model.DocumentKind, projectID, versionID int64, payload any) error {
	if _, err := tx.Exec(ctx, `DELETE FROM document_version_references WHERE document_version_id = $1`, versionID); err != nil {
		return err
	}

	references := collectReferences(kind, payload)
	for _, reference := range references {
		if _, err := tx.Exec(ctx, `
      INSERT INTO document_version_references (document_version_id, project_id, node_key, target_version_id)
      VALUES ($1, $2, $3, $4)
      ON CONFLICT (document_version_id, node_key, target_version_id) DO NOTHING`,
			versionID, projectID, reference.NodeKey, reference.TargetVersionID); err != nil {
			if isForeignKeyViolation(err) {
				return model.FieldErrors{{
					Field:   "payload",
					Message: "引用的文档版本不存在或不属于本项目，请检查各节点的版本引用",
				}}
			}
			return err
		}
	}
	return nil
}

// collectReferences 从 typed payload 中提取引用边。
//
// 只有蓝图会引用其它文档（§5 的节点表）：覆盖/标准/质量/映射是叶子文档，
// 它们不引用别的版本。这个差异是契约决定的，不是实现简化 ——
// 因此这里的 switch 只有 blueprint 分支有内容。
func collectReferences(kind model.DocumentKind, payload any) []documentReference {
	if kind != model.KindBlueprint {
		return nil
	}
	blueprint, ok := payload.(model.BlueprintPayload)
	if !ok {
		return nil
	}

	references := []documentReference{}
	add := func(nodeKey string, targetID int64) {
		if targetID > 0 {
			references = append(references, documentReference{NodeKey: nodeKey, TargetVersionID: targetID})
		}
	}
	add(model.BlueprintNodeCoverage, blueprint.Nodes.Coverage.CoverageVersionID)
	add(model.BlueprintNodeStandard, blueprint.Nodes.Standard.StandardVersionID)
	add(model.BlueprintNodeRules, blueprint.Nodes.Rules.QualityPolicyVersionID)
	add(model.BlueprintNodeDelivery, blueprint.Nodes.Delivery.MappingVersionID)
	// 评估节点的 rubric 版本也走同一套引用检查（它必须同项目）。
	add(model.BlueprintNodeEvaluation, blueprint.Nodes.Evaluation.RubricVersionID)
	return references
}

// isForeignKeyViolation 判断错误是否为 Postgres 外键冲突（SQLSTATE 23503）。
func isForeignKeyViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23503"
	}
	return false
}

// GetDocument 读取文档头与当前采用版本。
func (s *DocumentStore) GetDocument(ctx context.Context, projectID int64, kind model.DocumentKind, logicalID string) (VersionedDocument, error) {
	if strings.TrimSpace(logicalID) == "" {
		logicalID = DefaultLogicalID
	}
	document, err := scanDocument(s.db.QueryRow(ctx, `
    SELECT id, project_id, kind, logical_id, current_version, row_version, created_by, created_at, updated_at
    FROM versioned_documents
    WHERE project_id = $1 AND kind = $2 AND logical_id = $3`,
		projectID, string(kind), logicalID))
	if err != nil {
		return VersionedDocument{}, err
	}
	if document.CurrentVersion > 0 {
		version, err := s.GetVersion(ctx, document.ID, document.CurrentVersion)
		if err != nil {
			return VersionedDocument{}, err
		}
		document.Current = &version
	}
	return document, nil
}

// GetVersion 按文档与版本号读取一个版本。
func (s *DocumentStore) GetVersion(ctx context.Context, documentID int64, version int) (DocumentVersion, error) {
	var item DocumentVersion
	err := s.db.QueryRow(ctx, `
    SELECT id, document_id, project_id, kind, version, schema_version, payload, content_hash,
           change_reason, created_by, created_at
    FROM document_versions WHERE document_id = $1 AND version = $2`,
		documentID, version,
	).Scan(&item.ID, &item.DocumentID, &item.ProjectID, &item.Kind, &item.Version,
		&item.SchemaVersion, &item.Payload, &item.ContentHash, &item.ChangeReason,
		&item.CreatedBy, &item.CreatedAt)
	return item, err
}

// GetVersionByID 按版本行 ID 读取（批次快照持有的是 ID 或 hash）。
func (s *DocumentStore) GetVersionByID(ctx context.Context, versionID int64) (DocumentVersion, error) {
	var item DocumentVersion
	err := s.db.QueryRow(ctx, `
    SELECT id, document_id, project_id, kind, version, schema_version, payload, content_hash,
           change_reason, created_by, created_at
    FROM document_versions WHERE id = $1`, versionID,
	).Scan(&item.ID, &item.DocumentID, &item.ProjectID, &item.Kind, &item.Version,
		&item.SchemaVersion, &item.Payload, &item.ContentHash, &item.ChangeReason,
		&item.CreatedBy, &item.CreatedAt)
	return item, err
}

// ListVersions 列出一个逻辑文档的全部版本（倒序）。
//
// 历史版本必须**可读**（§2.2 验收项「旧版本可读/比较/复制」），
// 因此这里不做「只返回最近 N 版」的裁剪；数量增长由分页在 T08 的读模型处理。
func (s *DocumentStore) ListVersions(ctx context.Context, projectID int64, kind model.DocumentKind, logicalID string, limit int) ([]DocumentVersion, error) {
	if strings.TrimSpace(logicalID) == "" {
		logicalID = DefaultLogicalID
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT v.id, v.document_id, v.project_id, v.kind, v.version, v.schema_version, v.payload,
           v.content_hash, v.change_reason, v.created_by, v.created_at
    FROM document_versions v
    JOIN versioned_documents d ON d.id = v.document_id
    WHERE d.project_id = $1 AND d.kind = $2 AND d.logical_id = $3
    ORDER BY v.version DESC
    LIMIT $4`, projectID, string(kind), logicalID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []DocumentVersion{}
	for rows.Next() {
		var item DocumentVersion
		if err := rows.Scan(&item.ID, &item.DocumentID, &item.ProjectID, &item.Kind, &item.Version,
			&item.SchemaVersion, &item.Payload, &item.ContentHash, &item.ChangeReason,
			&item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListDocuments 列出项目的全部逻辑文档（按类型与逻辑 ID）。
//
// 项目概览（T10）与蓝图节点检查器（T11）都需要「这个项目已有哪些版本化文档」，
// 而不是逐个类型发一次请求。
func (s *DocumentStore) ListDocuments(ctx context.Context, projectID int64) ([]VersionedDocument, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, project_id, kind, logical_id, current_version, row_version, created_by, created_at, updated_at
    FROM versioned_documents
    WHERE project_id = $1
    ORDER BY CASE kind
               WHEN 'coverage' THEN 0
               WHEN 'standard' THEN 1
               WHEN 'blueprint' THEN 2
               WHEN 'quality_policy' THEN 3
               ELSE 4
             END, logical_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []VersionedDocument{}
	for rows.Next() {
		document, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, document)
	}
	return items, rows.Err()
}

// DeleteDraftDocument 删除一个**从未产生版本**的逻辑文档头。
//
// 为什么只允许删除零版本的文档：§4.1 要求「版本不可变、历史可读」。
// 一个已经有版本的文档不能删除，否则引用它的批次快照会失效。
// 界面上「删除草稿方向不破坏被引用的版本」（T04 验收项）指的是
// **payload 内部**的方向条目，不是删除整个文档；条目级的删除只需保存新版本。
func (s *DocumentStore) DeleteDraftDocument(ctx context.Context, projectID int64, kind model.DocumentKind, logicalID string) error {
	_, err := s.db.Exec(ctx, `
    DELETE FROM versioned_documents
    WHERE project_id = $1 AND kind = $2 AND logical_id = $3 AND current_version = 0`,
		projectID, string(kind), logicalID)
	return err
}

// DocumentReferences 返回一个版本的全部引用边（T08 的读模型用）。
func (s *DocumentStore) DocumentReferences(ctx context.Context, versionID int64) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, `
    SELECT node_key, target_version_id FROM document_version_references
    WHERE document_version_id = $1 ORDER BY node_key`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	references := map[string]int64{}
	for rows.Next() {
		var nodeKey string
		var targetID int64
		if err := rows.Scan(&nodeKey, &targetID); err != nil {
			return nil, err
		}
		references[nodeKey] = targetID
	}
	return references, rows.Err()
}

// scanDocument 读取文档头（无当前版本）。
func scanDocument(row pgx.Row) (VersionedDocument, error) {
	var document VersionedDocument
	err := row.Scan(&document.ID, &document.ProjectID, &document.Kind, &document.LogicalID,
		&document.CurrentVersion, &document.RowVersion, &document.CreatedBy,
		&document.CreatedAt, &document.UpdatedAt)
	if err != nil {
		return VersionedDocument{}, err
	}
	return document, nil
}

// DocumentKindLabel 返回文档类型的中文名（错误文案与界面共用）。
func DocumentKindLabel(kind model.DocumentKind) string {
	switch kind {
	case model.KindBlueprint:
		return "生产蓝图"
	case model.KindCoverage:
		return "覆盖计划"
	case model.KindStandard:
		return "思维标准"
	case model.KindQualityPolicy:
		return "质量策略"
	case model.KindMapping:
		return "字段映射"
	default:
		return "文档"
	}
}
