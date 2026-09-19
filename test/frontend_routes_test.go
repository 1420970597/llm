// 前端路由接线守卫。
//
// 背景：5b90c2e 把 5 个任务阶段路由改成 <Navigate to={activeTaskDetailRoute}>
// 自指重定向后，renderDomains() 与 renderRecordPage() 失去全部路由引用，
// 生成链路在 UI 上完全不可达，任务永远停在 draft（issue #61）。
//
// 这类缺陷在 CI 现有的 tsc + vite build 下完全不可见：路由指向死代码
// 仍是合法 TypeScript，构建照样通过。因此这里用源码级断言兜住：
//
//  1. 侧边栏声明的每个阶段路由，必须由真实页面渲染，不能是重定向；
//  2. App.tsx 中定义的 render* 渲染函数必须至少被引用一次，不允许死代码。
//
// 断言刻意保持「结构性」而非「精确文本」，避免因无关改动而误报。
package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const appSourcePath = "../apps/web-user/src/App.tsx"

func readAppSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(appSourcePath))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", appSourcePath, err)
	}
	return string(raw)
}

// stageRoutes 是需要保证可达的阶段路由。
//
// 与 App.tsx 中 taskWorkbenchPages / resultWorkbenchPages 的 route 字段一致，
// 并额外覆盖任务详情页阶段卡片使用的同一批路径。
var stageRoutes = []string{
	"/console/domains",
	"/console/questions",
	"/console/reasoning",
	"/console/rewards",
	"/console/exports",
}

// TestStageRoutesMustRenderPagesNotRedirect 断言阶段路由不被重定向吞掉。
//
// 若某条阶段路由指向 <Navigate>，用户点击阶段入口后会被送回原页面
// （或任务列表），表现为「按钮点了没反应」，流水线无法推进。
func TestStageRoutesMustRenderPagesNotRedirect(t *testing.T) {
	source := readAppSource(t)

	for _, route := range stageRoutes {
		t.Run(strings.TrimPrefix(route, "/console/"), func(t *testing.T) {
			// 抓取该 path 的整条 <Route ... />，Route 可能跨多行。
			pattern := regexp.MustCompile(
				`<Route\s+path="` + regexp.QuoteMeta(route) + `"\s+element=\{(?s).*?\}\s*/>`,
			)
			matches := pattern.FindAllString(source, -1)
			if len(matches) == 0 {
				t.Fatalf("未找到路由 %s 的 <Route> 声明；"+
					"该阶段页已从路由表移除，用户将无法到达", route)
			}
			for _, m := range matches {
				if strings.Contains(m, "<Navigate") {
					t.Errorf("阶段路由 %s 被重定向吞掉：\n%s\n"+
						"阶段路由必须渲染对应页面（renderDomains/renderRecordPage 包装函数），"+
						"否则生成链路在 UI 上不可达（见 issue #61）", route, strings.TrimSpace(m))
				}
			}
		})
	}
}

// TestRenderHelpersMustBeReferenced 断言没有渲染函数沦为死代码。
//
// renderDomains 与 renderRecordPage 曾因路由改动失去全部引用，
// 其内部按钮（如「生成方向结构」）因此完全不可达。
func TestRenderHelpersMustBeReferenced(t *testing.T) {
	source := readAppSource(t)

	// 匹配形如 `  const renderXxx = ...` 的定义。
	defPattern := regexp.MustCompile(`(?m)^\s*const\s+(render[A-Za-z0-9_]+)\s*=`)
	defs := defPattern.FindAllStringSubmatch(source, -1)
	if len(defs) == 0 {
		t.Fatal("未在 App.tsx 中找到任何 render* 定义，断言失效，请检查源码结构是否变化")
	}

	for _, def := range defs {
		name := def[1]
		t.Run(name, func(t *testing.T) {
			// 统计调用次数：定义行写的是 `const renderXxx = ...`，不含
			// `renderXxx(`，因此被正确接线到 <Route> 的渲染函数计数恰为 1。
			used := strings.Count(source, name+"(")
			if used < 1 {
				t.Errorf("渲染函数 %s 被定义但未被引用（出现 %d 次）：\n"+
					"  这通常意味着其路由被移除或改成了重定向，页面内操作将无法触达\n"+
					"  修复方式：让对应 <Route> 渲染该函数，或删除该函数与相关死代码",
					name, used)
			}
		})
	}
}

// TestWorkbenchNavRoutesAreReachable 断言侧边栏声明的阶段入口都有真实路由。
//
// 侧边栏与阶段卡片是用户进入各阶段的唯一入口，若其 route 在路由表中
// 缺失或被重定向，用户就会「点了没反应」。
func TestWorkbenchNavRoutesAreReachable(t *testing.T) {
	source := readAppSource(t)

	// 从 taskWorkbenchPages / resultWorkbenchPages 两个数组中提取 route。
	navBlockPattern := regexp.MustCompile(
		`(?s)const (?:taskWorkbenchPages|resultWorkbenchPages)[^=]*=\s*\[(.*?)\n\]`,
	)
	blocks := navBlockPattern.FindAllStringSubmatch(source, -1)
	if len(blocks) == 0 {
		t.Fatal("未找到 taskWorkbenchPages / resultWorkbenchPages 声明，断言失效")
	}

	routePattern := regexp.MustCompile(`route:\s*'([^']+)'`)
	found := 0
	for _, block := range blocks {
		for _, m := range routePattern.FindAllStringSubmatch(block[1], -1) {
			found++
			route := m[1]
			t.Run(strings.TrimPrefix(route, "/console/"), func(t *testing.T) {
				rp := regexp.MustCompile(
					`<Route\s+path="` + regexp.QuoteMeta(route) + `"`,
				)
				if !rp.MatchString(source) {
					t.Errorf("侧边栏/阶段卡片声明的路由 %s 在路由表中不存在", route)
				}
			})
		}
	}
	if found == 0 {
		t.Fatal("未从工作台导航定义中解析出任何 route，断言失效")
	}
}
