package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 冻结契约与实现的一致性守卫。
//
// 背景（issue #8）：docs/plans/eval-and-cleaning-plan.md 第 1.1 节把数据集子路由的
// 注册接口冻结成 RegisterDatasetAction / RegisterDatasetGet / datasetActionFunc，
// 但 apps/api/routes.go 当时只实现了 RegisterDatasetRouter。契约符号全仓库不存在，
// 而文档同时声明自己「不得自行改动」—— 于是 L3 的 questions/difficulty-stats 与
// L6 的 export/formats 这类多段路径在**现有接口下没有任何合规写法**。
//
// 后来实现补齐了这三个符号，文档也同步说明了两者的优先级关系。这条测试的作用是
// 防止二者再次漂移：文档里出现的每个 Go 契约符号，必须在实现里真实存在。

const (
	contractDocPath  = "../docs/plans/eval-and-cleaning-plan.md"
	contractGoDirAPI = "../apps/api"
)

// contractSymbols 是契约第 1.1 节冻结的、必须真实存在的 Go 符号。
var contractSymbols = []string{
	"routeRegistrar",
	"RegisterRoutes",
	"RegisterDatasetAction",
	"RegisterDatasetGet",
	"datasetActionFunc",
	"RegisterDatasetRouter",
	"datasetRouter",
}

// goBlockPattern 抓取文档里的 ```go 代码块。
var goBlockPattern = regexp.MustCompile("(?s)```go\n(.*?)```")

// funcDeclPattern 抓取代码块里的顶层函数声明名。
var funcDeclPattern = regexp.MustCompile(`(?m)^func\s+(\w+)`)

// typeDeclPattern 抓取代码块里的顶层类型声明名。
var typeDeclPattern = regexp.MustCompile(`(?m)^type\s+(\w+)`)

// readContractGoBlocks 返回文档中所有 go 代码块的正文。
func readContractGoBlocks(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(contractDocPath))
	if err != nil {
		t.Fatalf("读取契约 %s 失败: %v", contractDocPath, err)
	}
	blocks := goBlockPattern.FindAllStringSubmatch(string(raw), -1)
	if len(blocks) == 0 {
		t.Fatalf("契约 %s 中未找到任何 ```go 代码块；契约结构已变，本守卫失去判据", contractDocPath)
	}
	contents := make([]string, 0, len(blocks))
	for _, block := range blocks {
		contents = append(contents, block[1])
	}
	return contents
}

// readAPISources 返回 apps/api 下全部非测试 Go 源码的拼接内容。
func readAPISources(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(contractGoDirAPI)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", contractGoDirAPI, err)
	}
	var builder strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(contractGoDirAPI, entry.Name()))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", entry.Name(), err)
		}
		builder.Write(raw)
		builder.WriteString("\n")
	}
	return builder.String()
}

// TestContractGoSymbolsExistInImplementation 断言契约里声明的每个 Go 符号都能在实现里找到。
//
// 断言刻意保持「符号存在」而非「签名逐字相等」：后者会因注释、参数命名、
// 换行差异频繁误报，而 issue #8 的实际缺陷是**符号根本不存在**。
func TestContractGoSymbolsExistInImplementation(t *testing.T) {
	blocks := strings.Join(readContractGoBlocks(t), "\n")
	sources := readAPISources(t)

	documented := map[string]bool{}
	for _, match := range funcDeclPattern.FindAllStringSubmatch(blocks, -1) {
		documented[match[1]] = true
	}
	for _, match := range typeDeclPattern.FindAllStringSubmatch(blocks, -1) {
		documented[match[1]] = true
	}

	for _, symbol := range contractSymbols {
		t.Run(symbol, func(t *testing.T) {
			if !documented[symbol] {
				t.Fatalf("冻结符号 %s 已从契约 %s 的 go 代码块中消失；"+
					"若确需移除，必须先更新契约并说明替代方案（issue #8 的教训是：不能只改一边）",
					symbol, contractDocPath)
			}
			if !strings.Contains(sources, symbol) {
				t.Errorf("契约声明了 %s，但 apps/api 的实现里找不到该符号。"+
					"契约与实现不得单边漂移（issue #8）", symbol)
			}
		})
	}
}

// TestContractDeclaresDatasetSuffixRegistries 断言契约仍然记录了按 suffix 注册的两条入口，
// 且实现里确实把它们接进了查表链路（而不只是声明了个没人调用的函数）。
func TestContractDeclaresDatasetSuffixRegistries(t *testing.T) {
	sources := readAPISources(t)

	for _, fn := range []string{"RegisterDatasetAction", "RegisterDatasetGet"} {
		if !strings.Contains(sources, "func "+fn+"(") {
			t.Errorf("实现缺少 %s 的函数定义", fn)
		}
	}

	// 注册进去的 suffix 必须真的被查询，否则注册了也不生效（静默无效果）。
	for _, call := range []string{"datasetGets", "datasetActions", "lookupDatasetSuffix"} {
		if !strings.Contains(sources, call) {
			t.Errorf("查表链路缺少 %s；suffix 注册了也不会被命中", call)
		}
	}
}

// TestRouteDatasetDefaultRejectsUnknownSubPaths 断言 default 分支不再静默返回数据集图。
//
// issue #8 的核心危害：未知子路径返回 200 + 数据集图，导致「测试绿、页面白、错误无声」。
// 这里做源码级断言，因为需要真实 Postgres 才能跑活体 HTTP 断言（见 test/l15_route_contract.py）。
func TestRouteDatasetDefaultRejectsUnknownSubPaths(t *testing.T) {
	mainSource, err := os.ReadFile(filepath.Clean("../apps/api/main.go"))
	if err != nil {
		t.Fatalf("读取 apps/api/main.go 失败: %v", err)
	}
	text := string(mainSource)

	for _, handler := range []string{"routeDatasetGet", "routeDatasetActions"} {
		index := strings.Index(text, "func (app *application) "+handler)
		if index < 0 {
			t.Fatalf("未找到 %s；本守卫的定位锚点已失效", handler)
		}
		// 取该函数体到下一个顶层函数声明之间的文本。
		rest := text[index:]
		if next := strings.Index(rest[1:], "\nfunc "); next >= 0 {
			rest = rest[:next+1]
		}
		if !strings.Contains(rest, "writeDatasetSubResourceNotFound") {
			t.Errorf("%s 的未知子路径分支没有返回 404（未调用 writeDatasetSubResourceNotFound）；"+
				"漏注册的端点会再次静默得到 200 + 数据集图（issue #8）", handler)
		}
	}

	// 精确路径（/api/v1/datasets/{id}）必须仍然返回数据集图，否则是矫枉过正。
	if !strings.Contains(text, "datasetSubResourceSuffix") {
		t.Error("routeDatasetGet 未用 datasetSubResourceSuffix 区分「数据集本身」与「子资源」；" +
			"无法在保持精确路径 200 的同时对未知子路径返回 404")
	}
}
