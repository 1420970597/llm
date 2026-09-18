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

var (
	routeMu         sync.RWMutex
	routeRegistrars []routeRegistrar
	datasetRouters  = map[string]datasetRouter{}
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
func tryDatasetRouter(w http.ResponseWriter, r *http.Request) bool {
	id, segment, rest, ok := splitDatasetSubPath(r.URL.Path)
	if !ok || segment == "" {
		return false
	}
	router, found := lookupDatasetRouter(segment)
	if !found {
		return false
	}
	router(w, r, id, rest)
	return true
}

// datasetIDFromPath 由 apps/api/datasets.go 提供（legacy 分支使用），此处不再重复定义。
