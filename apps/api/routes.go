package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// 本文件是「路由注册表」，属于冻结契约的一部分（见 docs/plans/eval-and-cleaning-plan.md 第 1 节）。
//
// 设计目的：让各 lane 只通过 init() 注册自己的路由，无需修改 main.go，
// 从而保证多个 lane 并行开发时不会产生对同一文件的写冲突。
//
// lane 只允许注册**新前缀**（例如 /api/v1/eval/、/api/v1/cleaning/）。
// 严禁重复注册 main.go 中已存在的完整 pattern，否则 Go 1.22+ ServeMux 会 panic。

// routeRegistrar 顶层路由注册函数。
type routeRegistrar func(mux *http.ServeMux, app *application)

// datasetRouter 处理 /api/v1/datasets/{id}/{segment}/{rest...} 形式的请求。
//   - id      数据集 ID
//   - segment 注册时声明的路径段（例如 "chain-standards"）
//   - rest    segment 之后的剩余路径，可能为空（例如 "/12/versions"）
type datasetRouter func(w http.ResponseWriter, r *http.Request, id int64, rest string)

// datasetActionFunc 处理 /api/v1/datasets/{id}/<suffix> 形式的请求。
// 契约见 docs/plans/eval-and-cleaning-plan.md 第 1.1 节（冻结签名）。
type datasetActionFunc func(w http.ResponseWriter, r *http.Request, id int64)

var (
	routeMu         sync.RWMutex
	routeRegistrars []routeRegistrar
	datasetRouters  = map[string]datasetRouter{}
	// datasetGets/datasetActions 按 **suffix** 注册（可含多个路径段，例如
	// "questions/difficulty-stats"）。见 RegisterDatasetGet/RegisterDatasetAction。
	datasetGets    = map[string]datasetActionFunc{}
	datasetActions = map[string]datasetActionFunc{}
)

// RegisterRoutes 注册一组顶层路由。lane 在 init() 中调用。
func RegisterRoutes(registrar routeRegistrar) {
	if registrar == nil {
		return
	}
	routeMu.Lock()
	defer routeMu.Unlock()
	routeRegistrars = append(routeRegistrars, registrar)
}

// RegisterDatasetRouter 注册 /api/v1/datasets/{id}/{segment}/... 的子路由。
// segment 不得重复注册，也不得与 main.go 内置段（domains/questions/reasoning/rewards/export/pipeline）冲突。
func RegisterDatasetRouter(segment string, router datasetRouter) {
	segment = strings.Trim(segment, "/")
	if segment == "" || router == nil {
		return
	}
	routeMu.Lock()
	defer routeMu.Unlock()
	if _, exists := datasetRouters[segment]; exists {
		panic("duplicate dataset router segment: " + segment)
	}
	datasetRouters[segment] = router
}

// RegisterDatasetGet 注册 GET /api/v1/datasets/{id}/<suffix> 子动作。
//
// suffix 允许包含斜杠（例如 "questions/difficulty-stats"、"export/formats"），
// 这正是不允许注册到 questions/export/domains 等 legacy 段时的落地路径。
// 契约优先级：显式注册的 suffix > legacy switch。
func RegisterDatasetGet(suffix string, fn datasetActionFunc) {
	registerDatasetSuffix(datasetGets, "get", suffix, fn)
}

// RegisterDatasetAction 注册 POST /api/v1/datasets/{id}/<suffix> 子动作。
// 语义与 RegisterDatasetGet 相同，只作用于 POST。
func RegisterDatasetAction(suffix string, fn datasetActionFunc) {
	registerDatasetSuffix(datasetActions, "action", suffix, fn)
}

// registerDatasetSuffix 是两条注册入口的公共实现。
//
// 重复注册同一 suffix 会 panic：两个 lane 静默覆盖彼此比启动失败更危险。
func registerDatasetSuffix(registry map[string]datasetActionFunc, kind, suffix string, fn datasetActionFunc) {
	suffix = normalizeDatasetSuffix(suffix)
	if suffix == "" || fn == nil {
		return
	}
	routeMu.Lock()
	defer routeMu.Unlock()
	if _, exists := registry[suffix]; exists {
		panic("duplicate dataset " + kind + " suffix: " + suffix)
	}
	registry[suffix] = fn
}

// normalizeDatasetSuffix 去掉 suffix 两端的斜杠，使注册与匹配使用同一拼写
// （"questions/difficulty-stats" 与 "/questions/difficulty-stats/" 等价）。
func normalizeDatasetSuffix(suffix string) string {
	return strings.Trim(suffix, "/")
}

// lookupDatasetSuffix 在按 suffix 注册的表里取**最长匹配**。
//
// 取最长而非任意命中：允许同时注册 "export" 与 "export/formats"，
// 且更具体的那个获胜。
func lookupDatasetSuffix(registry map[string]datasetActionFunc, suffix string) (datasetActionFunc, bool) {
	routeMu.RLock()
	defer routeMu.RUnlock()
	for {
		if fn, ok := registry[suffix]; ok {
			return fn, true
		}
		index := strings.LastIndex(suffix, "/")
		if index < 0 {
			return nil, false
		}
		suffix = suffix[:index]
	}
}

// applyRouteRegistrars 在 main() 中调用，应用全部已注册的顶层路由。
func applyRouteRegistrars(mux *http.ServeMux, app *application) {
	routeMu.RLock()
	registrars := make([]routeRegistrar, len(routeRegistrars))
	copy(registrars, routeRegistrars)
	routeMu.RUnlock()
	for _, registrar := range registrars {
		registrar(mux, app)
	}
}

// lookupDatasetRouter 按路径段查找子路由。
func lookupDatasetRouter(segment string) (datasetRouter, bool) {
	routeMu.RLock()
	defer routeMu.RUnlock()
	router, ok := datasetRouters[segment]
	return router, ok
}

// splitDatasetSubPath 解析 /api/v1/datasets/{id}/{segment}[/rest...]。
// 解析失败时 ok=false，调用方应回退到 legacy 分支。
func splitDatasetSubPath(path string) (id int64, segment string, rest string, ok bool) {
	const prefix = "/api/v1/datasets/"
	if !strings.HasPrefix(path, prefix) {
		return 0, "", "", false
	}
	remainder := strings.TrimPrefix(path, prefix)
	remainder = strings.Trim(remainder, "/")
	if remainder == "" {
		return 0, "", "", false
	}
	parts := strings.SplitN(remainder, "/", 2)
	parsed, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || parsed <= 0 {
		return 0, "", "", false
	}
	if len(parts) == 1 {
		return parsed, "", "", true
	}
	segment = parts[1]
	if index := strings.Index(segment, "/"); index >= 0 {
		rest = segment[index:]
		segment = segment[:index]
	}
	return parsed, segment, rest, true
}

// tryDatasetRouter 尝试用注册表处理数据集子路径，命中返回 true。
//
// 查表顺序（先到先用）：
//  1. datasetRouters —— 按单段注册（RegisterDatasetRouter），能拿到 rest；
//  2. 按 suffix 注册的子动作（RegisterDatasetGet / RegisterDatasetAction）。
//
// 单段优先是因为它更具体（segment 精确相等）：注册了段 "questions" 的 lane
// 已经自己处理 /difficulty-stats 这类 rest，不该被 suffix 表抢走。
func tryDatasetRouter(w http.ResponseWriter, r *http.Request) bool {
	id, segment, rest, ok := splitDatasetSubPath(r.URL.Path)
	if !ok || segment == "" {
		return false
	}
	if router, found := lookupDatasetRouter(segment); found {
		router(w, r, id, rest)
		return true
	}

	suffix := normalizeDatasetSuffix(segment + rest)
	registry := datasetGets
	if r.Method == http.MethodPost {
		registry = datasetActions
	}
	if fn, found := lookupDatasetSuffix(registry, suffix); found {
		fn(w, r, id)
		return true
	}
	return false
}

// datasetSubResourceSuffix 返回 /api/v1/datasets/{id} 之后的剩余路径。
//
// 精确指向数据集资源本身（/api/v1/datasets/12 或 /api/v1/datasets/12/）时返回 ""；
// 指向任何子资源时返回去掉前导斜杠的剩余部分（例如 "domains"、"questions/x"）；
// 路径结构不合法时 ok=false。
//
// 这是 routeDatasetGet 的 default 分支唯一的判据：只有 suffix == "" 才允许返回
// 数据集图。否则任何未注册的子路径都会静默拿到 200 + 数据集图（issue #8）。
func datasetSubResourceSuffix(path string) (suffix string, ok bool) {
	_, segment, rest, ok := splitDatasetSubPath(path)
	if !ok {
		return "", false
	}
	return normalizeDatasetSuffix(segment + rest), true
}

// writeDatasetSubResourceNotFound 写 404，承接 writeError 的既有约定
// （4xx 原样透出中文业务提示，5xx 不泄漏内部详情）。
func (app *application) writeDatasetSubResourceNotFound(w http.ResponseWriter) {
	app.writeJSON(w, http.StatusNotFound, map[string]string{"error": datasetSubResourceNotFoundMessage})
}

// datasetSubResourceNotFoundMessage 同时被实现与契约测试引用，避免文案两处漂移。
const datasetSubResourceNotFoundMessage = "未找到该子资源"

// datasetIDFromPath 由 apps/api/datasets.go 提供（legacy 分支使用），此处不再重复定义。
