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

// stageWorkbenchArrayNames 是声明阶段工作台页面（及其侧边栏归属）的数组。
var stageWorkbenchArrayNames = []string{"taskWorkbenchPages", "resultWorkbenchPages"}

// TestStageNavMapMustBeDerived 断言阶段路由的侧边栏归属由单一来源派生。
//
// issue #61 的成因之一是同一个「阶段路由归属」被写了三份（路由表 / 侧边栏高亮 /
// 面包屑）。修好路由后，只要 stageRouteNavMap 还能被手写成常量表，
// 下一次改路由就会再次漂移。因此这里锁定「必须是派生」这一结构。
func TestStageNavMapMustBeDerived(t *testing.T) {
	source := readAppSource(t)

	block := regexp.MustCompile(`(?s)const\s+stageRouteNavMap[^=]*=\s*(.*?)\n\n`).FindStringSubmatch(source)
	if block == nil {
		t.Fatal("未找到 stageRouteNavMap 定义，断言失效")
	}
	expression := block[1]

	for _, arrayName := range stageWorkbenchArrayNames {
		if !strings.Contains(expression, arrayName) {
			t.Errorf("stageRouteNavMap 的取值表达式未引用 %s：\n%s\n"+
				"  阶段路由归属必须从阶段工作台声明派生，否则会与路由表再次漂移（见 issue #61）",
				arrayName, strings.TrimSpace(expression))
		}
	}
	if !strings.Contains(expression, "navParent") {
		t.Errorf("stageRouteNavMap 的派生未使用 navParent：\n%s",
			strings.TrimSpace(expression))
	}
}

// TestStageWorkbenchPagesMustDeclareNavParent 断言每个阶段工作台页面都声明了
// navParent，且该父项是真实存在的侧边栏路由。
//
// navParent 决定了处于某个阶段页时侧边栏高亮哪一项。缺失或指向不存在的路由，
// 用户就会看到侧边栏高亮到不相干的默认项。
func TestStageWorkbenchPagesMustDeclareNavParent(t *testing.T) {
	source := readAppSource(t)

	// 收集侧边栏（userPages）已声明的路由，作为合法的归属父项。
	sidebarBlock := regexp.MustCompile(`(?s)const\s+userPages[^=]*=\s*\[(.*?)\n\]`).FindStringSubmatch(source)
	if sidebarBlock == nil {
		t.Fatal("未找到 userPages 声明，断言失效")
	}
	sidebarRoutes := map[string]bool{}
	for _, m := range regexp.MustCompile(`route:\s*'([^']+)'`).FindAllStringSubmatch(sidebarBlock[1], -1) {
		sidebarRoutes[m[1]] = true
	}
	if len(sidebarRoutes) == 0 {
		t.Fatal("未从 userPages 解析出任何路由，断言失效")
	}

	pages := 0
	for _, arrayName := range stageWorkbenchArrayNames {
		block := regexp.MustCompile(
			`(?s)const\s+` + arrayName + `[^=]*=\s*\[(.*?)\n\]`).FindStringSubmatch(source)
		if block == nil {
			t.Fatalf("未找到 %s 声明，断言失效", arrayName)
		}

		// 以对象为单位切分，避免 route 与 navParent 跨条目错位配对。
		entries := regexp.MustCompile(`\{([^{}]*)\}`).FindAllStringSubmatch(block[1], -1)
		if len(entries) == 0 {
			t.Fatalf("%s 中未解析出任何条目", arrayName)
		}
		for _, entry := range entries {
			pages++
			route := regexp.MustCompile(`route:\s*'([^']+)'`).FindStringSubmatch(entry[1])
			parent := regexp.MustCompile(`navParent:\s*'([^']+)'`).FindStringSubmatch(entry[1])
			if route == nil {
				t.Errorf("%s 中有条目缺少 route", arrayName)
				continue
			}
			if parent == nil {
				t.Errorf("阶段工作台页面 %s 缺少 navParent：\n  处于该阶段时侧边栏会高亮到默认项",
					route[1])
				continue
			}
			if !sidebarRoutes[parent[1]] {
				t.Errorf("阶段工作台页面 %s 的 navParent=%s 不是 userPages 中声明的侧边栏路由",
					route[1], parent[1])
			}
		}
	}
	if pages == 0 {
		t.Fatal("未解析出任何阶段工作台页面，断言失效")
	}
}

// stageRouteNavParent 是 5 个阶段路由各自的**期望**侧边栏归属，来自
// docs/plans/issue-remediation-plan.md §1.4 与 test/l15_stage_routes.mjs 的冻结表。
//
// 为什么要单独断言「取值」而不只是「合法性」：
// TestStageWorkbenchPagesMustDeclareNavParent 只检查 navParent 指向 userPages 里
// 真实存在的路由。而 /console/tasks 与 /console/results 都是合法侧边栏项，因此把
// /console/domains 的归属错写成 /console/results 仍然「合法」—— 那正是 issue #61
// 的漂移形态本身（父代理用变异测试实测确认过：改错归属后本包测试仍然全绿）。
// 所以必须把期望值冻结在测试里。
var stageRouteNavParent = map[string]string{
	"/console/domains":   "/console/tasks",
	"/console/questions": "/console/results",
	"/console/reasoning": "/console/results",
	"/console/rewards":   "/console/results",
	"/console/exports":   "/console/results",
}

// TestStageRouteNavParentValuesAreFrozen 断言每个阶段路由的 navParent 取值正确。
//
// 这条断言与 test/l15_stage_routes.mjs 的「阶段声明的 route→navParent/label 与契约
// 冻结值一一对应」是同一不变量的两个入口：Go 侧让 CI 的 Backend job 能拦住它，
// .mjs 侧让渲染级证据也能拦住它。两侧都必须独立成立，不能互相替代。
func TestStageRouteNavParentValuesAreFrozen(t *testing.T) {
	source := readAppSource(t)

	// route -> navParent，按对象切分以免 route 与 navParent 跨条目错位配对。
	actual := map[string]string{}
	for _, arrayName := range stageWorkbenchArrayNames {
		block := regexp.MustCompile(
			`(?s)const\s+` + arrayName + `[^=]*=\s*\[(.*?)\n\]`).FindStringSubmatch(source)
		if block == nil {
			t.Fatalf("未找到 %s 声明，断言失效", arrayName)
		}
		for _, entry := range regexp.MustCompile(`\{([^{}]*)\}`).FindAllStringSubmatch(block[1], -1) {
			route := regexp.MustCompile(`route:\s*'([^']+)'`).FindStringSubmatch(entry[1])
			parent := regexp.MustCompile(`navParent:\s*'([^']+)'`).FindStringSubmatch(entry[1])
			if route == nil || parent == nil {
				continue
			}
			actual[route[1]] = parent[1]
		}
	}

	// 5 个阶段路由必须全部出现在声明里，缺一个就说明阶段工作台声明被删减。
	for route, wantParent := range stageRouteNavParent {
		gotParent, ok := actual[route]
		if !ok {
			t.Errorf("阶段路由 %s 未在阶段工作台声明中出现（期望 navParent=%s）", route, wantParent)
			continue
		}
		if gotParent != wantParent {
			t.Errorf("阶段路由 %s 的 navParent=%s，期望 %s：\n"+
				"  归属错误会让处于该阶段的用户看到侧边栏高亮到不相干的项（issue #61 的漂移形态）",
				route, gotParent, wantParent)
		}
	}

	// 反向：阶段工作台不得声明冻结表之外的阶段路由，否则冻结表本身已过期。
	for route := range actual {
		if _, ok := stageRouteNavParent[route]; !ok {
			t.Errorf("阶段工作台声明了冻结表之外的路由 %s：请同步更新 stageRouteNavParent 与本测试的契约来源", route)
		}
	}
}
