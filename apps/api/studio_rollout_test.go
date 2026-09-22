package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件验证 Issue #160 T33 在 HTTP 层的两条语义：
//
//  1. 回退开关关闭时，**项目创建**必须被拒绝（503 + 明确文案），
//     且拒绝发生在解析/落库之前；
//  2. 运维状态端点**只对管理员**开放（它含成本与回退原因）。
//
// 刻意不使用数据库：这两条判据都是「在碰数据库之前」发生的，
// 用真库只会让测试依赖一个与断言无关的前置条件。

func rolloutTestApp(rollout studio.Rollout) *application {
	return &application{studio: studio.NewWithRollout(nil, rollout)}
}

func requestWithUser(request *http.Request, role string) *http.Request {
	user := model.User{ID: 1, Email: "ops@example.test", Role: role}
	return request.WithContext(context.WithValue(request.Context(), userContextKey, user))
}

// TestCreateProjectRejectedWhenStudioDisabled 覆盖回退开关的写入侧语义。
func TestCreateProjectRejectedWhenStudioDisabled(t *testing.T) {
	app := rolloutTestApp(studio.Rollout{DisabledGlobally: true})
	request := requestWithUser(httptest.NewRequest(http.MethodPost, "/api/v1/projects",
		strings.NewReader(`{"name":"被暂停时的项目","goal":"x","targetKind":"sft"}`)), "user")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	app.createProject(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("回退时必须返回 503，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, studio.CodeUnavailable) {
		t.Fatalf("错误码必须是 %s（可重试语义），实际 %s", studio.CodeUnavailable, body)
	}
	if !strings.Contains(body, "STUDIO_ENABLED") {
		t.Fatalf("错误文案必须点明是哪个开关导致的，实际 %s", body)
	}
	// 项目名不得出现在响应里（拒绝发生在解析之前，因此没有回显用户输入）。
	if strings.Contains(body, "被暂停时的项目") {
		t.Fatalf("拒绝响应不应回显请求体（说明它发生在解析之前而不是之后）：%s", body)
	}
}

// TestCreateProjectAllowedWhenStudioEnabled 覆盖默认启用时开关不拦人。
//
// 用一个必然失败在**后续**步骤的请求（未注入 store 的 application）来区分
// 「开关放行」与「开关拦截」：放行时不会返回 503。
func TestCreateProjectAllowedWhenStudioEnabled(t *testing.T) {
	app := rolloutTestApp(studio.Rollout{})
	request := requestWithUser(httptest.NewRequest(http.MethodPost, "/api/v1/projects",
		strings.NewReader(`{`)), "user")
	recorder := httptest.NewRecorder()

	defer func() {
		// 后续步骤会因缺少依赖而 panic；这正是「开关放行了」的证据。
		_ = recover()
	}()
	app.createProject(recorder, request)
	if recorder.Code == http.StatusServiceUnavailable {
		t.Fatalf("默认启用的开关不得拦下项目创建，实际 %d", recorder.Code)
	}
}

// TestStudioRolloutEndpointRequiresAdmin 覆盖运维状态端点的权限。
func TestStudioRolloutEndpointRequiresAdmin(t *testing.T) {
	app := rolloutTestApp(studio.Rollout{DisabledGlobally: true})

	recorder := httptest.NewRecorder()
	app.getStudioRollout(recorder, requestWithUser(
		httptest.NewRequest(http.MethodGet, "/api/v1/studio/rollout", nil), "user"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("非管理员必须被拒绝（403），实际 %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	app.getStudioRollout(recorder, requestWithUser(
		httptest.NewRequest(http.MethodGet, "/api/v1/studio/rollout", nil), "admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("管理员应能读取开关状态，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"enabled":false`) {
		t.Fatalf("状态必须如实反映总开关关闭，实际 %s", recorder.Body.String())
	}

	// 健康快照同样是管理员专属（在读取数据库之前就该拒绝）。
	recorder = httptest.NewRecorder()
	app.getStudioHealth(recorder, requestWithUser(
		httptest.NewRequest(http.MethodGet, "/api/v1/studio/health", nil), "user"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("非管理员必须被拒绝（403），实际 %d", recorder.Code)
	}
}

// TestStudioRolloutFromConfigParsesReasons 覆盖配置到开关的映射。
func TestStudioRolloutFromConfigParsesReasons(t *testing.T) {
	// 总开关关闭：任何项目都被挡（且原因是总开关）。
	globalOff := studioRolloutFromConfig(config.APIConfig{
		StudioEnabled:             false,
		StudioDisabledProjectsRaw: "12:发布积压,13",
		StudioDisabledProjectIDs:  []int64{12, 13},
	})
	if blocked, reason := globalOff.BlockedFor(99); !blocked || !strings.Contains(reason, "STUDIO_ENABLED") {
		t.Fatalf("总开关关闭时任何项目都应被挡且原因是总开关，实际 blocked=%v reason=%q", blocked, reason)
	}

	// 总开关开启、仅项目级名单：原因必须从配置带到运行时。
	scoped := studioRolloutFromConfig(config.APIConfig{
		StudioEnabled:             true,
		StudioDisabledProjectsRaw: "12:发布积压,13",
		StudioDisabledProjectIDs:  []int64{12, 13},
	})
	if blocked, _ := scoped.BlockedFor(12); !blocked {
		t.Fatal("名单内项目必须被挡")
	}
	if _, reason := scoped.BlockedFor(12); !strings.Contains(reason, "发布积压") {
		t.Fatalf("回退原因必须从配置带到运行时，实际 %q", reason)
	}
	if blocked, _ := scoped.BlockedFor(13); !blocked {
		t.Fatal("无原因的项目也必须被挡（空原因不等于没被挡）")
	}

	// 原始写法解析不出 ID 时回退到已解析列表（两处口径必须一致）。
	fallback := studioRolloutFromConfig(config.APIConfig{
		StudioEnabled:             true,
		StudioDisabledProjectsRaw: "不是ID",
		StudioDisabledProjectIDs:  []int64{5},
	})
	if blocked, _ := fallback.BlockedFor(5); !blocked {
		t.Fatal("原始写法非法时必须回退到已解析的列表，否则配置会静默失效")
	}
}
