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

// 本文件实现方案库的读写与「以方案创建项目」（Issue #160 T26）。
//
// 契约：sql/migrations/0032_studio_recipes.sql 的文件头（表结构取舍）、
// #160 T26 的原文要求。
//
// 三条与「复制出来的是不是用户选的那一份」直接相关的实现约定：
//
//  1. **只有 published 版本可被复制**。draft 是作者自己的工作中间态；
//     允许复制 draft 会让「同事复制了我还没验证完的配置」成为常态，
//     而那种错误会以一批结构不对的样本形式出现在别人的项目里。
//  2. **复制在同一事务内写入五类文档**。项目与内容一起成功或一起失败 ——
//     否则会留下一个有项目壳但缺配置的状态（看起来正常，直到点「开始试制」）。
//  3. **可见性在服务端判定**：`private` 只有创建者可见（**包括工作区管理员**）。
//     T03 明确「workspace admin 不默认拥有所有项目内容读权」，方案同理。

// ErrRecipeNotFound 表示方案不存在（或对当前用户不可见）。
//
// 「不存在」与「不可见」用同一个错误：对不可见者确认「它存在」本身就是
// 信息泄露（与项目权限的资源隐藏型 404 同一原则）。
var ErrRecipeNotFound = errors.New("未找到该方案")

// ErrRecipeVersionNotFound 表示方案版本不存在。
var ErrRecipeVersionNotFound = errors.New("未找到该方案版本")

// ErrRecipeVersionNotPublished 表示该版本尚未发布，不能用于创建项目。
var ErrRecipeVersionNotPublished = errors.New("该方案版本尚未发布：只能复制已发布的版本（草稿是作者的工作中间态）")

// RecipeStore 提供方案的读写。
type RecipeStore struct {
	db       *pgxpool.Pool
	projects *ProjectStore
}

// NewRecipeStore 构造方案 store。
func NewRecipeStore(db *pgxpool.Pool) *RecipeStore {
	return &RecipeStore{db: db, projects: NewProjectStore(db)}
}

const recipeColumns = `id, workspace_id, name, name_key, description, target_kind, visibility,
  applicable_scope, limitations, created_by, created_at, updated_at`

// CreateRecipe 创建方案与它的第一个版本。
func (s *RecipeStore) CreateRecipe(ctx context.Context, workspaceID, actorID int64, input model.CreateRecipeInput, publish bool) (model.Recipe, model.RecipeVersion, error) {
	input.Normalize()
	if err := input.Validate(); err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	if workspaceID <= 0 {
		return model.Recipe{}, model.RecipeVersion{}, &apiStoreError{Message: "创建工作区不存在"}
	}

	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	contentHash, err := model.ContentHash(input.Payload)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	limitations, err := json.Marshal(input.Limitations)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var recipe model.Recipe
	var createdLimitations []byte
	err = tx.QueryRow(ctx, `
    INSERT INTO recipes
      (workspace_id, name, name_key, description, target_kind, visibility,
       applicable_scope, limitations, created_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
    RETURNING `+recipeColumns,
		workspaceID, input.Name, model.NormalizeRecipeNameKey(input.Name), input.Description,
		input.TargetKind, input.Visibility, input.ApplicableScope, limitations, actorID,
	).Scan(&recipe.ID, &recipe.WorkspaceID, &recipe.Name, &recipe.NameKey, &recipe.Description,
		&recipe.TargetKind, &recipe.Visibility, &recipe.ApplicableScope, &createdLimitations, &recipe.CreatedBy,
		&recipe.CreatedAt, &recipe.UpdatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return model.Recipe{}, model.RecipeVersion{}, model.FieldErrors{{Field: "name",
				Message: "同名方案已存在（名称比较忽略大小写与多余空白）"}}
		}
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	recipe.Limitations = decodeLimitations(createdLimitations)

	var version model.RecipeVersion
	var createdPayload []byte
	err = tx.QueryRow(ctx, `
    INSERT INTO recipe_versions
      (recipe_id, workspace_id, version, status, payload, content_hash, change_reason, published_at, created_by)
    VALUES ($1, $2, 1, $3, $4, $5, $6, CASE WHEN $3 = 'published' THEN NOW() ELSE NULL END, $7)
    RETURNING id, recipe_id, workspace_id, version, status, payload, content_hash, change_reason,
              published_at, created_by, created_at`,
		recipe.ID, workspaceID, recipeVersionStatus(publish), payloadJSON, contentHash,
		strings.TrimSpace(input.ChangeReason), actorID,
	).Scan(&version.ID, &version.RecipeID, &version.WorkspaceID, &version.Version, &version.Status,
		&createdPayload, &version.ContentHash, &version.ChangeReason,
		&version.PublishedAt, &version.CreatedBy, &version.CreatedAt)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	version.Payload = input.Payload
	if len(createdPayload) == 0 {
		return model.Recipe{}, model.RecipeVersion{}, errors.New("方案版本写入后回读为空")
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "recipe_create", Resource: "recipe",
		ResourceID: fmt.Sprint(recipe.ID), WorkspaceID: workspaceID,
		Reason: fmt.Sprintf("name=%s targetKind=%s visibility=%s", recipe.Name, recipe.TargetKind, recipe.Visibility),
	}); err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if IsUniqueViolation(err) {
			return model.Recipe{}, model.RecipeVersion{}, model.FieldErrors{{Field: "name",
				Message: "同名方案已存在（名称比较忽略大小写与多余空白）"}}
		}
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	recipe.LatestVersion = 1
	recipe.VersionCount = 1
	if publish {
		recipe.PublishedVersion = 1
	}
	return recipe, version, nil
}

// SaveRecipeVersion 追加一个方案版本。
func (s *RecipeStore) SaveRecipeVersion(ctx context.Context, recipeID, actorID int64, input model.SaveRecipeVersionInput) (model.RecipeVersion, error) {
	recipe, err := s.GetRecipeInternal(ctx, recipeID)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	input.Payload.SchemaVersion = firstNonEmpty(input.Payload.SchemaVersion, model.RecipePayloadSchemaVersion)
	if err := input.Payload.Validate(); err != nil {
		return model.RecipeVersion{}, err
	}
	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	contentHash, err := model.ContentHash(input.Payload)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	reason := strings.TrimSpace(input.ChangeReason)
	if reason == "" {
		return model.RecipeVersion{}, model.FieldErrors{{Field: "changeReason",
			Message: "必填：方案版本的变更理由会展示给使用者，缺它无法判断该选哪一版"}}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version model.RecipeVersion
	var rawPayload []byte
	err = tx.QueryRow(ctx, `
    INSERT INTO recipe_versions
      (recipe_id, workspace_id, version, status, payload, content_hash, change_reason, published_at, created_by)
    VALUES ($1, $2,
            (SELECT COALESCE(MAX(version), 0) + 1 FROM recipe_versions WHERE recipe_id = $1),
            $3, $4, $5, $6, CASE WHEN $3 = 'published' THEN NOW() ELSE NULL END, $7)
    RETURNING id, recipe_id, workspace_id, version, status, payload, content_hash, change_reason,
              published_at, created_by, created_at`,
		recipeID, recipe.WorkspaceID, recipeVersionStatus(input.Publish), payloadJSON, contentHash, reason, actorID,
	).Scan(&version.ID, &version.RecipeID, &version.WorkspaceID, &version.Version, &version.Status,
		&rawPayload, &version.ContentHash, &version.ChangeReason,
		&version.PublishedAt, &version.CreatedBy, &version.CreatedAt)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE recipes SET updated_at = NOW() WHERE id = $1`, recipeID); err != nil {
		return model.RecipeVersion{}, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "recipe_version_created", Resource: "recipe_version",
		ResourceID: fmt.Sprint(version.ID), WorkspaceID: recipe.WorkspaceID,
		Reason: reason,
	}); err != nil {
		return model.RecipeVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.RecipeVersion{}, err
	}
	version.Payload = input.Payload
	return version, nil
}

// PublishRecipeVersion 把某个版本标为 published。
//
// 发布是**显式动作**而不是保存时的副作用：它表达「作者认为这一版可以给别人用」，
// 而这个判断不能由系统代做。
func (s *RecipeStore) PublishRecipeVersion(ctx context.Context, recipeID int64, version int, actorID int64) (model.RecipeVersion, error) {
	recipe, err := s.GetRecipeInternal(ctx, recipeID)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var updated model.RecipeVersion
	var rawPayload []byte
	err = tx.QueryRow(ctx, `
    UPDATE recipe_versions
    SET status = 'published', published_at = COALESCE(published_at, NOW())
    WHERE recipe_id = $1 AND version = $2
    RETURNING id, recipe_id, workspace_id, version, status, payload, content_hash, change_reason,
              published_at, created_by, created_at`,
		recipeID, version,
	).Scan(&updated.ID, &updated.RecipeID, &updated.WorkspaceID, &updated.Version, &updated.Status,
		&rawPayload, &updated.ContentHash, &updated.ChangeReason, &updated.PublishedAt,
		&updated.CreatedBy, &updated.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.RecipeVersion{}, ErrRecipeVersionNotFound
	}
	if err != nil {
		return model.RecipeVersion{}, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "recipe_version_published", Resource: "recipe_version",
		ResourceID: fmt.Sprint(updated.ID), WorkspaceID: recipe.WorkspaceID,
		Reason: fmt.Sprintf("recipeId=%d version=%d", recipeID, version),
	}); err != nil {
		return model.RecipeVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.RecipeVersion{}, err
	}
	payload, err := model.DecodeRecipePayload(rawPayload)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	updated.Payload = payload
	return updated, nil
}

// ListRecipes 列出用户可见的方案。
//
// visibleTo：`private` 只有创建者可见；`workspace` 需要是工作区成员。
// 用一条 SQL 表达而不是「先查全部再在 Go 里过滤」：后者在方案变多以后
// 会把用户不可见的内容读进内存（也读进了进程的日志上下文）。
func (s *RecipeStore) ListRecipes(ctx context.Context, workspaceID, userID int64, targetKind string, limit int) ([]model.Recipe, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT r.id, r.workspace_id, r.name, r.name_key, r.description, r.target_kind, r.visibility,
           r.applicable_scope, r.limitations, r.created_by, r.created_at, r.updated_at,
           COALESCE(v.latest_version, 0), COALESCE(v.published_version, 0), COALESCE(v.version_count, 0),
           lv.payload
    FROM recipes r
    LEFT JOIN LATERAL (
      SELECT MAX(version) AS latest_version,
             COALESCE(MAX(version) FILTER (WHERE status = 'published'), 0) AS published_version,
             COUNT(*) AS version_count
      FROM recipe_versions WHERE recipe_id = r.id
    ) v ON TRUE
    LEFT JOIN LATERAL (
      SELECT payload FROM recipe_versions WHERE recipe_id = r.id ORDER BY version DESC LIMIT 1
    ) lv ON TRUE
    WHERE r.workspace_id = $1
      AND ($2 = '' OR r.target_kind = $2)
      AND (r.visibility = 'workspace' AND EXISTS (
             SELECT 1 FROM workspace_members m
             WHERE m.workspace_id = r.workspace_id AND m.user_id = $3)
           OR r.created_by = $3)
    ORDER BY r.updated_at DESC, r.id DESC
    LIMIT $4`, workspaceID, strings.TrimSpace(targetKind), userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	recipes := []model.Recipe{}
	for rows.Next() {
		recipe, err := scanRecipeWithCounts(rows)
		if err != nil {
			return nil, err
		}
		recipes = append(recipes, recipe)
	}
	return recipes, rows.Err()
}

// GetRecipe 读取用户可见的方案。
func (s *RecipeStore) GetRecipe(ctx context.Context, recipeID, userID int64) (model.Recipe, error) {
	recipe, err := s.GetRecipeInternal(ctx, recipeID)
	if err != nil {
		return model.Recipe{}, err
	}
	visible, err := s.RecipeVisibleTo(ctx, recipe, userID)
	if err != nil {
		return model.Recipe{}, err
	}
	if !visible {
		return model.Recipe{}, ErrRecipeNotFound
	}
	return recipe, nil
}

// GetRecipeInternal 读取方案（**不做可见性判定**）。
//
// 供 store 内部（保存版本、发布）使用；对外一律走 GetRecipe。
// 命名带 Internal 是为了让「忘了判可见性」在 code review 里可见。
func (s *RecipeStore) GetRecipeInternal(ctx context.Context, recipeID int64) (model.Recipe, error) {
	row := s.db.QueryRow(ctx, `
    SELECT r.id, r.workspace_id, r.name, r.name_key, r.description, r.target_kind, r.visibility,
           r.applicable_scope, r.limitations, r.created_by, r.created_at, r.updated_at,
           COALESCE(v.latest_version, 0), COALESCE(v.published_version, 0), COALESCE(v.version_count, 0),
           lv.payload
    FROM recipes r
    LEFT JOIN LATERAL (
      SELECT MAX(version) AS latest_version,
             COALESCE(MAX(version) FILTER (WHERE status = 'published'), 0) AS published_version,
             COUNT(*) AS version_count
      FROM recipe_versions WHERE recipe_id = r.id
    ) v ON TRUE
    LEFT JOIN LATERAL (
      SELECT payload FROM recipe_versions WHERE recipe_id = r.id ORDER BY version DESC LIMIT 1
    ) lv ON TRUE
    WHERE r.id = $1`, recipeID)
	recipe, err := scanRecipeWithCounts(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Recipe{}, ErrRecipeNotFound
	}
	return recipe, err
}

// RecipeVisibleTo 判定方案对某用户是否可见。
//
// private 对**工作区管理员也不可见**：T03 明确「workspace admin 不默认拥有
// 所有项目内容读权」，方案是同级的业务内容。管理员的处置路径是让创建者
// 自己改可见范围，而不是绕过它。
func (s *RecipeStore) RecipeVisibleTo(ctx context.Context, recipe model.Recipe, userID int64) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	if recipe.CreatedBy != nil && *recipe.CreatedBy == userID {
		return true, nil
	}
	if recipe.Visibility == model.RecipeVisibilityPrivate {
		return false, nil
	}
	_, isMember, err := s.projects.WorkspaceRole(ctx, recipe.WorkspaceID, userID)
	if err != nil {
		return false, err
	}
	return isMember, nil
}

// ListRecipeVersions 列出方案的版本（倒序）。
func (s *RecipeStore) ListRecipeVersions(ctx context.Context, recipeID int64, limit int) ([]model.RecipeVersion, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, recipe_id, workspace_id, version, status, payload, content_hash, change_reason,
           published_at, created_by, created_at
    FROM recipe_versions WHERE recipe_id = $1
    ORDER BY version DESC LIMIT $2`, recipeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []model.RecipeVersion{}
	for rows.Next() {
		version, err := scanRecipeVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// RecipeVersionForCopy 读取可用于创建项目的方案版本。
//
// 三条拒绝：不可见（ErrRecipeNotFound）、不存在（ErrRecipeVersionNotFound）、
// 未发布（ErrRecipeVersionNotPublished）。
func (s *RecipeStore) RecipeVersionForCopy(ctx context.Context, versionID, userID int64) (model.Recipe, model.RecipeVersion, error) {
	recipe, version, err := s.GetRecipeVersionByID(ctx, versionID)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	visible, err := s.RecipeVisibleTo(ctx, recipe, userID)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	if !visible {
		return model.Recipe{}, model.RecipeVersion{}, ErrRecipeNotFound
	}
	if version.Status != model.RecipeVersionPublished {
		return model.Recipe{}, model.RecipeVersion{}, ErrRecipeVersionNotPublished
	}
	return recipe, version, nil
}

// GetRecipeVersionByID 按版本 ID 读取（含所属方案）。
func (s *RecipeStore) GetRecipeVersionByID(ctx context.Context, versionID int64) (model.Recipe, model.RecipeVersion, error) {
	row := s.db.QueryRow(ctx, `
    SELECT id, recipe_id, workspace_id, version, status, payload, content_hash, change_reason,
           published_at, created_by, created_at
    FROM recipe_versions WHERE id = $1`, versionID)
	version, err := scanRecipeVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Recipe{}, model.RecipeVersion{}, ErrRecipeVersionNotFound
	}
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	recipe, err := s.GetRecipeInternal(ctx, version.RecipeID)
	if err != nil {
		return model.Recipe{}, model.RecipeVersion{}, err
	}
	return recipe, version, nil
}

// CreateProjectFromRecipe 以方案创建项目：**同一事务**写入项目与五类文档。
//
// 这是 T26 的核心验收项「新项目值确实来自选定版本，而不只是 URL 带 recipe 参数」：
// 复制的是方案版本里的**内容快照**，复制完成后项目与方案再无写入关系
// （只保留 source_recipe_version_id 用于追溯），因此「复制后编辑不影响方案/其它项目」
// 与「方案升级只影响未来复制」都是结构性事实。
func (s *RecipeStore) CreateProjectFromRecipe(ctx context.Context, workspaceID, actorID int64,
	input model.CreateProjectInput, versionID int64) (model.Project, model.RecipeCopyResult, error) {
	recipe, version, err := s.RecipeVersionForCopy(ctx, versionID, actorID)
	if err != nil {
		return model.Project{}, model.RecipeCopyResult{}, err
	}
	input.Normalize()
	if err := input.Validate(); err != nil {
		return model.Project{}, model.RecipeCopyResult{}, err
	}
	// GRPO/SFT 不错配（T26 验收项）：把 SFT 的节点配置复制进 GRPO 项目
	// 会产出一批结构错误的样本，而错误只在生成时才暴露。
	if !model.RecipeTargetMatches(recipe.TargetKind, input.TargetKind) {
		return model.Project{}, model.RecipeCopyResult{}, model.FieldErrors{{Field: "targetKind",
			Message: fmt.Sprintf("方案的适用类型是 %s，与项目的目标类型 %s 不一致，"+
				"请选择匹配的方案或调整目标类型", recipe.TargetKind, input.TargetKind)}}
	}
	input.SourceRecipeVersionID = &versionID

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Project{}, model.RecipeCopyResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	project, err := createProjectTx(ctx, tx, workspaceID, actorID, input)
	if err != nil {
		return model.Project{}, model.RecipeCopyResult{}, err
	}
	if err := insertProjectOwnerTx(ctx, tx, project.ID, actorID); err != nil {
		return model.Project{}, model.RecipeCopyResult{}, err
	}

	result := model.RecipeCopyResult{
		RecipeID: recipe.ID, RecipeVersionID: version.ID, RecipeName: recipe.Name,
		Version: version.Version, Limitations: append([]string{}, recipe.Limitations...),
		CopiedDocuments: []string{},
	}
	changeReason := fmt.Sprintf("从方案「%s」v%d 复制（recipeVersionId=%d）", recipe.Name, version.Version, version.ID)
	// copiedVersionID 记录「本次复制在新项目里产生了哪些版本行」：
	// 蓝图的节点引用必须重映射到它们（见下面的说明）。
	copiedVersionID := map[model.DocumentKind]int64{}
	for _, entry := range version.Payload.Documents() {
		payload := entry.Payload
		if entry.Kind == model.KindBlueprint {
			blueprint, ok := payload.(model.BlueprintPayload)
			if !ok {
				return model.Project{}, model.RecipeCopyResult{}, fmt.Errorf("蓝图内容格式不正确")
			}
			// **重映射版本引用**（T26 的核心：复制的是配置，不是一堆文本）：
			// 来源蓝图里的 coverageVersionId/standardVersionId/qualityPolicyVersionId/
			// mappingVersionId 指向**来源项目**的版本行。原样带过来会两选一：
			// 要么违反「引用必须同项目」的复合外键（复制失败），
			// 要么指向不属于本项目的版本（看起来复制成功，其实引用是错的）。
			blueprint.Nodes.Coverage.CoverageVersionID = copiedVersionID[model.KindCoverage]
			blueprint.Nodes.Standard.StandardVersionID = copiedVersionID[model.KindStandard]
			blueprint.Nodes.Rules.QualityPolicyVersionID = copiedVersionID[model.KindQualityPolicy]
			blueprint.Nodes.Delivery.MappingVersionID = copiedVersionID[model.KindMapping]
			// rubric 版本在本轮的五类文档里没有对应类型，因此无法重映射：
			// 清空并在结果里标记，让界面提示「需要重新选择」。
			if blueprint.Nodes.Evaluation.RubricVersionID != 0 {
				blueprint.Nodes.Evaluation.RubricVersionID = 0
				result.ClearedRubricVersion = true
			}
			payload = blueprint
		}
		_, created, err := saveVersionTx(ctx, tx, project.ID, entry.Kind, actorID, SaveDocumentVersionInput{
			ChangeReason: changeReason,
			Payload:      payload,
		})
		if err != nil {
			return model.Project{}, model.RecipeCopyResult{}, fmt.Errorf("复制文档 %s 失败：%w", entry.Kind, err)
		}
		copiedVersionID[entry.Kind] = created.ID
		result.CopiedDocuments = append(result.CopiedDocuments, string(entry.Kind))
	}

	// 连接可用性：方案里的蓝图引用的是**非秘密连接 ID**（凭证不复制）。
	// 连接不存在或已停用时标记出来，让界面提示「需要绑定连接」，
	// 而不是让项目看起来配置齐全、直到开始试制才失败。
	if version.Payload.Blueprint != nil {
		connectionID := version.Payload.Blueprint.Nodes.Generation.ModelConnectionID
		if connectionID > 0 {
			var usable bool
			if err := tx.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM model_providers WHERE id = $1 AND is_active)`,
				connectionID).Scan(&usable); err != nil {
				return model.Project{}, model.RecipeCopyResult{}, err
			}
			result.UnboundModelConnection = !usable
		}
		// 裁判连接同理：连接被停用/删除时标记出来，而不是让质量实验
		// 在运行时才报「裁判连接不可用」。
		for _, judgeConnectionID := range version.Payload.Blueprint.Nodes.Evaluation.JudgeConnectionIDs {
			if judgeConnectionID <= 0 {
				continue
			}
			var usable bool
			if err := tx.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM model_providers WHERE id = $1 AND is_active)`,
				judgeConnectionID).Scan(&usable); err != nil {
				return model.Project{}, model.RecipeCopyResult{}, err
			}
			if !usable {
				result.UnboundJudgeConnections++
			}
		}
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "project_created_from_recipe", Resource: "project",
		ResourceID: fmt.Sprint(project.ID), ProjectID: project.ID,
		Reason: fmt.Sprintf("recipeId=%d recipeVersionId=%d version=%d docs=%s",
			recipe.ID, version.ID, version.Version, strings.Join(result.CopiedDocuments, ",")),
	}); err != nil {
		return model.Project{}, model.RecipeCopyResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		if IsUniqueViolation(err) {
			return model.Project{}, model.RecipeCopyResult{}, model.FieldErrors{{Field: "name",
				Message: "工作区里已存在同名项目"}}
		}
		return model.Project{}, model.RecipeCopyResult{}, err
	}
	return project, result, nil
}

// GetRecipeNameForVersion 返回方案版本对应的方案名（项目详情展示来源用）。
//
// 方案版本被删除后返回空串：调用方据此显示「来源方案已删除」，
// 而不是一个不存在的名字（迁移 0032 用 ON DELETE SET NULL 保留项目）。
func (s *RecipeStore) GetRecipeNameForVersion(ctx context.Context, versionID int64) (string, int, error) {
	var name string
	var version int
	err := s.db.QueryRow(ctx, `
    SELECT r.name, v.version FROM recipe_versions v
    JOIN recipes r ON r.id = v.recipe_id WHERE v.id = $1`, versionID).Scan(&name, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	return name, version, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// recipeVersionStatus 把 publish 语义翻译成表里的 status。
func recipeVersionStatus(publish bool) string {
	if publish {
		return model.RecipeVersionPublished
	}
	return model.RecipeVersionDraft
}

func scanRecipeWithCounts(row pgx.Row) (model.Recipe, error) {
	var recipe model.Recipe
	var limitations []byte
	// latestPayload 用于派生「整套方法」摘要；为 NULL（还没有版本）时保持 nil。
	var latestPayload []byte
	err := row.Scan(&recipe.ID, &recipe.WorkspaceID, &recipe.Name, &recipe.NameKey, &recipe.Description,
		&recipe.TargetKind, &recipe.Visibility, &recipe.ApplicableScope, &limitations, &recipe.CreatedBy,
		&recipe.CreatedAt, &recipe.UpdatedAt, &recipe.LatestVersion, &recipe.PublishedVersion,
		&recipe.VersionCount, &latestPayload)
	if err != nil {
		return model.Recipe{}, err
	}
	recipe.Limitations = decodeLimitations(limitations)
	// 解析失败**不报错**：摘要只是展示信息，让一份坏 payload 把整个方案列表
	// 读不出来是更差的取舍（那时用户连方案名都看不到）。
	if payload, decodeErr := model.DecodeRecipePayload(latestPayload); decodeErr == nil {
		summary := payload.Summarize()
		recipe.Summary = &summary
	}
	return recipe, nil
}

func scanRecipeVersion(row pgx.Row) (model.RecipeVersion, error) {
	var version model.RecipeVersion
	var rawPayload []byte
	err := row.Scan(&version.ID, &version.RecipeID, &version.WorkspaceID, &version.Version, &version.Status,
		&rawPayload, &version.ContentHash, &version.ChangeReason, &version.PublishedAt,
		&version.CreatedBy, &version.CreatedAt)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	payload, err := model.DecodeRecipePayload(rawPayload)
	if err != nil {
		return model.RecipeVersion{}, err
	}
	version.Payload = payload
	return version, nil
}

// decodeLimitations 解析方案的 limitations JSONB。
//
// 解不开时返回空列表：限制说明是展示信息，让它把一个方案整个读不出来
// 是更差的取舍（用户在列表里看不到这个方案，而原因与方案无关）。
func decodeLimitations(raw []byte) []string {
	limitations := []string{}
	if len(raw) == 0 {
		return limitations
	}
	if err := json.Unmarshal(raw, &limitations); err != nil {
		return []string{}
	}
	return limitations
}
