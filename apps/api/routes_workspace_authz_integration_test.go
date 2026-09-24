package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the HTTP scope boundary against a real Postgres. The
// handler tests are intentionally separate from the pure route-registration
// guards: a route can be mounted correctly while still authorizing one
// workspace and writing another.

type workspaceAuthzIntegrationFixture struct {
	pool        *pgxpool.Pool
	app         *application
	workspaceA  int64
	workspaceB  int64
	actor       int64
	other       int64
	secondOwner int64
	suffix      string
}

func newWorkspaceAuthzIntegrationFixture(t *testing.T) *workspaceAuthzIntegrationFixture {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 workspace 授权测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	fixture := &workspaceAuthzIntegrationFixture{
		pool: pool,
		app: &application{
			projects:    store.NewProjectStore(pool),
			authz:       store.NewAuthzStore(pool),
			recipes:     store.NewRecipeStore(pool),
			idempotency: store.NewIdempotencyStore(pool),
			documents:   store.NewDocumentStore(pool),
			studio:      studio.NewWithRollout(pool, studio.Rollout{}),
		},
	}
	fixture.workspaceA = seedWorkspaceAuthzWorkspace(t, pool, "A", suffix)
	fixture.workspaceB = seedWorkspaceAuthzWorkspace(t, pool, "B", suffix)
	fixture.actor = seedWorkspaceAuthzUser(t, pool, "actor", suffix)
	fixture.other = seedWorkspaceAuthzUser(t, pool, "other", suffix)
	fixture.secondOwner = seedWorkspaceAuthzUser(t, pool, "second-owner", suffix)
	if _, err := pool.Exec(ctx, `
    INSERT INTO workspace_members (workspace_id, user_id, role, created_by)
    VALUES ($1, $2, 'admin', $2)`, fixture.workspaceA, fixture.actor); err != nil {
		t.Fatalf("seed actor workspace membership: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		// audit_logs has non-cascading workspace/project references, so clear it
		// before deleting the fixture objects.
		_, _ = pool.Exec(cleanup, `DELETE FROM audit_logs WHERE workspace_id IN ($1, $2)`, fixture.workspaceA, fixture.workspaceB)
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE workspace_id IN ($1, $2)`, fixture.workspaceA, fixture.workspaceB)
		_, _ = pool.Exec(cleanup, `DELETE FROM recipes WHERE workspace_id IN ($1, $2)`, fixture.workspaceA, fixture.workspaceB)
		_, _ = pool.Exec(cleanup, `DELETE FROM workspace_members WHERE workspace_id IN ($1, $2)`, fixture.workspaceA, fixture.workspaceB)
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id IN ($1, $2)`, fixture.workspaceA, fixture.workspaceB)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id IN ($1, $2, $3)`, fixture.actor, fixture.other, fixture.secondOwner)
	})
	return fixture
}

func seedWorkspaceAuthzWorkspace(t *testing.T, pool *pgxpool.Pool, label, suffix string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"workspace authz "+label, "workspace-authz-"+label+"-"+suffix).Scan(&id); err != nil {
		t.Fatalf("seed workspace %s: %v", label, err)
	}
	return id
}

func seedWorkspaceAuthzUser(t *testing.T, pool *pgxpool.Pool, label, suffix string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"workspace-authz-"+label+"-"+suffix+"@example.test").Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", label, err)
	}
	return id
}

func projectCreateRequest(workspaceID int64, name string) *http.Request {
	body := fmt.Sprintf(`{"name":%q,"goal":"g","targetKind":"sft","workspaceId":%d,"pilotSize":1,"coverage":{"domains":1,"directionsPerDomain":1,"questionsPerDirection":1}}`, name, workspaceID)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// TestCreateProjectRequiresWorkspaceMembership prevents a non-member from
// creating a project in a known workspace and receiving owner capabilities.
func TestCreateProjectRequiresWorkspaceMembership(t *testing.T) {
	fixture := newWorkspaceAuthzIntegrationFixture(t)
	ctx := context.Background()

	request := projectCreateRequest(fixture.workspaceB, "非成员不得创建")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey,
		model.User{ID: fixture.actor, Email: "actor@example.test", Role: "user"}))
	recorder := httptest.NewRecorder()
	fixture.app.createProject(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("非工作区成员创建项目必须返回隐藏型 404，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var projectCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM projects WHERE workspace_id = $1 AND name = $2`,
		fixture.workspaceB, "非成员不得创建").Scan(&projectCount); err != nil {
		t.Fatalf("check rejected project: %v", err)
	}
	if projectCount != 0 {
		t.Fatal("被拒绝的非成员项目创建不得写入项目行")
	}

	// Membership is the only required workspace scope; an ordinary member may
	// create a project without being a workspace administrator.
	if _, err := fixture.pool.Exec(ctx, `
    INSERT INTO workspace_members (workspace_id, user_id, role, created_by)
    VALUES ($1, $2, 'member', $2)`, fixture.workspaceB, fixture.actor); err != nil {
		t.Fatalf("seed ordinary member: %v", err)
	}
	request = projectCreateRequest(fixture.workspaceB, "成员可以创建")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey,
		model.User{ID: fixture.actor, Email: "actor@example.test", Role: "user"}))
	recorder = httptest.NewRecorder()
	fixture.app.createProject(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("工作区成员创建项目应成功，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestWorkspaceMemberUpsertCannotChangeAuthorizedScope prevents a request
// authorized for workspace A from changing workspace B via body.workspaceId.
func TestWorkspaceMemberUpsertCannotChangeAuthorizedScope(t *testing.T) {
	fixture := newWorkspaceAuthzIntegrationFixture(t)
	ctx := context.Background()
	body := fmt.Sprintf(`{"workspaceId":%d,"userId":%d,"role":"member"}`, fixture.workspaceB, fixture.other)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace/members?workspaceId="+fmt.Sprint(fixture.workspaceA), strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey,
		model.User{ID: fixture.actor, Email: "actor@example.test", Role: "user"}))
	recorder := httptest.NewRecorder()
	fixture.app.upsertWorkspaceMember(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("query/body 工作区不一致必须返回 422，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var present bool
	if err := fixture.pool.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id = $1 AND user_id = $2)`,
		fixture.workspaceB, fixture.other).Scan(&present); err != nil {
		t.Fatalf("check cross-workspace write: %v", err)
	}
	if present {
		t.Fatal("query/body scope 冲突不得写入另一个工作区")
	}
}

// TestProjectIdempotencyReplayRechecksMembership verifies that a stale
// idempotency key cannot be used as a permission bypass after project access
// is revoked.
func TestProjectIdempotencyReplayRechecksMembership(t *testing.T) {
	fixture := newWorkspaceAuthzIntegrationFixture(t)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `
    INSERT INTO workspace_members (workspace_id, user_id, role, created_by)
    VALUES ($1, $2, 'member', $2), ($1, $3, 'member', $3)`,
		fixture.workspaceB, fixture.actor, fixture.secondOwner); err != nil {
		t.Fatalf("seed project workspace members: %v", err)
	}
	request := projectCreateRequest(fixture.workspaceB, "撤权后不得回放")
	request.Header.Set("Idempotency-Key", "replay-revoke-"+fixture.suffix)
	request = request.WithContext(context.WithValue(request.Context(), userContextKey,
		model.User{ID: fixture.actor, Email: "actor@example.test", Role: "user"}))
	recorder := httptest.NewRecorder()
	fixture.app.createProject(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("初次创建应成功，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var projectID int64
	if err := fixture.pool.QueryRow(ctx, `
    SELECT id FROM projects WHERE workspace_id = $1 AND name = $2`,
		fixture.workspaceB, "撤权后不得回放").Scan(&projectID); err != nil {
		t.Fatalf("find created project: %v", err)
	}
	authz := store.NewAuthzStore(fixture.pool)
	if err := authz.UpsertProjectMember(ctx, projectID, fixture.actor, fixture.secondOwner,
		model.ProjectRoleOwner, "保留项目负责人", "replay-test"); err != nil {
		t.Fatalf("add second owner: %v", err)
	}
	if err := authz.RemoveProjectMember(ctx, projectID, fixture.actor, fixture.actor,
		"撤销访问", "replay-test"); err != nil {
		t.Fatalf("revoke actor: %v", err)
	}

	request = projectCreateRequest(fixture.workspaceB, "撤权后不得回放")
	request.Header.Set("Idempotency-Key", "replay-revoke-"+fixture.suffix)
	request = request.WithContext(context.WithValue(request.Context(), userContextKey,
		model.User{ID: fixture.actor, Email: "actor@example.test", Role: "user"}))
	recorder = httptest.NewRecorder()
	fixture.app.createProject(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("撤权后幂等回放必须返回隐藏型 404，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}

	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("error response must be JSON: %v", err)
	}
}
