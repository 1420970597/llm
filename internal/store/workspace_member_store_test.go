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

// 本文件验证 Issue #160 T28 的工作区成员规则（真实 Postgres）。
//
// 三条规则都是「做错之后只能靠改数据库恢复」的状态，因此必须由测试冻结：
//  1. 最后一名工作区管理员不可移除/降级；
//  2. 仍是某项目最后一名 owner 的成员不可被移除（并列出阻塞项目）；
//  3. 移除成功时同时清理他在本工作区的项目成员关系（否则界面说「已移出」而数据仍可读）。

type memberFixture struct {
	pool        *pgxpool.Pool
	members     *WorkspaceMemberStore
	projects    *ProjectStore
	workspaceID int64
	adminA      int64
	adminB      int64
	member      int64
	suffix      string
}

func newMemberFixture(t *testing.T) *memberFixture {
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
	fixture := &memberFixture{
		pool: pool, members: NewWorkspaceMemberStore(pool), projects: NewProjectStore(pool), suffix: suffix,
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"成员测试工作区 "+suffix, "member-test-"+suffix).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	fixture.adminA = fixture.seedUser(t, "admin-a")
	fixture.adminB = fixture.seedUser(t, "admin-b")
	fixture.member = fixture.seedUser(t, "member")
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM workspace_members WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE email LIKE $1`, "member-"+suffix+"-%")
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id = $1`, fixture.workspaceID)
	})
	return fixture
}

func (fixture *memberFixture) seedUser(t *testing.T, label string) int64 {
	t.Helper()
	var userID int64
	if err := fixture.pool.QueryRow(context.Background(), `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"member-"+fixture.suffix+"-"+label+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user %s: %v", label, err)
	}
	return userID
}

// TestLastWorkspaceAdminCannotBeRemoved 覆盖规则 1。
func TestLastWorkspaceAdminCannotBeRemoved(t *testing.T) {
	fixture := newMemberFixture(t)
	ctx := context.Background()
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA,
		fixture.adminA, model.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("add admin A: %v", err)
	}
	// 只有一名管理员时：移除与降级都必须被拒绝。
	if err := fixture.members.RemoveWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA, fixture.adminA); !errors.Is(err, ErrLastWorkspaceAdmin) {
		t.Fatalf("移除最后一名管理员必须被拒绝，实际 %v", err)
	}
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA,
		fixture.adminA, model.WorkspaceRoleMember); !errors.Is(err, ErrLastWorkspaceAdmin) {
		t.Fatalf("降级最后一名管理员必须被拒绝，实际 %v", err)
	}
	// 有了第二名管理员之后，降级第一名是允许的。
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA,
		fixture.adminB, model.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("add admin B: %v", err)
	}
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, fixture.workspaceID, fixture.adminB,
		fixture.adminA, model.WorkspaceRoleMember); err != nil {
		t.Fatalf("有两名管理员时应允许降级：%v", err)
	}
}

// TestWorkspaceMemberStoreRejectsActorFromAnotherWorkspace freezes the store
// boundary as well as the HTTP handler boundary: an admin from workspace A
// must not be able to mutate workspace B merely by passing B's ID.
func TestWorkspaceMemberStoreRejectsActorFromAnotherWorkspace(t *testing.T) {
	fixture := newMemberFixture(t)
	ctx := context.Background()
	var otherWorkspaceID int64
	if err := fixture.pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"另一个成员测试工作区 "+fixture.suffix, "member-test-other-"+fixture.suffix).Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM workspace_members WHERE workspace_id = $1`, otherWorkspaceID)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, otherWorkspaceID)
	})

	// B has its own administrator; actor adminA is only an administrator in A.
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, otherWorkspaceID, fixture.adminB,
		fixture.adminB, model.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("seed other workspace admin: %v", err)
	}
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, otherWorkspaceID, fixture.adminA,
		fixture.member, model.WorkspaceRoleMember); !errors.Is(err, ErrWorkspaceMemberManageDenied) {
		t.Fatalf("跨工作区 actor 必须被拒绝，实际 %v", err)
	}
	var present bool
	if err := fixture.pool.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id = $1 AND user_id = $2)`,
		otherWorkspaceID, fixture.member).Scan(&present); err != nil {
		t.Fatalf("check unauthorized member: %v", err)
	}
	if present {
		t.Fatal("被拒绝的跨工作区写入不得留下成员关系")
	}
}

// TestRemoveWorkspaceMemberBlockedByLastProjectOwner 覆盖规则 2 与规则 3。
func TestRemoveWorkspaceMemberBlockedByLastProjectOwner(t *testing.T) {
	fixture := newMemberFixture(t)
	ctx := context.Background()
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA,
		fixture.adminA, model.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("add admin: %v", err)
	}
	if _, err := fixture.members.UpsertWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA,
		fixture.member, model.WorkspaceRoleMember); err != nil {
		t.Fatalf("add member: %v", err)
	}
	// 让 member 成为一个项目的 owner（CreateProject 会把创建者写成 owner）。
	project, err := fixture.projects.CreateProject(ctx, fixture.workspaceID, fixture.member,
		model.CreateProjectInput{Name: "成员项目 " + fixture.suffix, Goal: "g", TargetKind: model.TargetKindSFT})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// 他是该项目最后一名 owner → 拒绝并列出阻塞项目。
	err = fixture.members.RemoveWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA, fixture.member)
	var owns *ErrWorkspaceMemberOwnsProjects
	if !errors.As(err, &owns) {
		t.Fatalf("仍是最后一名 owner 时必须拒绝移除，实际 %v", err)
	}
	if len(owns.Projects) != 1 || owns.Projects[0].ProjectID != project.ID {
		t.Fatalf("阻塞列表必须点名具体项目，实际 %+v", owns.Projects)
	}
	if !strings.Contains(owns.Error(), "负责人") {
		t.Fatalf("错误必须给出可行动的说明，实际 %q", owns.Error())
	}

	// 指定另一位 owner 后即可移除，并且项目成员关系被一并清理。
	if err := fixture.authzUpsertOwner(t, project.ID, fixture.adminA); err != nil {
		t.Fatalf("add second owner: %v", err)
	}
	if err := fixture.members.RemoveWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA, fixture.member); err != nil {
		t.Fatalf("有第二名 owner 后应能移除：%v", err)
	}
	var stillMember bool
	if err := fixture.pool.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)`,
		project.ID, fixture.member).Scan(&stillMember); err != nil {
		t.Fatalf("check project member: %v", err)
	}
	if stillMember {
		t.Fatal("移除工作区成员必须同时清理其在本工作区的项目成员关系（否则界面说已移出而数据仍可读）")
	}
	// 再次移除应报告「不是成员」而不是静默成功。
	if err := fixture.members.RemoveWorkspaceMember(ctx, fixture.workspaceID, fixture.adminA, fixture.member); !errors.Is(err, ErrWorkspaceMemberNotBlocked) {
		t.Fatalf("重复移除应返回 ErrWorkspaceMemberNotBlocked，实际 %v", err)
	}
}

// authzUpsertOwner 用既有项目成员命令添加一名 owner（复用 T03 的实现而不是直接插表）。
func (fixture *memberFixture) authzUpsertOwner(t *testing.T, projectID, userID int64) error {
	t.Helper()
	return NewAuthzStore(fixture.pool).UpsertProjectMember(context.Background(), projectID, userID, userID,
		model.ProjectRoleOwner, "测试：避免单点负责人", "req-member-test")
}

// TestResolveUserIDByEmail 覆盖「首版只能选择已有账号」。
func TestResolveUserIDByEmail(t *testing.T) {
	fixture := newMemberFixture(t)
	ctx := context.Background()
	email := "member-" + fixture.suffix + "-member@example.test"
	resolved, err := fixture.members.ResolveUserIDByEmail(ctx, strings.ToUpper(email))
	if err != nil {
		t.Fatalf("已存在的账号（大小写不敏感）应能解析：%v", err)
	}
	if resolved != fixture.member {
		t.Fatalf("解析出的用户不对：%d vs %d", resolved, fixture.member)
	}
	if _, err := fixture.members.ResolveUserIDByEmail(ctx, "nobody-"+fixture.suffix+"@example.test"); err == nil {
		t.Fatal("不存在的账号必须被拒绝")
	} else if _, ok := model.HasFieldErrors(err); !ok {
		t.Fatalf("不存在账号应返回字段级错误（界面按字段提示），实际 %v", err)
	}
}
