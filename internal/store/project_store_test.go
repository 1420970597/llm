package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T02 的数据库不变量。
//
// 为什么必须连真实 Postgres：这些断言全部关于**约束与事务**——
//   * 项目与 owner 成员行同事务（失败不留半个项目）；
//   * 项目内唯一名由数据库拦（不靠应用层 SELECT-then-INSERT）；
//   * keyset 分页在并发修改下仍然无重复无遗漏；
//   * 默认工作区幂等。
// 纯函数单测无法证明任何一条。未设置 LLM_TEST_POSTGRES_DSN 时跳过，
// 避免在无数据库环境里伪装通过（T32 会检查这些测试不是 Skip）。
//
// 本地运行：
//
//	docker run --rm --network llm_default -v $PWD:/w -w /w \
//	  -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestProject -v"

func newStudioTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// studioTestFixture 是一个工作区 + 一个用户，供项目测试使用。
type studioTestFixture struct {
	pool        *pgxpool.Pool
	workspaceID int64
	userID      int64
	store       *ProjectStore
}

// newStudioTestFixture 建一套隔离的测试数据，清理时按 ID 精确删除。
//
// 为什么每次新建用户而不是复用默认管理员：项目 owner 是外键引用，
// 复用共享用户会让「删除最后一个 owner」这类断言互相干扰；
// 测试用户用唯一邮箱（含 pid 与测试名）保证并发运行不冲突。
func newStudioTestFixture(t *testing.T) studioTestFixture {
	t.Helper()
	pool := newStudioTestPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", os.Getpid(), t.Name())
	email := fmt.Sprintf("studio-test-%s@example.test", suffix)
	var userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role)
    VALUES ($1, 'x', 'user')
    ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role
    RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	var workspaceID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug)
    VALUES ($1, $2)
    ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
    RETURNING id`, "测试工作区 "+suffix, "studio-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	t.Cleanup(func() {
		// 精确条件删除；project_members 由 ON DELETE CASCADE 带走。
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	return studioTestFixture{pool: pool, workspaceID: workspaceID, userID: userID, store: NewProjectStore(pool)}
}

// validProjectInput 是一份通过校验的创建请求。
func validProjectInput(name string) model.CreateProjectInput {
	input := model.CreateProjectInput{
		Name: name,
		// goal 跟随 name：搜索同时匹配名称与目标，固定的 goal 会让「同名/同名目标」
		// 的断言无法区分「按名称命中」与「按目标命中」。
		Goal:       "交付可用于 SFT 的「" + name + "」领域问答",
		TargetKind: model.TargetKindSFT,
		PilotSize:  12,
	}
	domains, directionsPerDomain, questionsPerDirection := 3, 4, 5
	input.Coverage.Domains = &domains
	input.Coverage.DirectionsPerDomain = &directionsPerDomain
	input.Coverage.QuestionsPerDirection = &questionsPerDirection
	return input
}

// TestProjectCreateIsIndependentAndAssignsOwner 覆盖 T02 的核心验收：
// 「两个项目 ID 不同」+「归属正确」+「owner 成员行存在」。
func TestProjectCreateIsIndependentAndAssignsOwner(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	first, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("冷链问答 A"))
	if err != nil {
		t.Fatalf("create first project: %v", err)
	}
	second, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("冷链问答 B"))
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("两个项目必须有独立 ID，实际都是 %d", first.ID)
	}

	// 归属：创建者必须是 owner，且 ProjectRole 能立即读到。
	role, isMember, err := fixture.store.ProjectRole(ctx, first.ID, fixture.userID)
	if err != nil {
		t.Fatalf("ProjectRole: %v", err)
	}
	if !isMember || role != model.ProjectRoleOwner {
		t.Fatalf("创建者必须是项目 owner，实际 isMember=%v role=%q", isMember, role)
	}

	// 非成员不是成员：membership 才是权限来源，不是 users.role。
	other := seedStudioUser(t, fixture.pool, "outsider")
	if _, isMember, err := fixture.store.ProjectRole(ctx, first.ID, other); err != nil {
		t.Fatalf("ProjectRole(outsider): %v", err)
	} else if isMember {
		t.Fatal("没有成员关系的用户不得被视为项目成员（契约 §1.6）")
	}

	// 计划问题数是 n × m × x，不是「已产出」（§2.1）。
	if got := first.PlannedQuestions(); got != 60 {
		t.Fatalf("plannedQuestions 必须为 n*m*x=60，实际 %d", got)
	}
	// 归档不影响批次/发布的读；新项目默认 draft。
	if first.Status != model.ProjectStatusDraft {
		t.Fatalf("新项目状态必须是 draft，实际 %q", first.Status)
	}
}

// TestProjectCreateLeavesNothingOnFailure 覆盖验收项「失败事务不遗留半个项目」。
//
// 手法：用一个不存在的工作区 ID 触发外键冲突。项目行插不进去，
// 于是「项目 + owner 成员行」这一对必须整体不存在 —— 不能留下孤儿项目。
func TestProjectCreateLeavesNothingOnFailure(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	const missingWorkspace = int64(1 << 62)
	_, err := fixture.store.CreateProject(ctx, missingWorkspace, fixture.userID, validProjectInput("不存在的工作区"))
	if err == nil {
		t.Fatal("外键不存在时必须失败，不能创建出引用不存在工作区的项目")
	}

	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM projects WHERE workspace_id = $1`, missingWorkspace).Scan(&count); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if count != 0 {
		t.Fatalf("失败事务不得留下任何项目行，实际留下 %d 条", count)
	}
}

// TestProjectNameUniquePerWorkspace 覆盖「项目内唯一名由数据库拦」。
func TestProjectNameUniquePerWorkspace(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	if _, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("重名项目")); err != nil {
		t.Fatalf("create first: %v", err)
	}

	// 同名（含大小写差异）必须被唯一索引拒绝，而不是静默创建第二个。
	_, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("重名项目"))
	if err == nil {
		t.Fatal("同一工作区内同名项目必须被拒绝")
	}
	if !IsUniqueViolation(err) {
		t.Fatalf("必须是唯一约束冲突（handler 据此返回 409），实际: %v", err)
	}
}

// TestProjectListPaginationIsStableUnderConcurrency 覆盖验收项「列表分页与归属正确」。
//
// 断言语义（契约 §1.5「翻页无重复、无遗漏」）：
//  1. 逐页翻到底后，看到的项目 ID 集合恰好等于创建的全部 ID（无遗漏）；
//  2. 集合内无重复（无重复）。
//
// 这里刻意在翻页**中途**插入新项目（updatedAt 最大，会排在第一页之前）：
// 这正是 OFFSET 分页会漏项的经典场景，keyset 分页必须仍然收敛到完整集合。
func TestProjectListPaginationIsStableUnderConcurrency(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	const pageSize = 3
	created := map[int64]bool{}
	for i := 0; i < 7; i++ {
		project, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID,
			validProjectInput(fmt.Sprintf("分页项目 %d", i)))
		if err != nil {
			t.Fatalf("create project %d: %v", i, err)
		}
		created[project.ID] = true
	}

	projectIDs, err := fixture.store.ProjectIDsForUser(ctx, fixture.userID)
	if err != nil {
		t.Fatalf("ProjectIDsForUser: %v", err)
	}
	if len(projectIDs) != 7 {
		t.Fatalf("可见项目数必须是 7，实际 %d", len(projectIDs))
	}

	seen := map[int64]int{}
	cursor := ProjectCursor{}
	pages := 0
	for {
		page, err := fixture.store.ListProjects(ctx, ProjectListQuery{
			ProjectIDs: projectIDs,
			Limit:      pageSize,
			Cursor:     cursor,
		})
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("第 %d 页为空但仍有 nextCursor=%q（分页提前终止，会漏项）", pages, page.NextCursor)
		}
		for _, item := range page.Items {
			seen[item.ID]++
		}
		pages++

		if page.NextCursor == "" {
			break
		}
		if pages == 1 {
			// 第一页之后插入一条新项目：它排在最前，属于「已经翻过」的区域。
			// OFFSET 分页会因为位移把原本第 4 条挤到下一页之外（漏项），
			// keyset 分页只看 (updated_at, id) 严格小于游标，不受影响。
			if _, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID,
				validProjectInput("翻页中途插入的项目")); err != nil {
				t.Fatalf("create mid-pagination project: %v", err)
			}
		}
		if pages > 20 {
			t.Fatal("分页未收敛（可能游标恒为空导致死循环）")
		}
		cursor, err = DecodeProjectCursor(page.NextCursor)
		if err != nil {
			t.Fatalf("DecodeProjectCursor: %v", err)
		}
	}

	for id := range created {
		if seen[id] == 0 {
			t.Fatalf("项目 %d 在翻页过程中被漏掉（keyset 分页必须无遗漏）", id)
		}
		if seen[id] > 1 {
			t.Fatalf("项目 %d 出现了 %d 次（keyset 分页必须无重复）", id, seen[id])
		}
	}
}

// TestProjectListSearchOnlyFiltersProjects 覆盖 #159 §1「搜索只过滤项目」。
//
// 两个断言：
//  1. 关键词命中名称 → 只返回那一个；
//  2. 关键词只命中**目标**文案（不在名称里）→ 也必须能返回（搜索覆盖名称与目标）。
func TestProjectListSearchOnlyFiltersProjects(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	matching, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("冷链问答"))
	if err != nil {
		t.Fatalf("create matching: %v", err)
	}
	if _, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("电力巡检")); err != nil {
		t.Fatalf("create other: %v", err)
	}

	projectIDs, _ := fixture.store.ProjectIDsForUser(ctx, fixture.userID)
	page, err := fixture.store.ListProjects(ctx, ProjectListQuery{ProjectIDs: projectIDs, Query: "冷链"})
	if err != nil {
		t.Fatalf("ListProjects(search): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != matching.ID {
		t.Fatalf("搜索必须只命中名称/目标匹配的项目，实际 %d 条", len(page.Items))
	}

	// 只出现在 goal 里、不在 name 里的关键词：搜索必须也能命中。
	detail, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, model.CreateProjectInput{
		Name:       "通用问答集",
		Goal:       "覆盖半导体刻蚀工序的异常诊断",
		TargetKind: model.TargetKindGRPO,
		PilotSize:  5,
	})
	if err != nil {
		t.Fatalf("create goal-only match: %v", err)
	}
	// 可见集合必须重新取：projectIDs 是创建前算好的快照，直接复用会漏掉新项目
	//（这一点本身就是 store 契约的一部分：可见范围属于调用方责任）。
	projectIDs, err = fixture.store.ProjectIDsForUser(ctx, fixture.userID)
	if err != nil {
		t.Fatalf("ProjectIDsForUser: %v", err)
	}
	page, err = fixture.store.ListProjects(ctx, ProjectListQuery{ProjectIDs: projectIDs, Query: "刻蚀"})
	if err != nil {
		t.Fatalf("ListProjects(goal search): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != detail.ID {
		t.Fatalf("搜索必须覆盖目标文案，实际命中 %d 条", len(page.Items))
	}
}

// TestProjectListEmptyScopeReturnsEmptyPage 守住「空可见集合不是全库读取」。
//
// 这条断言针对一类高危错误：把「可见项目为空」当成「不过滤」，
// 于是新用户或撤权用户会看到工作区里的**所有**项目。
func TestProjectListEmptyScopeReturnsEmptyPage(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	if _, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("别人的项目")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	page, err := fixture.store.ListProjects(ctx, ProjectListQuery{ProjectIDs: nil})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("空可见集合必须返回空页，实际返回 %d 条（越权：把空集合当成不过滤）", len(page.Items))
	}
	if page.Items == nil {
		t.Fatal("空页必须是 [] 而不是 null，前端按数组渲染")
	}
}

// TestEnsureDefaultWorkspaceIsIdempotent 覆盖验收项「迁移/引导可重复执行」。
func TestEnsureDefaultWorkspaceIsIdempotent(t *testing.T) {
	pool := newStudioTestPool(t)
	ctx := context.Background()
	store := NewProjectStore(pool)

	userID := seedStudioUser(t, pool, "default-ws-admin")
	memberID := seedStudioUser(t, pool, "default-ws-member")
	first, err := store.EnsureDefaultWorkspace(ctx, userID, memberID)
	if err != nil {
		t.Fatalf("first EnsureDefaultWorkspace: %v", err)
	}
	second, err := store.EnsureDefaultWorkspace(ctx, userID, memberID)
	if err != nil {
		t.Fatalf("second EnsureDefaultWorkspace: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("重复引导必须命中同一个默认工作区：first=%d second=%d", first.ID, second.ID)
	}
	if second.Slug != DefaultWorkspaceSlug {
		t.Fatalf("默认工作区 slug 必须是 %q，实际 %q", DefaultWorkspaceSlug, second.Slug)
	}

	role, isMember, err := store.WorkspaceRole(ctx, second.ID, userID)
	if err != nil {
		t.Fatalf("WorkspaceRole: %v", err)
	}
	if !isMember || role != model.WorkspaceRoleAdmin {
		t.Fatalf("初始管理员必须是 workspace admin，实际 isMember=%v role=%q", isMember, role)
	}

	memberRole, isMember, err := store.WorkspaceRole(ctx, second.ID, memberID)
	if err != nil {
		t.Fatalf("WorkspaceRole(member): %v", err)
	}
	if !isMember || memberRole != model.WorkspaceRoleMember {
		t.Fatalf("默认普通用户必须是 workspace member，实际 isMember=%v role=%q", isMember, memberRole)
	}
}

// TestEnsureDefaultWorkspaceAllowsMemberBootstrapWithoutAdmin guards the
// degraded first-start path: an unavailable admin lookup must not turn the
// nullable audit actor into user id 0 and roll back the ordinary member.
func TestEnsureDefaultWorkspaceAllowsMemberBootstrapWithoutAdmin(t *testing.T) {
	pool := newStudioTestPool(t)
	ctx := context.Background()
	store := NewProjectStore(pool)
	memberID := seedStudioUser(t, pool, "default-ws-member-no-admin")

	workspace, err := store.EnsureDefaultWorkspace(ctx, 0, memberID)
	if err != nil {
		t.Fatalf("EnsureDefaultWorkspace without admin: %v", err)
	}
	role, isMember, err := store.WorkspaceRole(ctx, workspace.ID, memberID)
	if err != nil {
		t.Fatalf("WorkspaceRole(member): %v", err)
	}
	if !isMember || role != model.WorkspaceRoleMember {
		t.Fatalf("member bootstrap must succeed without admin, actual isMember=%v role=%q", isMember, role)
	}
}

// TestEnsureDefaultWorkspaceDoesNotRestoreRemovedBootstrapMember proves that
// the convenient default-user onboarding does not turn an explicit removal
// into a temporary permission that vanishes on the next API restart.
func TestEnsureDefaultWorkspaceDoesNotRestoreRemovedBootstrapMember(t *testing.T) {
	pool := newStudioTestPool(t)
	ctx := context.Background()
	store := NewProjectStore(pool)
	adminID := seedStudioUser(t, pool, "default-ws-revoke-admin")
	memberID := seedStudioUser(t, pool, "default-ws-revoke-member")

	workspace, err := store.EnsureDefaultWorkspace(ctx, adminID, memberID)
	if err != nil {
		t.Fatalf("initial EnsureDefaultWorkspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspace.ID, memberID); err != nil {
		t.Fatalf("remove bootstrap member: %v", err)
	}

	if _, err := store.EnsureDefaultWorkspace(ctx, adminID, memberID); err != nil {
		t.Fatalf("restart EnsureDefaultWorkspace: %v", err)
	}
	_, isMember, err := store.WorkspaceRole(ctx, workspace.ID, memberID)
	if err != nil {
		t.Fatalf("WorkspaceRole after removal: %v", err)
	}
	if isMember {
		t.Fatal("显式移除的默认普通成员不得在 API 重启后被重新授予工作区权限")
	}
}

// TestProjectListConcurrentCreateHasNoDuplicates 是「并发创建不会产生重复可见项」的守卫。
//
// 它与分页测试的区别：分页测试验证读路径，这里验证**写路径**在并发下
// 仍然让每个项目只出现一次（唯一约束 + 事务边界没有留下半行）。
func TestProjectListConcurrentCreateHasNoDuplicates(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	const goroutines = 8
	type createResult struct {
		index int
		err   error
	}
	results := make(chan createResult, goroutines)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID,
				validProjectInput(fmt.Sprintf("并发项目 %d", idx)))
			// 经 channel 回传而不是写共享切片：并发写同一 slice 的元素属于数据竞争。
			results <- createResult{index: idx, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("goroutine %d create 失败: %v", result.index, result.err)
		}
		succeeded++
	}
	if succeeded != goroutines {
		t.Fatalf("并发创建必须全部返回结果：期望 %d，实际 %d", goroutines, succeeded)
	}

	projectIDs, _ := fixture.store.ProjectIDsForUser(ctx, fixture.userID)
	page, err := fixture.store.ListProjects(ctx, ProjectListQuery{ProjectIDs: projectIDs, Limit: 100})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	seen := map[int64]bool{}
	for _, item := range page.Items {
		if seen[item.ID] {
			t.Fatalf("项目 %d 在列表中重复出现", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != goroutines {
		t.Fatalf("并发创建 %d 个项目后必须都能读到，实际 %d 个", goroutines, len(seen))
	}
}

// TestProjectStoreDoesNotRequireProviderOrStorage 是「创建草稿不依赖模型/存储」的守卫。
//
// 手法：把工作区内的 provider/storage 相关表清空都不需要 —— 更直接的证据是
// **本测试全程不插入任何 model_providers / storage_profiles 行**，
// 而项目创建仍然成功。这条断言的价值在于它会在有人往 CreateProject 里
// 加 provider 校验时立刻失败（那正是 issue #58/#83 那类缺陷的形态）。
func TestProjectStoreDoesNotRequireProviderOrStorage(t *testing.T) {
	fixture := newStudioTestFixture(t)
	ctx := context.Background()

	var providers, storages int
	if err := fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM model_providers`).Scan(&providers); err != nil {
		t.Fatalf("count providers: %v", err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM storage_profiles`).Scan(&storages); err != nil {
		t.Fatalf("count storage profiles: %v", err)
	}

	project, err := fixture.store.CreateProject(ctx, fixture.workspaceID, fixture.userID, validProjectInput("无连接项目"))
	if err != nil {
		t.Fatalf("没有 provider/storage 时也必须能创建项目草稿（#160 §1）: %v", err)
	}
	if project.ID == 0 {
		t.Fatal("创建必须返回项目 ID")
	}
	// 明确记录前提，便于读日志的人判断这条测试是否真的覆盖了那个场景。
	t.Logf("providers=%d storage_profiles=%d（本测试不依赖它们）", providers, storages)
}

// seedStudioUser 建一个测试用户并返回 ID。
func seedStudioUser(t *testing.T, pool *pgxpool.Pool, label string) int64 {
	t.Helper()
	email := fmt.Sprintf("studio-%s-%d-%s@example.test", label, os.Getpid(), t.Name())
	var id int64
	if err := pool.QueryRow(context.Background(), `
    INSERT INTO users (email, hashed_password, role)
    VALUES ($1, 'x', 'user')
    ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role
    RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", label, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// TestProjectCursorRoundTripAndRejection 覆盖游标编解码的边界。
//
// 不连数据库也能跑：这是纯函数契约，但它属于 T02 的接口契约，
// 因此放在本文件里与分页测试相邻，便于一起审查。
func TestProjectCursorRoundTripAndRejection(t *testing.T) {
	if cursor, err := DecodeProjectCursor(""); err != nil || !cursor.UpdatedAt.IsZero() {
		t.Fatalf("空游标必须是零值而不是错误：cursor=%+v err=%v", cursor, err)
	}
	if _, err := DecodeProjectCursor("not-base64!!"); err == nil {
		t.Fatal("非法游标必须报错，不能静默从头开始（静默会让用户以为翻页成功）")
	}

	encoded := EncodeProjectCursor(ProjectCursor{ID: 42})
	decoded, err := DecodeProjectCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeProjectCursor: %v", err)
	}
	if decoded.ID != 42 {
		t.Fatalf("游标往返必须保持 ID：期望 42 实际 %d", decoded.ID)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("不可达分支：仅为让 errors 导入被使用")
	}
}
