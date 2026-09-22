package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 本文件定义方案（Recipe）的对象与判据（Issue #160 T26）。
//
// 契约：docs/plans/atelier-implementation.md §4.1（Recipe 对象）、§6.3（T26）、
// sql/migrations/0032_studio_recipes.sql 的文件头（表结构取舍）。
//
// 方案 = 一份可复用的**配置组合**：蓝图 + 覆盖 + 标准 + 质量策略 + 映射。
// 它的价值在于「把已经验证过的一套方法复制到新项目」，因此三条性质必须成立：
//
//  1. **内容快照而不是 ID 引用**：见迁移文件头。方案作者改内容不影响已复制出去的项目。
//  2. **适用类型独立**：SFT 与 GRPO 的节点/字段不同，错配会产出一批结构错误的样本。
//  3. **升级只影响未来复制**：方案发布新版本不改动任何已有项目（结构上如此 ——
//     复制是单向的写入，项目侧不持有对方案的引用，只有一个来源 ID 用于追溯）。

// 方案可见范围。
const (
	// RecipeVisibilityPrivate 只对创建者可见。
	//
	// 默认之外的一个独立取值存在的理由：未验证的方案被同事当成「公认做法」
	// 直接复制，是方案库最可能的伤害形态（一个错误的配置会被快速扩散）。
	RecipeVisibilityPrivate = "private"
	// RecipeVisibilityWorkspace 对工作区成员可读。
	RecipeVisibilityWorkspace = "workspace"
)

// 方案版本状态。
const (
	RecipeVersionDraft     = "draft"
	RecipeVersionPublished = "published"
)

// Recipe 是方案的身份（不含内容）。
type Recipe struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspaceId"`
	Name        string `json:"name"`
	// NameKey 是规范化唯一键（小写、去空白）。
	NameKey         string   `json:"nameKey"`
	Description     string   `json:"description"`
	TargetKind      string   `json:"targetKind"`
	Visibility      string   `json:"visibility"`
	ApplicableScope string   `json:"applicableScope"`
	Limitations     []string `json:"limitations"`

	// LatestVersion/PublishedVersion 是读模型字段（不落库在这一行）。
	LatestVersion    int `json:"latestVersion"`
	PublishedVersion int `json:"publishedVersion"`
	VersionCount     int `json:"versionCount"`

	CreatedBy *int64    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RecipeVersion 是一个不可变的方案版本。
type RecipeVersion struct {
	ID           int64         `json:"id"`
	RecipeID     int64         `json:"recipeId"`
	WorkspaceID  int64         `json:"workspaceId"`
	Version      int           `json:"version"`
	Status       string        `json:"status"`
	Payload      RecipePayload `json:"payload"`
	ContentHash  string        `json:"contentHash"`
	ChangeReason string        `json:"changeReason"`
	PublishedAt  *time.Time    `json:"publishedAt,omitempty"`
	CreatedBy    *int64        `json:"createdBy,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
}

// RecipePayload 是方案的组合内容：五类文档的**完整 payload 快照**。
//
// 用指针区分「这份方案包含该文档」与「该文档为空」：
// 一份方案可以只包含蓝图（先固化结构，其余留待项目里补），
// 而用零值表示「不包含」会让空 payload 与缺失无法区分。
type RecipePayload struct {
	SchemaVersion string                `json:"schemaVersion"`
	Blueprint     *BlueprintPayload     `json:"blueprint,omitempty"`
	Coverage      *CoveragePayload      `json:"coverage,omitempty"`
	Standard      *StandardPayload      `json:"standard,omitempty"`
	QualityPolicy *QualityPolicyPayload `json:"qualityPolicy,omitempty"`
	Mapping       *MappingPayload       `json:"mapping,omitempty"`
}

// RecipePayloadSchemaVersion 是方案 payload 的 schema 版本。
const RecipePayloadSchemaVersion = "recipe.v1"

// DocumentEntry 是方案里的一份文档（复制到项目时按顺序写入）。
type DocumentEntry struct {
	Kind    DocumentKind
	Payload any
}

// Documents 返回方案里的全部文档（按**复制顺序**）。
//
// 顺序有依赖含义：**蓝图必须最后写**，因为它引用其余四份文档的版本行
// （覆盖/标准/质量策略/映射），而以方案创建项目需要把蓝图里的版本引用
// 重映射到新项目里的新版本行（T26）—— 先写蓝图就找不到可映射的目标。
func (payload RecipePayload) Documents() []DocumentEntry {
	entries := []DocumentEntry{}
	if payload.Coverage != nil {
		entries = append(entries, DocumentEntry{Kind: KindCoverage, Payload: *payload.Coverage})
	}
	if payload.Standard != nil {
		entries = append(entries, DocumentEntry{Kind: KindStandard, Payload: *payload.Standard})
	}
	if payload.QualityPolicy != nil {
		entries = append(entries, DocumentEntry{Kind: KindQualityPolicy, Payload: *payload.QualityPolicy})
	}
	if payload.Mapping != nil {
		entries = append(entries, DocumentEntry{Kind: KindMapping, Payload: *payload.Mapping})
	}
	if payload.Blueprint != nil {
		entries = append(entries, DocumentEntry{Kind: KindBlueprint, Payload: *payload.Blueprint})
	}
	return entries
}

// Validate 校验方案内容：至少一份文档，且每份文档自身合法。
//
// 为什么要逐份调用各自的校验器而不是只看 JSON 形状：
// 方案会被整份复制进新项目，而项目创建是「一次事务写入五类文档」。
// 若方案里存着一份不合法的蓝图，失败会发生在**复制时**（用户点「用方案建项目」
// 的那一刻），而那时错误信息离开方案详情的上下文，用户很难定位是哪份文档的问题。
func (payload RecipePayload) Validate() error {
	if payload.SchemaVersion == "" {
		payload.SchemaVersion = RecipePayloadSchemaVersion
	}
	if payload.SchemaVersion != RecipePayloadSchemaVersion {
		return FieldErrors{{Field: "payload.schemaVersion",
			Message: fmt.Sprintf("必须为 %s", RecipePayloadSchemaVersion)}}
	}
	entries := payload.Documents()
	if len(entries) == 0 {
		return FieldErrors{{Field: "payload",
			Message: "方案至少需要包含一份配置（覆盖/标准/质量策略/蓝图/映射）"}}
	}
	for _, entry := range entries {
		if err := validateDocumentPayload(entry.Kind, entry.Payload); err != nil {
			return FieldErrors{{Field: "payload." + string(entry.Kind), Message: err.Error()}}
		}
	}
	return nil
}

// validateDocumentPayload 按文档类型分派到各自的校验器。
//
// 与 store 的 validatePayloadForKind 同源（那边面向 any 做类型断言）；
// 这里面对的是已经 typed 的值，因此直接调用模型层的校验函数。
func validateDocumentPayload(kind DocumentKind, payload any) error {
	switch kind {
	case KindBlueprint:
		typed, ok := payload.(BlueprintPayload)
		if !ok {
			return fmt.Errorf("蓝图内容格式不正确")
		}
		return ValidateBlueprintPayload(typed)
	case KindCoverage:
		typed, ok := payload.(CoveragePayload)
		if !ok {
			return fmt.Errorf("覆盖计划内容格式不正确")
		}
		return ValidateCoveragePayload(typed)
	case KindStandard:
		typed, ok := payload.(StandardPayload)
		if !ok {
			return fmt.Errorf("思维标准内容格式不正确")
		}
		return ValidateStandardPayload(typed)
	case KindQualityPolicy:
		typed, ok := payload.(QualityPolicyPayload)
		if !ok {
			return fmt.Errorf("质量策略内容格式不正确")
		}
		return ValidateQualityPolicyPayload(typed)
	case KindMapping:
		typed, ok := payload.(MappingPayload)
		if !ok {
			return fmt.Errorf("字段映射内容格式不正确")
		}
		return ValidateMappingPayload(typed)
	default:
		return fmt.Errorf("不支持的文档类型 %s", kind)
	}
}

// ---------------------------------------------------------------------------
// 请求体规范化与校验
// ---------------------------------------------------------------------------

// CreateRecipeInput 是创建方案的请求。
type CreateRecipeInput struct {
	Name            string
	Description     string
	TargetKind      string
	Visibility      string
	ApplicableScope string
	Limitations     []string
	// ChangeReason 记在首个版本上。
	ChangeReason string
	Payload      RecipePayload
}

// Normalize 填默认值。
func (input *CreateRecipeInput) Normalize() {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.TargetKind = strings.ToLower(strings.TrimSpace(input.TargetKind))
	if input.TargetKind == "" {
		input.TargetKind = TargetKindSFT
	}
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if input.Visibility == "" {
		// 默认 workspace：方案库的主要价值是同事之间复用。
		// 需要「先自己验证」的场景显式选 private。
		input.Visibility = RecipeVisibilityWorkspace
	}
	input.ApplicableScope = strings.TrimSpace(input.ApplicableScope)
	if input.Payload.SchemaVersion == "" {
		input.Payload.SchemaVersion = RecipePayloadSchemaVersion
	}
	if input.Limitations == nil {
		input.Limitations = []string{}
	}
}

// Validate 校验创建方案的请求。
func (input CreateRecipeInput) Validate() error {
	var errs FieldErrors
	if err := validateRecipeName(input.Name); err != nil {
		errs = append(errs, err.(FieldErrors)...)
	}
	if len([]rune(input.Description)) > maxRecipeDescriptionLength {
		errs = append(errs, FieldError{Field: "description",
			Message: fmt.Sprintf("不能超过 %d 个字符", maxRecipeDescriptionLength)})
	}
	if input.TargetKind != TargetKindSFT && input.TargetKind != TargetKindGRPO {
		errs = append(errs, FieldError{Field: "targetKind", Message: "只能是 sft 或 grpo"})
	}
	if input.Visibility != RecipeVisibilityPrivate && input.Visibility != RecipeVisibilityWorkspace {
		errs = append(errs, FieldError{Field: "visibility", Message: "只能是 private 或 workspace"})
	}
	if len([]rune(input.ApplicableScope)) > maxRecipeScopeLength {
		errs = append(errs, FieldError{Field: "applicableScope",
			Message: fmt.Sprintf("不能超过 %d 个字符", maxRecipeScopeLength)})
	}
	if len(input.Limitations) > maxRecipeLimitations {
		errs = append(errs, FieldError{Field: "limitations",
			Message: fmt.Sprintf("最多 %d 条", maxRecipeLimitations)})
	}
	for index, limitation := range input.Limitations {
		if len([]rune(limitation)) > maxRecipeLimitationLength {
			errs = append(errs, FieldError{Field: fmt.Sprintf("limitations[%d]", index),
				Message: fmt.Sprintf("不能超过 %d 个字符", maxRecipeLimitationLength)})
		}
	}
	if err := input.Payload.Validate(); err != nil {
		fieldErrors, ok := err.(FieldErrors)
		if !ok {
			errs = append(errs, FieldError{Field: "payload", Message: err.Error()})
		} else {
			errs = append(errs, fieldErrors...)
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// SaveRecipeVersionInput 是保存方案新版本的请求。
type SaveRecipeVersionInput struct {
	Payload      RecipePayload
	ChangeReason string
	// Publish 为 true 时版本直接进入 published（用于「保存并发布」一步完成）。
	Publish bool
}

const (
	maxRecipeNameLength        = 80
	maxRecipeDescriptionLength = 500
	maxRecipeScopeLength       = 500
	maxRecipeLimitations       = 10
	maxRecipeLimitationLength  = 200
)

// validateRecipeName 校验方案名。
func validateRecipeName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return FieldErrors{{Field: "name", Message: "必填"}}
	}
	if len([]rune(trimmed)) > maxRecipeNameLength {
		return FieldErrors{{Field: "name",
			Message: fmt.Sprintf("不能超过 %d 个字符", maxRecipeNameLength)}}
	}
	// 禁止 "latest"：与发布版本名同一理由 —— 用户会以为它总是指向最新内容，
	// 而方案是按版本复制的（复制的是当时那一版）。
	if NormalizeRecipeNameKey(trimmed) == "latest" {
		return FieldErrors{{Field: "name", Message: "不能叫 latest（方案按版本复制，不存在「总是最新」的方案）"}}
	}
	return nil
}

// NormalizeRecipeNameKey 计算方案名的规范化键。
//
// 折叠大小写与连续空白：让「医疗问答」与「医疗问答  」命中同一个唯一键，
// 而用户在列表里看到两行几乎一样的记录时无法判断该用哪个。
func NormalizeRecipeNameKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

// RecipeTargetMatches 判断方案的目标类型是否与项目匹配（T26 验收项「GRPO/SFT 不错配」）。
func RecipeTargetMatches(recipeTargetKind, projectTargetKind string) bool {
	return strings.EqualFold(strings.TrimSpace(recipeTargetKind), strings.TrimSpace(projectTargetKind))
}

// ---------------------------------------------------------------------------
// 复制结果
// ---------------------------------------------------------------------------

// RecipeCopyResult 记录「从方案复制出的文档」。
//
// 为什么需要它而不是复制完就算了：
//   - 缺连接时（T26：「模型连接/凭证不跨工作区复制，缺连接待用户绑定」）
//     需要明确告诉用户「哪一节点还没有绑定连接」，否则项目建出来看起来正常，
//     直到点「开始试制」才失败；
//   - 复制了哪些文档、各自 hash 是什么，是「这个项目从哪来的」的追溯依据。
type RecipeCopyResult struct {
	RecipeID        int64  `json:"recipeId"`
	RecipeVersionID int64  `json:"recipeVersionId"`
	RecipeName      string `json:"recipeName"`
	Version         int    `json:"version"`
	// CopiedDocuments 是复制成功的文档类型（按复制顺序）。
	CopiedDocuments []string `json:"copiedDocuments"`
	// UnboundModelConnection 为 true 表示生成节点引用了模型连接 ID，
	// 但该连接在当前工作区不可用（凭证不跨工作区复制），需要用户绑定。
	UnboundModelConnection bool `json:"unboundModelConnection"`
	// UnboundJudgeConnections 是不可用的裁判连接数（同上，需要重新选择）。
	UnboundJudgeConnections int `json:"unboundJudgeConnections"`
	// ClearedRubricVersion 为 true 表示来源蓝图的评估节点引用了 rubric 版本，
	// 而本轮的五类文档里没有对应类型可以映射它，因此**已清空**并在界面提示重选。
	//
	// 为什么不原样保留：rubricVersionID 指向来源项目里的版本行，在新项目里
	// 要么违反「引用必须同项目」的复合外键（复制直接失败），要么指向一个
	// 不属于本项目的版本（看起来复制成功，其实引用是错的）。
	ClearedRubricVersion bool `json:"clearedRubricVersion"`
	// Limitations 从方案带过来的限制（写进项目说明，避免「复制完就忘了边界」）。
	Limitations []string `json:"limitations"`
}

// DecodeRecipePayload 解析方案 payload JSON。
func DecodeRecipePayload(raw json.RawMessage) (RecipePayload, error) {
	var payload RecipePayload
	if len(raw) == 0 {
		return payload, fmt.Errorf("方案内容为空")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, fmt.Errorf("方案内容不是合法 JSON：%w", err)
	}
	return payload, nil
}
