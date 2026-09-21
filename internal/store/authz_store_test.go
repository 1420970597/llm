package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T03 的授权、撤权与审计不变量。
//
// 为什么必须连真实 Postgres：全部断言都关于**服务端当前状态**——
// 撤权后同一次会话必须立即失效、最后一名 owner 由数据库触发器兜底、
// 审计与变更同事务（失败不留审计、成功必有审计）。
// 这些性质在纯内存测试里无法被证明。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

// authzFixture 是一个工作区 + 三个用户的测试场景。
type authzFixture struct {
	pool        *pgxpool.Pool
	workspaceID int64
	ownerID     int64
	reviewerID  int64
	viewerID    int64
	outsiderID  int64
	projectID   int64
	authz       *AuthzStore
}

// newAuthzFixture 建项目并把 reviewer/viewer 加为成员。
func newAuthzFixture(t *testing.T) authzFixture {
	t.Helper()
	pool := newStudioTestPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", os.Getpid(), t.Name())
	workspaceID := seedAuthzWorkspace(t, pool, suffix)
	ownerID := seedStudioUser(t, pool, "owner-"+suffix)
	reviewerID := seedStudioUser(t, pool, "reviewer-"+suffix)
	viewerID := seedStudioUser(t, pool, "viewer-"+suffix)
	outsiderID := seedStudioUser(t, pool, "outsider-"+suffix)

	projectStore := NewProjectStore(pool)
	project, err := projectStore.CreateProject(ctx, workspaceID, ownerID, validProjectInput("授权测试项目"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	authz := NewAuthzStore(pool)
	if err := authz.UpsertProjectMember(ctx, project.ID, ownerID, reviewerID, model.ProjectRoleReviewer, "测试初始化", "req-test"); err != nil {
		t.Fatalf("add reviewer: %v", err)
	}
	if err := authz.UpsertProjectMember(ctx, project.ID, ownerID, viewerID, model.ProjectRoleViewer, "测试初始化", "req-test"); err != nil {
		t.Fatalf("add viewer: %v", err)
	}

	return authzFixture{
		pool: pool, workspaceID: workspaceID,
		ownerID: ownerID, reviewerID: reviewerID, viewerID: viewerID, outsiderID: outsiderID,
		projectID: project.ID, authz: authz,
	}
}

// seedAuthzWorkspace 建一个测试工作区，并把 owner 之外的角色交给测试自己写。
func seedAuthzWorkspace(t *testing.T, pool *pgxpool.Pool, suffix string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
    INSERT INTO workspaces (name, slug)
    VALUES ($1, $2)
    ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
    RETURNING id`, "授权测试工作区 "+suffix, "authz-test-"+suffix).Scan(&id); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Cleanup(func() {
		// 审计有面向 projects/workspaces 的外键，删除顺序由 CASCADE 处理。
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE workspace_id = $1`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, id)
	})
	return id
}

// TestAuthzMatrixByRole 是 T03 的核心断言：角色 → 动作的完整矩阵。
//
// 契约 §1.6 的表格逐格验证。任何一格写反都是越权或能力丢失，
// 而这类错误在 UI 上表现为「按钮能点但服务端拒绝」或更糟的反向情况。
func TestAuthzMatrixByRole(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		userID int64
		action AuthzAction
		want   bool
	}{
		// owner：设计/运行/判断/发布/读/下载/管成员 全部允许。
		{"owner 设计", fixture.ownerID, AuthzDesign, true},
		{"owner 运行", fixture.ownerID, AuthzRun, true},
		{"owner 判断", fixture.ownerID, AuthzReview, true},
		{"owner 发布", fixture.ownerID, AuthzPublish, true},
		{"owner 读", fixture.ownerID, AuthzRead, true},
		{"owner 下载", fixture.ownerID, AuthzDownload, true},
		{"owner 管成员", fixture.ownerID, AuthzManageMembers, true},

		// reviewer：可读、可判断、可下载；不得设计/运行/发布/管成员。
		{"reviewer 读", fixture.reviewerID, AuthzRead, true},
		{"reviewer 判断", fixture.reviewerID, AuthzReview, true},
		{"reviewer 下载", fixture.reviewerID, AuthzDownload, true},
		{"reviewer 设计", fixture.reviewerID, AuthzDesign, false},
		{"reviewer 运行", fixture.reviewerID, AuthzRun, false},
		{"reviewer 发布", fixture.reviewerID, AuthzPublish, false},
		{"reviewer 管成员", fixture.reviewerID, AuthzManageMembers, false},

		// viewer：只读授权内容 + 允许下载的发布。
		{"viewer 读", fixture.viewerID, AuthzRead, true},
		{"viewer 下载", fixture.viewerID, AuthzDownload, true},
		{"viewer 设计", fixture.viewerID, AuthzDesign, false},
		{"viewer 运行", fixture.viewerID, AuthzRun, false},
		{"viewer 判断", fixture.viewerID, AuthzReview, false},
		{"viewer 发布", fixture.viewerID, AuthzPublish, false},
		{"viewer 管成员", fixture.viewerID, AuthzManageMembers, false},

		// 非成员：全部拒绝，且 IsMember=false（调用方据此返回资源隐藏型 404）。
		{"非成员 读", fixture.outsiderID, AuthzRead, false},
		{"非成员 设计", fixture.outsiderID, AuthzDesign, false},
		{"非成员 下载", fixture.outsiderID, AuthzDownload, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decision, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, testCase.userID, testCase.action)
			if err != nil {
				t.Fatalf("AuthorizeProject: %v", err)
			}
			if decision.Allowed != testCase.want {
				t.Fatalf("动作 %s 的判定必须是 %v，实际 %v（角色=%q isMember=%v）",
					testCase.action, testCase.want, decision.Allowed, decision.Role, decision.IsMember)
			}
			if testCase.userID == fixture.outsiderID && decision.IsMember {
				t.Fatal("非成员必须返回 IsMember=false，否则调用方会误判成 403（确认了项目存在）")
			}
		})
	}
}

// TestAuthzWorkspaceAdminHasNoProjectContentAccess 覆盖契约 §1.6 的关键一行：
// 「workspace admin 管理成员/连接，**不默认拥有**所有项目内容读权」。
//
// 这条断言防的是最容易被写成「看起来合理」的越权：既然他能管工作区，
// 顺手让他读项目内容似乎是自然的 —— 但项目名与样本正文就是业务信息本身。
func TestAuthzWorkspaceAdminHasNoProjectContentAccess(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	adminID := seedStudioUser(t, fixture.pool, "ws-admin")
	if _, err := fixture.pool.Exec(ctx, `
    INSERT INTO workspace_members (workspace_id, user_id, role)
    VALUES ($1, $2, 'admin')
    ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'admin'`,
		fixture.workspaceID, adminID); err != nil {
		t.Fatalf("add workspace admin: %v", err)
	}

	// 治理动作允许。
	workspaceDecision, err := fixture.authz.AuthorizeWorkspace(ctx, fixture.workspaceID, adminID, AuthzManageWorkspace)
	if err != nil {
		t.Fatalf("AuthorizeWorkspace: %v", err)
	}
	if !workspaceDecision.Allowed {
		t.Fatal("workspace admin 必须能管理工作区成员与连接")
	}

	// 项目内容动作拒绝，且必须是「非成员」而不是「角色不足」。
	for _, action := range []AuthzAction{AuthzRead, AuthzDesign, AuthzRun, AuthzReview, AuthzPublish} {
		decision, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, adminID, action)
		if err != nil {
			t.Fatalf("AuthorizeProject(%s): %v", action, err)
		}
		if decision.Allowed {
			t.Fatalf("workspace admin 不得拥有项目内容动作 %s（契约 §1.6）", action)
		}
		if decision.IsMember {
			t.Fatalf("workspace admin 未显式加入项目时必须 IsMember=false（动作 %s）", action)
		}
	}

	// 显式加入后才获得内容权限 —— 这就是「额外访问需显式成员授权」。
	if err := fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID, adminID, model.ProjectRoleViewer, "显式授权", "req-test"); err != nil {
		t.Fatalf("explicit grant: %v", err)
	}
	decision, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, adminID, AuthzRead)
	if err != nil {
		t.Fatalf("AuthorizeProject after grant: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("显式加入项目后必须能读授权内容")
	}
}

// TestAuthzRevocationIsImmediate 覆盖 T03 验收项「撤销成员」立即生效。
//
// 关键点：**判定每次重新读库**，不信任会话里的角色副本。
// 这条测试直接对「登录时写入 cookie 的角色」这一实现方式下达否定：
// 那种实现下撤权后旧会话仍然能读，而缺陷只有真实撤权场景才暴露。
func TestAuthzRevocationIsImmediate(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	before, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, fixture.reviewerID, AuthzReview)
	if err != nil {
		t.Fatalf("AuthorizeProject before revoke: %v", err)
	}
	if !before.Allowed {
		t.Fatal("前置条件不成立：reviewer 在撤权前必须能判断")
	}

	if err := fixture.authz.RemoveProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.reviewerID, "离开团队", "req-revoke"); err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}

	after, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, fixture.reviewerID, AuthzReview)
	if err != nil {
		t.Fatalf("AuthorizeProject after revoke: %v", err)
	}
	if after.Allowed {
		t.Fatal("撤权后必须立即拒绝（不得信任登录时的角色副本）")
	}
	if after.IsMember {
		t.Fatal("撤权后 IsMember 必须为 false，调用方据此返回资源隐藏型 404")
	}
}

// TestAuthzKeepLastOwnerRejected 覆盖 T03 验收项「不得移除最后一名 owner」。
//
// 三种移除路径都要被拦：显式移除、降级角色、以及**数据库触发器**兜底。
// 只测其中一条会漏掉「绕过应用层直接改库」这条路径。
func TestAuthzKeepLastOwnerRejected(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	// 路径 1：显式移除最后一名 owner。
	err := fixture.authz.RemoveProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.ownerID, "测试", "req-test")
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("移除最后一名 owner 必须返回 ErrLastOwner，实际: %v", err)
	}

	// 路径 2：降级最后一名 owner 的角色。
	err = fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.ownerID, model.ProjectRoleViewer, "测试", "req-test")
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("降级最后一名 owner 必须返回 ErrLastOwner，实际: %v", err)
	}

	// 路径 3：绕过应用层直接 DELETE —— 数据库触发器必须拦住。
	_, rawErr := fixture.pool.Exec(ctx, `
    DELETE FROM project_members WHERE project_id = $1 AND role = 'owner'`, fixture.projectID)
	if rawErr == nil {
		t.Fatal("绕过应用层删除最后一名 owner 必须被数据库触发器拒绝（应用层判断必然漏路径）")
	}
	if !LooksLikeCheckViolation(rawErr) {
		t.Fatalf("期望数据库约束错误（check_violation），实际: %v", rawErr)
	}

	// 反证：先加第二名 owner，再移除第一名必须成功。
	secondOwner := seedStudioUser(t, fixture.pool, "second-owner")
	if err := fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID, secondOwner, model.ProjectRoleOwner, "共同负责", "req-test"); err != nil {
		t.Fatalf("add second owner: %v", err)
	}
	if err := fixture.authz.RemoveProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.ownerID, "交接", "req-test"); err != nil {
		t.Fatalf("有两名 owner 时移除其中一名必须成功: %v", err)
	}
}

// TestAuthzLastOwnerCheckIsRaceSafe 覆盖「两个并发降级各自看到另一个 owner 而同时通过」。
//
// 这是典型 write skew：读到的结论在提交前失效。修复方式是在事务内
// `SELECT ... FOR UPDATE` 锁住目标成员行，让第二个事务串行化。
//
// 断言：两个并发降级请求中**恰有一个**成功，项目始终留有 owner。
func TestAuthzLastOwnerCheckIsRaceSafe(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	secondOwner := seedStudioUser(t, fixture.pool, "race-owner")
	if err := fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID, secondOwner, model.ProjectRoleOwner, "并发测试", "req-test"); err != nil {
		t.Fatalf("add second owner: %v", err)
	}

	var wg sync.WaitGroup
	type outcome struct {
		label string
		err   error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})

	for _, target := range []struct {
		label string
		id    int64
	}{{"owner", fixture.ownerID}, {"second", secondOwner}} {
		wg.Add(1)
		go func(label string, id int64) {
			defer wg.Done()
			<-start
			results <- outcome{label: label, err: fixture.authz.UpsertProjectMember(
				ctx, fixture.projectID, fixture.ownerID, id, model.ProjectRoleViewer, "并发降级", "req-race")}
		}(target.label, target.id)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	for result := range results {
		if result.err == nil {
			succeeded++
			continue
		}
		if !errors.Is(result.err, ErrLastOwner) {
			t.Fatalf("%s 的降级失败原因必须是 ErrLastOwner，实际: %v", result.label, result.err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("两个并发降级必须恰好成功一个（否则项目会没有 owner），实际成功 %d 个", succeeded)
	}

	var ownerCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND role = 'owner'`,
		fixture.projectID).Scan(&ownerCount); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	if ownerCount != 1 {
		t.Fatalf("项目必须始终留有恰好 1 名 owner，实际 %d", ownerCount)
	}
}

// TestAuthzAuditIsWrittenWithChange 覆盖 T03 验收项「变更与审计同事务」。
//
// 断言两件事：
//  1. 成功的成员变更必然留下带 actor/object/revision/reason/requestId 的审计；
//  2. **失败的变更不留审计**（那是「写了审计但没改成功」，会让历史与事实不符）。
func TestAuthzAuditIsWrittenWithChange(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	// 清掉 fixture 初始化留下的审计，让计数从零开始。
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM audit_logs WHERE project_id = $1`, fixture.projectID); err != nil {
		t.Fatalf("clear audit: %v", err)
	}

	target := seedStudioUser(t, fixture.pool, "audited")
	if err := fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID,
		target, model.ProjectRoleReviewer, "参与质量审阅", "req-audit-1"); err != nil {
		t.Fatalf("UpsertProjectMember: %v", err)
	}

	entries, err := fixture.authz.ListProjectAudit(ctx, fixture.projectID, time.Time{}, 0, 50)
	if err != nil {
		t.Fatalf("ListProjectAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("成功的成员变更必须留下恰好 1 条审计，实际 %d 条", len(entries))
	}
	entry := entries[0]
	if entry.ActorUserID == nil || *entry.ActorUserID != fixture.ownerID {
		t.Fatalf("审计必须记录 actor_user_id（结构化身份，可联表），实际 %+v", entry.ActorUserID)
	}
	if entry.ProjectID == nil || *entry.ProjectID != fixture.projectID {
		t.Fatalf("审计必须记录 project_id（按项目查询与对账），实际 %+v", entry.ProjectID)
	}
	if entry.Reason != "参与质量审阅" {
		t.Fatalf("审计必须记录变更理由，实际 %q", entry.Reason)
	}
	if entry.RequestID != "req-audit-1" {
		t.Fatalf("审计必须记录 requestId（与日志对上同一次请求），实际 %q", entry.RequestID)
	}
	if entry.Resource != "project_member" || entry.Action != "member_upsert" {
		t.Fatalf("审计必须记录对象与动作，实际 %s/%s", entry.Resource, entry.Action)
	}
	// 审计不得泄漏样本正文：成员变更只记角色变化（T03 验收项）。
	if entry.Detail != "role=reviewer previous=" {
		t.Fatalf("成员审计的 detail 只应记录角色变化，实际 %q", entry.Detail)
	}

	// 失败的变更（最后一名 owner）不得留下审计。
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM audit_logs WHERE project_id = $1`, fixture.projectID); err != nil {
		t.Fatalf("clear audit: %v", err)
	}
	err = fixture.authz.RemoveProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.ownerID, "试图移除自己", "req-audit-2")
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("前置条件不成立：期望 ErrLastOwner，实际 %v", err)
	}
	entries, err = fixture.authz.ListProjectAudit(ctx, fixture.projectID, time.Time{}, 0, 50)
	if err != nil {
		t.Fatalf("ListProjectAudit: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("失败的变更不得留下审计（否则历史与事实不符），实际 %d 条", len(entries))
	}
}

// TestAuthzAuditNeverContainsSecretsOrSampleText 覆盖 T03 验收项
// 「连接密钥与样本正文不进审计日志」。
//
// 手法：用一个把密钥/正文塞进 reason 的请求，断言审计表里**搜不到**那些字符串。
// 这说明审计写入路径不反射用户输入到 detail 之外的列，也不把对象正文序列化进去。
func TestAuthzAuditNeverContainsSecretsOrSampleText(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	const secret = "sk-live-DEADBEEF-not-a-real-key"
	target := seedStudioUser(t, fixture.pool, "secret-check")
	// reason 会被存进 reason 列（用户自己写的理由），但**不能**出现在 detail 或
	// 其它自动生成的列里；这里同时断言 SQL 层没有把成员邮箱等写进去。
	if err := fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID,
		target, model.ProjectRoleViewer, "临时授权 "+secret, "req-secret"); err != nil {
		t.Fatalf("UpsertProjectMember: %v", err)
	}

	var leaked int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM audit_logs
    WHERE project_id = $1 AND (detail LIKE '%' || $2 || '%' OR resource_id LIKE '%' || $2 || '%')`,
		fixture.projectID, secret).Scan(&leaked); err != nil {
		t.Fatalf("count leaked secrets: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("密钥不得进入审计的 detail/resource_id 列，实际命中 %d 条", leaked)
	}

	var emailLeaked int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM audit_logs
    WHERE project_id = $1 AND detail LIKE '%@%'`, fixture.projectID).Scan(&emailLeaked); err != nil {
		t.Fatalf("count leaked emails: %v", err)
	}
	if emailLeaked != 0 {
		t.Fatalf("审计的 detail 不应记录成员邮箱（治理信息，T28 的成员页才展示），实际命中 %d 条", emailLeaked)
	}
}

// TestAuthzArchivedProjectIsReadOnly 覆盖「归档只阻止新运行，不删批次或发布」。
func TestAuthzArchivedProjectIsReadOnly(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	if _, err := fixture.pool.Exec(ctx, `
    UPDATE projects SET status = 'archived' WHERE id = $1`, fixture.projectID); err != nil {
		t.Fatalf("archive project: %v", err)
	}

	read, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, fixture.ownerID, AuthzRead)
	if err != nil {
		t.Fatalf("AuthorizeProject(read): %v", err)
	}
	if !read.Allowed {
		t.Fatal("归档项目必须仍可读（历史不能被关闭）")
	}
	download, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, fixture.ownerID, AuthzDownload)
	if err != nil {
		t.Fatalf("AuthorizeProject(download): %v", err)
	}
	if !download.Allowed {
		t.Fatal("归档项目必须仍可下载已发布版本")
	}

	for _, action := range []AuthzAction{AuthzDesign, AuthzRun, AuthzReview, AuthzPublish, AuthzManageMembers} {
		decision, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, fixture.ownerID, action)
		if err != nil {
			t.Fatalf("AuthorizeProject(%s): %v", action, err)
		}
		if decision.Allowed {
			t.Fatalf("归档项目不得允许 %s（只阻止新运行，但写操作同样应关闭）", action)
		}
	}
}

// TestAuthzMissingProjectAndNonMemberAreIndistinguishable 覆盖契约 §1.2 的
// 「资源隐藏型 404」：不存在的项目与非成员必须是**同一种**判定结果。
//
// 若两者可区分，攻击者就能通过遍历 ID 探测「哪些项目存在」。
func TestAuthzMissingProjectAndNonMemberAreIndistinguishable(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	nonMember, err := fixture.authz.AuthorizeProject(ctx, fixture.projectID, fixture.outsiderID, AuthzRead)
	if err != nil {
		t.Fatalf("AuthorizeProject(non-member): %v", err)
	}
	missing, err := fixture.authz.AuthorizeProject(ctx, int64(1<<62), fixture.outsiderID, AuthzRead)
	if err != nil {
		t.Fatalf("AuthorizeProject(missing): %v", err)
	}

	if nonMember.Allowed || missing.Allowed {
		t.Fatal("两种情况都必须拒绝")
	}
	if nonMember.IsMember || missing.IsMember {
		t.Fatal("两种情况都必须 IsMember=false，调用方才能统一返回资源隐藏型 404")
	}
	// 文案也必须一致：不同文案本身就是可探测的信号。
	if nonMember.Reason(AuthzRead) != missing.Reason(AuthzRead) {
		t.Fatalf("两种情况的文案必须一致（否则可用于探测项目是否存在）：%q vs %q",
			nonMember.Reason(AuthzRead), missing.Reason(AuthzRead))
	}
}

// TestAuthzMemberUpsertRejectsUnknownRoleAndUser 覆盖成员命令的输入校验。
func TestAuthzMemberUpsertRejectsUnknownRoleAndUser(t *testing.T) {
	fixture := newAuthzFixture(t)
	ctx := context.Background()

	err := fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.viewerID, "superuser", "测试", "req-test")
	if !IsStoreValidationError(err) {
		t.Fatalf("未知角色必须被拒绝（不得静默降级为 viewer），实际: %v", err)
	}

	err = fixture.authz.UpsertProjectMember(ctx, fixture.projectID, fixture.ownerID, 0, model.ProjectRoleViewer, "测试", "req-test")
	if !IsStoreValidationError(err) {
		t.Fatalf("未指定成员必须被拒绝，实际: %v", err)
	}

	// 移除不存在的成员必须是 pgx.ErrNoRows（handler 据此返回 404）。
	err = fixture.authz.RemoveProjectMember(ctx, fixture.projectID, fixture.ownerID, fixture.outsiderID, "测试", "req-test")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("移除不存在的成员必须返回 pgx.ErrNoRows，实际: %v", err)
	}
}

// TestNormalizeReasonLimitsLength 覆盖理由的归一化。
//
// 不限长会允许一次请求写入任意大小的一行（也容易被用来撑大审计表）。
func TestNormalizeReasonLimitsLength(t *testing.T) {
	if got := NormalizeReason("  参与审阅  "); got != "参与审阅" {
		t.Fatalf("理由必须去空白，实际 %q", got)
	}
	long := ""
	for i := 0; i < 600; i++ {
		long += "字"
	}
	if got := NormalizeReason(long); len([]rune(got)) != 500 {
		t.Fatalf("理由必须限长 500 个字符，实际 %d", len([]rune(got)))
	}
	// 按 rune 截断而不是 byte：按 byte 截断会把中文切成无效 UTF-8。
	if got := NormalizeReason(string([]rune("中")[0]) + long); len([]rune(got)) != 500 {
		t.Fatalf("截断必须按字符，实际 %d", len([]rune(got)))
	}
}

// LooksLikeCheckViolation 判断错误是否为 Postgres check_violation（SQLSTATE 23514）。
//
// 放在测试文件里：它只服务于测试断言（生产代码用 pgconn 的 PgError 类型判断）。
func LooksLikeCheckViolation(err error) bool {
	if err == nil {
		return false
	}
	type sqlStater interface{ SQLState() string }
	var state sqlStater
	if errors.As(err, &state) {
		return state.SQLState() == "23514"
	}
	return false
}
