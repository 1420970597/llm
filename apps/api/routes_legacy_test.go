package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5/pgxpool"
)

// studioForTest 构造一个只用于判定逻辑的 service（不需要数据库连接）。
//
// 零值 Rollout = 全部启用，而冻结判定用的是 cfg.LegacyWritesFrozen，
// 两者互不影响 —— 测试要能分别验证，不能把它们绑在一起。
func studioForTest() *studio.Service {
	return studio.NewWithRollout(nil, studio.Rollout{})
}

// legacyRouteTestApp 构造带真实连接池的 application（映射查询需要它）。
func legacyRouteTestApp(pool *pgxpool.Pool) *application {
	return &application{studio: studio.NewWithRollout(pool, studio.Rollout{})}
}

// 本文件验证 Issue #160 T31 在 HTTP 层的两个机制：
//
//  1. **旧写入口冻结**：判定是纯函数（`isLegacyWriteRequest`），
//     因此可以脱离数据库断言边界 —— 而边界正是最容易写错的地方
//     （漏一个前缀 = 迁移期间旧库仍被写）。
//  2. **旧 → 新映射**：未映射时返回 200 + `not_mapped`，而不是 404。
//     T31 要求「无法确定对象的阶段入口保留只读历史列表，不跳错项目」，
//     而 404 会让前端把它当成错误页，从而丢掉「这是历史资产」的语义。

// TestIsLegacyWriteRequestBoundaries 覆盖冻结判定的边界。
func TestIsLegacyWriteRequestBoundaries(t *testing.T) {
	writes := []string{
		"POST /api/v1/datasets",
		"PUT /api/v1/datasets/12",
		"POST /api/v1/datasets/12/domains",
		"PATCH /api/v1/questions/5",
		"DELETE /api/v1/reasoning/9",
		"POST /api/v1/grpo/generate",
		"PUT /api/v1/chain-standards/3",
	}
	for _, item := range writes {
		parts := strings.SplitN(item, " ", 2)
		if !isLegacyWriteRequest(parts[0], parts[1]) {
			t.Errorf("%s 必须判定为旧写入口", item)
		}
	}

	allowed := []struct{ method, path string }{
		// 读操作永远不被冻结（用户要能把历史数据取走）。
		{"GET", "/api/v1/datasets/12"},
		{"GET", "/api/v1/export/formats"},
		// 新主线由 STUDIO_ENABLED 管，不归这个开关。
		{"POST", "/api/v1/projects"},
		{"POST", "/api/v1/projects/3/batches"},
		{"POST", "/api/v1/studio/rollout"},
		// 治理面：迁移期间运维仍要改 provider / 存储 / 提示词。
		{"POST", "/api/v1/admin/providers"},
		{"PUT", "/api/v1/admin/storage-profiles"},
		// 前缀相似但不是同一段路径（必须按段边界判定，不能按字符串前缀）。
		{"POST", "/api/v1/datasetsx"},
		{"POST", "/api/v1/evaluation"},
	}
	for _, item := range allowed {
		if isLegacyWriteRequest(item.method, item.path) {
			t.Errorf("%s %s 不应被冻结", item.method, item.path)
		}
	}
}

// TestLegacyWritesFrozenMiddlewareRejectsWrites 覆盖中间件层的冻结行为。
func TestLegacyWritesFrozenMiddlewareRejectsWrites(t *testing.T) {
	app := &application{
		cfg:    config.APIConfig{LegacyWritesFrozen: true},
		studio: studioForTest(),
	}
	handler := app.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 不该走到这里
	}))

	recorder := httptest.NewRecorder()
	// 先置入已登录用户：中间件先做认证再做冻结判定，
	// 未登录应该得到 401（而不是先知道「系统正在迁移」这个运维状态）。
	req := requestWithUser(httptest.NewRequest(http.MethodPost, "/api/v1/datasets", nil), "user")
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("冻结时旧写入口必须返回 409，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "旧入口已冻结") {
		t.Fatalf("拒绝文案必须说明这是迁移而不是故障，实际 %s", recorder.Body.String())
	}

	// 未登录必须先得到 401（不泄露运维状态）。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/datasets", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录时必须先返回 401，实际 %d", recorder.Code)
	}

	// 读请求必须放行到下游。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, requestWithUser(
		httptest.NewRequest(http.MethodGet, "/api/v1/datasets/12", nil), "user"))
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("读请求必须放行，实际 %d", recorder.Code)
	}
}

// TestLegacyDatasetProjectMapping 覆盖映射端点的两个分支（真实 Postgres）。
func TestLegacyDatasetProjectMapping(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	// 唯一键里带**运行标签**：slug/email 这类唯一键如果在两次运行之间复用，
	// 上一次残留（清理不完整时）会让第二次运行直接 23505 失败 ——
	// 而那种失败与断言对象毫无关系，只在「同一个库上连跑两遍」时出现
	//（T32 的集成门禁要求连跑不互相污染，因此必须避免）。
	suffix := strings.ReplaceAll(t.Name(), "/", "_")
	runTag := suffix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var workspaceID, userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"legacy-route-"+suffix, "legacy-route-"+runTag).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"legacy-route-"+runTag+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var datasetID, mappedDatasetID int64
	if err := pool.QueryRow(ctx, `
    WITH created AS (INSERT INTO datasets (name, root_keyword) VALUES ($1, 'x') RETURNING id)
    SELECT id FROM created`, "legacy-route-"+suffix).Scan(&datasetID); err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	if err := pool.QueryRow(ctx, `
    WITH created AS (INSERT INTO datasets (name, root_keyword) VALUES ($1, 'x') RETURNING id)
    SELECT id FROM created`, "legacy-route-mapped-"+suffix).Scan(&mappedDatasetID); err != nil {
		t.Fatalf("seed mapped dataset: %v", err)
	}
	projects := store.NewProjectStore(pool)
	input := model.CreateProjectInput{
		Name: "legacy-route-project-" + suffix, Goal: "映射", TargetKind: model.TargetKindSFT,
		LegacyDatasetID: &mappedDatasetID,
	}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		// 顺序很关键：audit_logs 用 workspace_id 引用工作区且**不是** CASCADE，
		// 先删工作区会外键失败（而 `_, _ =` 会把它静默吞掉，于是残留导致下次 23505）。
		// 这里按**稳定前缀**清理：连上以前运行留下的残留一起清掉。
		_, _ = pool.Exec(cleanup, `DELETE FROM audit_logs WHERE workspace_id IN
      (SELECT id FROM workspaces WHERE slug LIKE 'legacy-route-%')`)
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM datasets WHERE root_keyword = 'x' AND name LIKE $1`, "legacy-route-%"+suffix)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE email LIKE $1`, "legacy-route-%@example.test")
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
	})

	app := legacyRouteTestApp(pool)
	get := func(dataset string) legacyProjectMapping {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/legacy/datasets/"+dataset+"/project", nil)
		request.SetPathValue("datasetId", dataset)
		request = requestWithUser(request, "user")
		recorder := httptest.NewRecorder()
		app.getLegacyDatasetProject(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("dataset %s 的映射查询应返回 200，实际 %d（body=%s）", dataset, recorder.Code, recorder.Body.String())
		}
		var mapping legacyProjectMapping
		if err := json.Unmarshal(recorder.Body.Bytes(), &mapping); err != nil {
			t.Fatalf("decode mapping: %v", err)
		}
		return mapping
	}

	mapped := get(fmt.Sprint(mappedDatasetID))
	if mapped.MigrationStatus != "mapped" || mapped.ProjectID != project.ID {
		t.Fatalf("已映射的 dataset 必须给出项目 ID：%+v（期望 %d）", mapped, project.ID)
	}
	if !strings.Contains(mapped.PagePath, fmt.Sprint(project.ID)) {
		t.Fatalf("必须给出可跳转路径：%+v", mapped)
	}

	unmapped := get(fmt.Sprint(datasetID))
	if unmapped.MigrationStatus != "not_mapped" || unmapped.ProjectID != 0 {
		t.Fatalf("未映射的 dataset 必须返回 not_mapped 且不带项目 ID（不能猜一个）：%+v", unmapped)
	}
	if !strings.Contains(unmapped.Message, "只读历史") {
		t.Fatalf("未映射说明必须指向只读历史语义，实际 %q", unmapped.Message)
	}
}
