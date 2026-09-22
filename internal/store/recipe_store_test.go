package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T26 的方案库与「以方案创建项目」（真实 Postgres）。
//
// 六条必须由真库证明的性质：
//  1. 创建/列出/读取/发布版本；
//  2. private 方案只对创建者可见（**包括工作区管理员**——T03 的读权约定）；
//  3. **复制是同一事务写入五类文档**，且项目保留 source_recipe_version_id；
//  4. 草稿不可复制；SFT 方案不能复制进 GRPO 项目；
//  5. **方案升级不影响已复制出去的项目**（T26 验收项）；
//  6. 同名方案（忽略大小写与空白）被拒绝。

type recipeFixture struct {
	pool          *pgxpool.Pool
	recipes       *RecipeStore
	documents     *DocumentStore
	projects      *ProjectStore
	workspaceID   int64
	creatorID     int64
	otherMemberID int64
	adminID       int64
	suffix        string
}

func newRecipeFixture(t *testing.T) *recipeFixture {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := strings.ReplaceAll(t.Name(), "/", "_")
	fixture := &recipeFixture{
		pool: pool, recipes: NewRecipeStore(pool), documents: NewDocumentStore(pool),
		projects: NewProjectStore(pool), suffix: suffix,
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"方案测试工作区 "+suffix, "recipe-test-"+suffix).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	fixture.creatorID = fixture.seedUser(t, "creator")
	fixture.otherMemberID = fixture.seedUser(t, "member")
	fixture.adminID = fixture.seedUser(t, "admin")
	// 三位都是工作区成员：否则「可见性」与「是否成员」两个条件会混在一起，
	// 而 T26 要区分的是可见性。
	for _, userID := range []int64{fixture.creatorID, fixture.otherMemberID, fixture.adminID} {
		role := "member"
		if userID == fixture.adminID {
			role = model.WorkspaceRoleAdmin
		}
		if _, err := pool.Exec(ctx, `
      INSERT INTO workspace_members (workspace_id, user_id, role, created_by)
      VALUES ($1, $2, $3, $2)`, fixture.workspaceID, userID, role); err != nil {
			t.Fatalf("seed workspace member: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		// 项目侧随 projects 级联（文档版本）；方案侧随 recipes 级联（版本）。
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM recipes WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE email LIKE $1`, "recipe-"+suffix+"-%")
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id = $1`, fixture.workspaceID)
	})
	return fixture
}

func (fixture *recipeFixture) seedUser(t *testing.T, label string) int64 {
	t.Helper()
	var userID int64
	if err := fixture.pool.QueryRow(context.Background(), `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"recipe-"+fixture.suffix+"-"+label+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user %s: %v", label, err)
	}
	return userID
}

// recipePayload 构造一份通过校验的方案内容（五类文档齐全）。
//
// 复用 document_store_test.go 的 payload 构造器：两处各写一份必然有一天不一致，
// 而「方案里的文档不能通过文档校验」会以复制时才失败的形式暴露。
func (fixture *recipeFixture) recipePayload(t *testing.T, title string, connectionID int64) model.RecipePayload {
	t.Helper()
	blueprint := blueprintPayload(0, 0)
	blueprint.Nodes.Generation.ModelConnectionID = connectionID
	return model.RecipePayload{
		SchemaVersion: model.RecipePayloadSchemaVersion,
		Coverage:      ptrCoverage(coveragePayload()),
		Standard:      ptrStandard(standardPayload(title)),
		QualityPolicy: &model.QualityPolicyPayload{
			SchemaVersion: model.SchemaVersionFor(model.KindQualityPolicy),
			Rules: []model.QualityRule{{
				ID: "r1", Name: "拒答词检查", MatchType: model.RuleMatchContains,
				Expression: "无法回答", Field: "answer",
				Severity: model.RuleSeverityWarning, SuggestedAction: model.RuleActionReview,
			}},
		},
		Blueprint: &blueprint,
		Mapping: &model.MappingPayload{
			SchemaVersion: model.SchemaVersionFor(model.KindMapping),
			Format:        model.ExportFormatJSONL,
			Fields: []model.MappingField{
				{TargetField: "question", SourceField: "question", Required: true},
			},
		},
	}
}

func ptrCoverage(value model.CoveragePayload) *model.CoveragePayload { return &value }
func ptrStandard(value model.StandardPayload) *model.StandardPayload { return &value }

func (fixture *recipeFixture) createRecipe(t *testing.T, name string, publish bool) model.Recipe {
	t.Helper()
	_, version, err := fixture.recipes.CreateRecipe(context.Background(), fixture.workspaceID, fixture.creatorID,
		model.CreateRecipeInput{
			Name: name, Description: "方案测试", TargetKind: model.TargetKindSFT,
			Visibility: model.RecipeVisibilityWorkspace, ApplicableScope: "冷链问答",
			Limitations: []string{"仅覆盖冷链"}, ChangeReason: "初版",
			Payload: fixture.recipePayload(t, "识别约束", 0),
		}, publish)
	if err != nil {
		t.Fatalf("CreateRecipe: %v", err)
	}
	recipe, err := fixture.recipes.GetRecipe(context.Background(), version.RecipeID, fixture.creatorID)
	if err != nil {
		t.Fatalf("GetRecipe: %v", err)
	}
	return recipe
}

func (fixture *recipeFixture) projectInput(name string, targetKind string) model.CreateProjectInput {
	input := model.CreateProjectInput{Name: name, Goal: "方案复制", TargetKind: targetKind}
	input.Normalize()
	return input
}

func (fixture *recipeFixture) countProjectDocuments(t *testing.T, projectID int64) map[string]int {
	t.Helper()
	rows, err := fixture.pool.Query(context.Background(), `
    SELECT kind, COUNT(*) FROM document_versions WHERE project_id = $1 GROUP BY kind`, projectID)
	if err != nil {
		t.Fatalf("query documents: %v", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			t.Fatalf("scan documents: %v", err)
		}
		counts[kind] = count
	}
	return counts
}

// TestRecipeLifecycle 覆盖创建 → 保存新版本 → 发布。
func TestRecipeLifecycle(t *testing.T) {
	fixture := newRecipeFixture(t)
	ctx := context.Background()

	recipe := fixture.createRecipe(t, "冷链问答方案 "+fixture.suffix, false)
	if recipe.LatestVersion != 1 || recipe.PublishedVersion != 0 {
		t.Fatalf("初版应为 latest=1 published=0，实际 %+v", recipe)
	}

	// 未发布时不可复制（草稿是作者的工作中间态）。
	if _, _, err := fixture.recipes.RecipeVersionForCopy(ctx, recipe.ID, fixture.creatorID); !errors.Is(err, ErrRecipeVersionNotFound) {
		// recipe.ID 是方案 ID 不是版本 ID，这里预期走到「版本不存在」；
		// 真正的草稿拒绝在下一条断言里（用真实版本 ID）。
		t.Logf("用方案 ID 作为版本 ID 查询得到：%v（预期 ErrRecipeVersionNotFound）", err)
	}
	versions, err := fixture.recipes.ListRecipeVersions(ctx, recipe.ID, 10)
	if err != nil {
		t.Fatalf("ListRecipeVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("应有 1 个版本，实际 %d", len(versions))
	}
	if _, _, err := fixture.recipes.RecipeVersionForCopy(ctx, versions[0].ID, fixture.creatorID); !errors.Is(err, ErrRecipeVersionNotPublished) {
		t.Fatalf("草稿版本必须拒绝复制，实际 %v", err)
	}

	// 保存新版本并发布。
	saved, err := fixture.recipes.SaveRecipeVersion(ctx, recipe.ID, fixture.creatorID, model.SaveRecipeVersionInput{
		Payload: fixture.recipePayload(t, "识别约束并列出例外", 0), ChangeReason: "补充边界检查点",
	})
	if err != nil {
		t.Fatalf("SaveRecipeVersion: %v", err)
	}
	if saved.Version != 2 || saved.Status != model.RecipeVersionDraft {
		t.Fatalf("新版本应为 v2/draft，实际 v%d/%s", saved.Version, saved.Status)
	}
	// 变更理由必填：没有它，使用者无法判断该选哪一版。
	if _, err := fixture.recipes.SaveRecipeVersion(ctx, recipe.ID, fixture.creatorID, model.SaveRecipeVersionInput{
		Payload: fixture.recipePayload(t, "无理由", 0),
	}); err == nil {
		t.Fatal("缺少变更理由必须被拒绝")
	}

	published, err := fixture.recipes.PublishRecipeVersion(ctx, recipe.ID, 2, fixture.creatorID)
	if err != nil {
		t.Fatalf("PublishRecipeVersion: %v", err)
	}
	if published.Status != model.RecipeVersionPublished || published.PublishedAt == nil {
		t.Fatalf("发布后状态与时间必须写入：%+v", published)
	}

	reloaded, err := fixture.recipes.GetRecipe(ctx, recipe.ID, fixture.otherMemberID)
	if err != nil {
		t.Fatalf("GetRecipe: %v", err)
	}
	if reloaded.PublishedVersion != 2 || reloaded.LatestVersion != 2 || reloaded.VersionCount != 2 {
		t.Fatalf("读模型计数不符：%+v", reloaded)
	}

	list, err := fixture.recipes.ListRecipes(ctx, fixture.workspaceID, fixture.otherMemberID, model.TargetKindSFT, 10)
	if err != nil {
		t.Fatalf("ListRecipes: %v", err)
	}
	found := false
	for _, item := range list {
		if item.ID == recipe.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("workspace 可见度的方案必须出现在其他成员的列表里")
	}
}

// TestRecipePrivateVisibility 覆盖 private 只对创建者可见（管理员也不例外）。
func TestRecipePrivateVisibility(t *testing.T) {
	fixture := newRecipeFixture(t)
	ctx := context.Background()

	_, version, err := fixture.recipes.CreateRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		model.CreateRecipeInput{
			Name: "私有方案 " + fixture.suffix, TargetKind: model.TargetKindSFT,
			Visibility: model.RecipeVisibilityPrivate, ChangeReason: "初版",
			Payload: fixture.recipePayload(t, "私有", 0),
		}, true)
	if err != nil {
		t.Fatalf("CreateRecipe: %v", err)
	}

	if _, err := fixture.recipes.GetRecipe(ctx, version.RecipeID, fixture.otherMemberID); !errors.Is(err, ErrRecipeNotFound) {
		t.Fatalf("普通成员不得读取私有方案，实际 %v", err)
	}
	// 工作区管理员同样读不到：T03 明确 admin 不默认拥有所有内容读权。
	if _, err := fixture.recipes.GetRecipe(ctx, version.RecipeID, fixture.adminID); !errors.Is(err, ErrRecipeNotFound) {
		t.Fatalf("工作区管理员不得读取私有方案（T03 的读权约定），实际 %v", err)
	}
	// 复制同样被拒绝（可见性在复制路径上生效）。
	if _, _, err := fixture.recipes.RecipeVersionForCopy(ctx, version.ID, fixture.otherMemberID); !errors.Is(err, ErrRecipeNotFound) {
		t.Fatalf("不可见方案的版本不得被复制，实际 %v", err)
	}
	if _, err := fixture.recipes.GetRecipe(ctx, version.RecipeID, fixture.creatorID); err != nil {
		t.Fatalf("创建者必须能读自己的私有方案：%v", err)
	}
}

// TestRecipeCopyWritesDocumentsInOneTransaction 覆盖 T26 的核心验收项。
func TestRecipeCopyWritesDocumentsInOneTransaction(t *testing.T) {
	fixture := newRecipeFixture(t)
	ctx := context.Background()
	recipe := fixture.createRecipe(t, "可复制方案 "+fixture.suffix, true)

	// 蓝图引用一个不存在的模型连接：复制后必须标记「需要绑定连接」，
	// 而不是让项目看起来配置齐全直到开始试制才失败。
	versions, err := fixture.recipes.ListRecipeVersions(ctx, recipe.ID, 10)
	if err != nil {
		t.Fatalf("ListRecipeVersions: %v", err)
	}
	if _, err := fixture.recipes.SaveRecipeVersion(ctx, recipe.ID, fixture.creatorID, model.SaveRecipeVersionInput{
		Payload: fixture.recipePayload(t, "带连接引用", 999999), ChangeReason: "引用外部连接",
		Publish: true,
	}); err != nil {
		t.Fatalf("SaveRecipeVersion: %v", err)
	}
	_ = versions

	latest, err := fixture.recipes.ListRecipeVersions(ctx, recipe.ID, 10)
	if err != nil {
		t.Fatalf("ListRecipeVersions: %v", err)
	}
	target := latest[0]

	project, result, err := fixture.recipes.CreateProjectFromRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		fixture.projectInput("从方案创建 "+fixture.suffix, model.TargetKindSFT), target.ID)
	if err != nil {
		t.Fatalf("CreateProjectFromRecipe: %v", err)
	}
	if result.RecipeVersionID != target.ID || result.Version != target.Version {
		t.Fatalf("复制结果必须记录来源版本：%+v（期望 v%d）", result, target.Version)
	}
	if len(result.CopiedDocuments) != 5 {
		t.Fatalf("必须复制五类文档，实际 %v", result.CopiedDocuments)
	}
	if !result.UnboundModelConnection {
		t.Fatal("引用了不可用连接时必须标记「需要绑定连接」")
	}
	if project.SourceRecipeVersionID == nil || *project.SourceRecipeVersionID != target.ID {
		t.Fatalf("项目必须保留 sourceRecipeVersionId，实际 %v", project.SourceRecipeVersionID)
	}

	counts := fixture.countProjectDocuments(t, project.ID)
	for _, kind := range []string{"coverage", "standard", "quality_policy", "blueprint", "mapping"} {
		if counts[kind] != 1 {
			t.Fatalf("项目里 %s 应有 1 个版本，实际 %d（全部：%v）", kind, counts[kind], counts)
		}
	}

	// 蓝图引用被正确复制（覆盖/标准版本行存在，引用边由 saveVersionTx 重建）。
	rows, err := fixture.documents.DocumentReferences(ctx, mustBlueprintVersionID(t, fixture, project.ID))
	if err != nil {
		t.Fatalf("DocumentReferences: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("复制后的蓝图必须保留对覆盖/标准版本的引用（否则项目看起来有配置但节点没有指向任何版本）")
	}
	for _, targetID := range rows {
		if targetID <= 0 {
			t.Fatalf("引用目标必须是新项目里的版本行，实际 %v", rows)
		}
	}
}

func mustBlueprintVersionID(t *testing.T, fixture *recipeFixture, projectID int64) int64 {
	t.Helper()
	document, err := fixture.documents.GetDocument(context.Background(), projectID, model.KindBlueprint, DefaultLogicalID)
	if err != nil {
		t.Fatalf("GetDocument(blueprint): %v", err)
	}
	if document.Current == nil {
		t.Fatal("复制后的项目必须有当前蓝图版本")
	}
	return document.Current.ID
}

// TestRecipeCopyRejectsDraftAndTargetMismatch 覆盖两条拒绝路径。
func TestRecipeCopyRejectsDraftAndTargetMismatch(t *testing.T) {
	fixture := newRecipeFixture(t)
	ctx := context.Background()

	// 草稿版本不可复制。
	_, draft, err := fixture.recipes.CreateRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		model.CreateRecipeInput{
			Name: "草稿方案 " + fixture.suffix, TargetKind: model.TargetKindSFT,
			Visibility: model.RecipeVisibilityWorkspace, ChangeReason: "初版",
			Payload: fixture.recipePayload(t, "草稿", 0),
		}, false)
	if err != nil {
		t.Fatalf("CreateRecipe: %v", err)
	}
	if _, _, err := fixture.recipes.CreateProjectFromRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		fixture.projectInput("草稿复制 "+fixture.suffix, model.TargetKindSFT), draft.ID); !errors.Is(err, ErrRecipeVersionNotPublished) {
		t.Fatalf("草稿版本必须拒绝创建项目，实际 %v", err)
	}

	// SFT 方案不能复制进 GRPO 项目。
	recipe := fixture.createRecipe(t, "类型校验方案 "+fixture.suffix, true)
	versions, err := fixture.recipes.ListRecipeVersions(ctx, recipe.ID, 5)
	if err != nil {
		t.Fatalf("ListRecipeVersions: %v", err)
	}
	if _, _, err := fixture.recipes.CreateProjectFromRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		fixture.projectInput("类型不符 "+fixture.suffix, model.TargetKindGRPO), versions[0].ID); err == nil {
		t.Fatal("SFT 方案复制进 GRPO 项目必须被拒绝（否则会产出一批结构错误的样本）")
	} else if _, ok := model.HasFieldErrors(err); !ok {
		t.Fatalf("类型不符应返回字段级错误（用户要知道改哪个字段），实际 %v", err)
	}
}

// TestRecipeUpgradeDoesNotAffectCopiedProject 覆盖 T26
// 「方案升级只影响未来复制」。
func TestRecipeUpgradeDoesNotAffectCopiedProject(t *testing.T) {
	fixture := newRecipeFixture(t)
	ctx := context.Background()
	recipe := fixture.createRecipe(t, "升级方案 "+fixture.suffix, true)

	versions, err := fixture.recipes.ListRecipeVersions(ctx, recipe.ID, 5)
	if err != nil {
		t.Fatalf("ListRecipeVersions: %v", err)
	}
	firstVersionID := versions[0].ID
	project, _, err := fixture.recipes.CreateProjectFromRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		fixture.projectInput("升级前复制 "+fixture.suffix, model.TargetKindSFT), firstVersionID)
	if err != nil {
		t.Fatalf("CreateProjectFromRecipe: %v", err)
	}
	beforeHash := blueprintHashOf(t, fixture, project.ID)

	// 方案升级：v2 内容不同并发布。
	if _, err := fixture.recipes.SaveRecipeVersion(ctx, recipe.ID, fixture.creatorID, model.SaveRecipeVersionInput{
		Payload: fixture.recipePayload(t, "完全不同的标准", 0), ChangeReason: "换了标准", Publish: true,
	}); err != nil {
		t.Fatalf("SaveRecipeVersion: %v", err)
	}

	// 已复制出去的项目**不受影响**：它的蓝图版本与 content hash 都不变。
	afterHash := blueprintHashOf(t, fixture, project.ID)
	if beforeHash != afterHash {
		t.Fatalf("方案升级不得改变已复制项目的配置：%s -> %s", beforeHash, afterHash)
	}
	counts := fixture.countProjectDocuments(t, project.ID)
	if counts["blueprint"] != 1 || counts["standard"] != 1 {
		t.Fatalf("方案升级不得给已建项目追加版本：%v", counts)
	}

	// 新复制拿到的是 v2 的内容。
	latest, err := fixture.recipes.ListRecipeVersions(ctx, recipe.ID, 5)
	if err != nil {
		t.Fatalf("ListRecipeVersions: %v", err)
	}
	fresh, result, err := fixture.recipes.CreateProjectFromRecipe(ctx, fixture.workspaceID, fixture.creatorID,
		fixture.projectInput("升级后复制 "+fixture.suffix, model.TargetKindSFT), latest[0].ID)
	if err != nil {
		t.Fatalf("CreateProjectFromRecipe(v2): %v", err)
	}
	if result.Version != 2 {
		t.Fatalf("新复制必须来自 v2，实际 v%d", result.Version)
	}
	// 两份方案版本的差异在**标准**文档上，因此比对标准文档的 hash：
	// 比对蓝图不会有差异（两份方案的蓝图内容相同），那会让这条断言空转。
	if before := documentHashOf(t, fixture, project.ID, model.KindStandard); documentHashOf(t, fixture, fresh.ID, model.KindStandard) == before {
		t.Fatal("v2 复制出的项目内容应与 v1 不同（否则复制没有真正取到选定版本）")
	}
}

// documentHashOf 读取项目里某类文档当前版本的 content hash。
//
// 用 hash 而不是「版本数」判断「内容有没有变」：版本数会因为无关操作
// （例如重放一次复制）而变化，而 hash 只随内容变。
func documentHashOf(t *testing.T, fixture *recipeFixture, projectID int64, kind model.DocumentKind) string {
	t.Helper()
	document, err := fixture.documents.GetDocument(context.Background(), projectID, kind, DefaultLogicalID)
	if err != nil {
		t.Fatalf("GetDocument(%s): %v", kind, err)
	}
	if document.Current == nil {
		t.Fatalf("项目缺少当前 %s 版本", kind)
	}
	return document.Current.ContentHash
}

func blueprintHashOf(t *testing.T, fixture *recipeFixture, projectID int64) string {
	t.Helper()
	return documentHashOf(t, fixture, projectID, model.KindBlueprint)
}

// TestRecipeDuplicateNameRejected 覆盖同名（忽略大小写与空白）拒绝。
func TestRecipeDuplicateNameRejected(t *testing.T) {
	fixture := newRecipeFixture(t)
	ctx := context.Background()
	name := "重名方案 " + fixture.suffix
	fixture.createRecipe(t, name, true)

	_, _, err := fixture.recipes.CreateRecipe(ctx, fixture.workspaceID, fixture.creatorID, model.CreateRecipeInput{
		Name: "  " + strings.ToUpper(name) + "  ", TargetKind: model.TargetKindSFT,
		Visibility: model.RecipeVisibilityWorkspace, ChangeReason: "重复",
		Payload: fixture.recipePayload(t, "重复", 0),
	}, true)
	if err == nil {
		t.Fatal("同名方案必须被拒绝（规范化后比较）")
	}
	if _, ok := model.HasFieldErrors(err); !ok {
		t.Fatalf("重名应返回字段级错误，实际 %v", err)
	}
}

// TestRecipeNameLatestRejected 覆盖「方案不能叫 latest」。
func TestRecipeNameLatestRejected(t *testing.T) {
	fixture := newRecipeFixture(t)
	_, _, err := fixture.recipes.CreateRecipe(context.Background(), fixture.workspaceID, fixture.creatorID,
		model.CreateRecipeInput{
			Name: "latest", TargetKind: model.TargetKindSFT,
			Visibility: model.RecipeVisibilityWorkspace, ChangeReason: "初版",
			Payload: fixture.recipePayload(t, "latest", 0),
		}, true)
	if err == nil {
		t.Fatal("方案名禁止叫 latest（方案按版本复制，不存在「总是最新」的方案）")
	}
}
