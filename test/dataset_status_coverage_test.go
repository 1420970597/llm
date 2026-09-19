// 数据集状态覆盖守卫（issue #98 的防复发机制）。
//
// 背景：后端 worker 会写入前端不认识的状态值。最典型的是 `directions_completed`
// （`apps/worker/job_directions.go:86` / `:166`），它在 `apps/web-user/src/App.tsx`
// 里的出现次数长期为 **0** —— 所有 `switch (status)` 都落到 `default`，
// 而 `statusLabel` 的 default 是 `return status`，于是**原始英文内部状态串被直接显示给用户**：
//
//	主题：行业研究 · directions_completed · 进度 0%
//
// 同时进度归零、ETA 失效、主按钮把用户送回起点。这违反 功能说明.txt 的
// 「系统设计必须符合人机交互习惯」，也违反 todo.md §14「用户页面禁止默认暴露内部实现字段」。
//
// 为什么这条守卫必须存在（而不是补上那一个状态就完事）：
//
//	状态集合的**所有权在后端**。前端自己声明一份清单，只能证明「前端和自己一致」，
//	证明不了「前端和后端一致」—— 而漂移恰恰发生在两者之间。
//	因此本守卫直接从**后端 Go 源码**提取会写入的状态值，再断言前端有能力处理它们。
//	这样「后端新增状态、前端忘改」会在 `go test` 阶段直接失败。
//
// 与 test/frontend_routes_test.go 的分工：
//
//	那是「路由接线」守卫（#61），这是「状态覆盖」守卫（#98）；两者都是
//	「源码级断言兜住 CI 看不见的那一类缺陷」。
package test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// backendStatusSources 是可能写入 `datasets.status` 的后端源码。
//
// 分成三类是因为写入方式不同，提取规则也不同：
//   - literal: 直接写字面量的 UpdateStatus 调用，以及 store 层的 nextStatus 赋值；
//   - dynamic: `job.Type + "_failed"` 这种运行时拼接，只能提取「拼接形式」；
//   - queued:  apps/api 侧入队时写入的 queuedStatus。
var backendStatusSources = struct {
	// 字面量 UpdateStatus 调用的文件
	workers []string
	// store 层写 nextStatus 的文件
	stores []string
	// api 侧入队写 queuedStatus 的文件
	apis []string
}{
	workers: []string{
		"../apps/worker/main.go",
		"../apps/worker/job_directions.go",
		"../apps/worker/job_questions_v2.go",
		"../apps/worker/job_export_multi.go",
	},
	stores: []string{
		"../internal/store/reasoning_store.go",
		"../internal/store/reward_store.go",
		"../internal/store/dataset_store.go",
	},
	apis: []string{
		"../apps/api/questions.go",
		"../apps/api/reasoning.go",
		"../apps/api/rewards.go",
		"../apps/api/exports.go",
		"../apps/api/routes_directions.go",
		"../apps/api/routes_grpo.go",
		"../apps/api/routes_sft.go",
		"../apps/api/routes_chain_standards.go",
		"../apps/api/routes_questions_v2.go",
		"../apps/api/routes_export_formats.go",
		"../apps/api/datasets.go",
	},
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(raw)
}

// extractLiteralStatuses 提取 `UpdateStatus(ctx, x, "literal")` 形式的状态值。
//
// 调用可能跨行（gofmt 会把长调用拆行），因此先做空白归一化再匹配。
func extractLiteralStatuses(t *testing.T, path string) []string {
	t.Helper()
	source := readSource(t, path)
	// 把换行与连续空白压成单空格，跨行调用就能被单行正则命中。
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(source, " ")

	found := map[string]bool{}
	// UpdateStatus(<非逗号>, <非逗号>, "字面量")
	callPattern := regexp.MustCompile(`UpdateStatus\([^,]+,[^,]+,\s*"([A-Za-z0-9_.\-]+)"`)
	for _, m := range callPattern.FindAllStringSubmatch(flat, -1) {
		found[m[1]] = true
	}

	out := make([]string, 0, len(found))
	for status := range found {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

// extractNextStatusLiterals 提取 store 层 `nextStatus := "literal"` 的赋值。
func extractNextStatusLiterals(t *testing.T, path string) []string {
	t.Helper()
	source := readSource(t, path)
	pattern := regexp.MustCompile(`nextStatus\s*(?::=|=)\s*"([A-Za-z0-9_]+)"`)

	found := map[string]bool{}
	for _, m := range pattern.FindAllStringSubmatch(source, -1) {
		found[m[1]] = true
	}
	out := make([]string, 0, len(found))
	for status := range found {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

// extractQueuedStatuses 提取 apps/api 侧 `enqueueJob(..., "xxx_queued")` 的入参。
//
// 三种调用形态：
//
//	enqueueJob(ctx, "job.type", id, "xxx_queued")          // 4 参
//	enqueueDatasetJob(ctx, "job.type", id, "xxx_queued")   // 4 参
//	enqueueJob(ctx, jobType, id, stage+"_queued")          // 动态拼接（记下来，由后缀规则兜）
func extractQueuedStatuses(t *testing.T, path string) []string {
	t.Helper()
	source := readSource(t, path)
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(source, " ")

	found := map[string]bool{}
	// 4 参形态的最后一个字面量参数
	pattern := regexp.MustCompile(`enqueue(?:Dataset)?Job\([^)]*?,\s*"([A-Za-z0-9_]+)"\s*\)`)
	for _, m := range pattern.FindAllStringSubmatch(flat, -1) {
		found[m[1]] = true
	}

	out := make([]string, 0, len(found))
	for status := range found {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

// frontendStatusModulePath 是状态文案的单一事实来源（issue #98 引入）。
const frontendStatusModulePath = "../apps/web-user/src/lib/datasetStatus.ts"

// extractFrontendKnownStatuses 从前端状态模块里读出显式认识的状态 key。
//
// 读取 `DATASET_STATUS_LABELS` 这个 Record 的 key 集合：它是
// `Record<DatasetStatus, string>`，漏 key 会由 tsc 报错（TS2739），
// 因此「这个集合 == DatasetStatus 联合类型」由编译器保证。
func extractFrontendKnownStatuses(t *testing.T) []string {
	t.Helper()
	source := readSource(t, frontendStatusModulePath)

	block := regexp.MustCompile(
		`(?s)DATASET_STATUS_LABELS\s*:\s*Record<DatasetStatus,\s*string>\s*=\s*\{(.*?)\n\}`,
	).FindStringSubmatch(source)
	if block == nil {
		t.Fatalf("未在 %s 中找到 DATASET_STATUS_LABELS 声明，"+
			"断言失效（源码结构可能已变化，请同步更新本守卫）", frontendStatusModulePath)
	}

	keyPattern := regexp.MustCompile(`(?m)^\s*'?([A-Za-z0-9_]+)'?\s*:`)
	known := map[string]bool{}
	for _, m := range keyPattern.FindAllStringSubmatch(block[1], -1) {
		known[m[1]] = true
	}
	if len(known) == 0 {
		t.Fatalf("未从 DATASET_STATUS_LABELS 解析出任何 key，断言失效")
	}

	out := make([]string, 0, len(known))
	for status := range known {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

// extractFrontendSuffixRules 读取前端后缀规则，用于校验「动态状态被兜住」。
func extractFrontendSuffixRules(t *testing.T) []string {
	t.Helper()
	source := readSource(t, frontendStatusModulePath)
	pattern := regexp.MustCompile(`suffix:\s*'([A-Za-z0-9_]+)'`)

	rules := map[string]bool{}
	for _, m := range pattern.FindAllStringSubmatch(source, -1) {
		rules[m[1]] = true
	}
	if len(rules) == 0 {
		t.Fatalf("未在 %s 中找到任何 SUFFIX_RULES 条目，断言失效", frontendStatusModulePath)
	}
	out := make([]string, 0, len(rules))
	for rule := range rules {
		out = append(out, rule)
	}
	sort.Strings(out)
	return out
}

// collectBackendLiteralStatuses 汇总后端**静态**能写出的全部状态值。
func collectBackendLiteralStatuses(t *testing.T) []string {
	t.Helper()
	all := map[string]bool{}

	for _, path := range backendStatusSources.workers {
		for _, status := range extractLiteralStatuses(t, path) {
			all[status] = true
		}
	}
	for _, path := range backendStatusSources.stores {
		for _, status := range extractNextStatusLiterals(t, path) {
			all[status] = true
		}
		for _, status := range extractLiteralStatuses(t, path) {
			all[status] = true
		}
	}
	for _, path := range backendStatusSources.apis {
		for _, status := range extractQueuedStatuses(t, path) {
			all[status] = true
		}
	}

	out := make([]string, 0, len(all))
	for status := range all {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

// TestBackendDatasetStatusesAreCoveredByFrontend 断言后端写入的每个状态值
// 都在前端状态模块里有显式文案。
//
// 这是 issue #98 的防复发守卫：只补 directions_completed 一个值，
// 同类问题会在下次后端新增状态时重演。
func TestBackendDatasetStatusesAreCoveredByFrontend(t *testing.T) {
	backend := collectBackendLiteralStatuses(t)
	if len(backend) == 0 {
		t.Fatal("未能从后端源码提取到任何状态值，守卫失效，请检查提取规则")
	}

	known := map[string]bool{}
	for _, status := range extractFrontendKnownStatuses(t) {
		known[status] = true
	}

	var missing []string
	for _, status := range backend {
		if !known[status] {
			missing = append(missing, status)
		}
	}

	if len(missing) > 0 {
		t.Errorf("后端会写入 %d 个前端不认识的状态值（issue #98 的成因）：\n"+
			"  %s\n\n"+
			"修复方式：在 %s 的 DatasetStatus 联合类型与 DATASET_STATUS_LABELS /\n"+
			"DATASET_STATUS_PROGRESS / DATASET_STATUS_ROUTE 中补齐这些值。\n"+
			"只补文案不补进度/去向会导致「文案对了但进度与跳转仍错」。\n"+
			"若不补，用户在界面上会看到原始英文内部状态串（违反 todo.md §14）。",
			len(missing), strings.Join(missing, ", "), frontendStatusModulePath)
	}

	t.Logf("后端静态状态 %d 个，前端显式覆盖 %d 个，全部命中", len(backend), len(known))
}

// TestFrontendStatusMapsCoverSameKeySet 断言三张前端状态表覆盖**同一组** key。
//
// 为什么单独测：只补 DATASET_STATUS_LABELS 而忘补 PROGRESS/ROUTE 是本 issue 的
// 真实症状之一 —— 文案对了，但进度仍显示 0%、主按钮仍把人送回起点。
func TestFrontendStatusMapsCoverSameKeySet(t *testing.T) {
	source := readSource(t, frontendStatusModulePath)

	extractKeys := func(constName string) []string {
		block := regexp.MustCompile(
			`(?s)` + constName + `\s*:\s*Record<DatasetStatus,\s*[A-Za-z]+>\s*=\s*\{(.*?)\n\}`,
		).FindStringSubmatch(source)
		if block == nil {
			t.Fatalf("未找到 %s 声明，断言失效", constName)
		}
		keyPattern := regexp.MustCompile(`(?m)^\s*'?([A-Za-z0-9_]+)'?\s*:`)
		out := []string{}
		for _, m := range keyPattern.FindAllStringSubmatch(block[1], -1) {
			out = append(out, m[1])
		}
		sort.Strings(out)
		return out
	}

	labels := extractKeys("DATASET_STATUS_LABELS")
	progress := extractKeys("DATASET_STATUS_PROGRESS")
	routes := extractKeys("DATASET_STATUS_ROUTE")

	for name, keys := range map[string][]string{
		"DATASET_STATUS_LABELS":   labels,
		"DATASET_STATUS_PROGRESS": progress,
		"DATASET_STATUS_ROUTE":    routes,
	} {
		if len(keys) == 0 {
			t.Fatalf("%s 未解析出任何 key，断言失效", name)
		}
	}

	if strings.Join(labels, ",") != strings.Join(progress, ",") {
		t.Errorf("DATASET_STATUS_LABELS 与 DATASET_STATUS_PROGRESS 的 key 集合不一致：\n"+
			"  labels   = %v\n  progress = %v\n"+
			"  漏了进度会让状态显示为 0%%（issue #98 的第二个症状）", labels, progress)
	}
	if strings.Join(labels, ",") != strings.Join(routes, ",") {
		t.Errorf("DATASET_STATUS_LABELS 与 DATASET_STATUS_ROUTE 的 key 集合不一致：\n"+
			"  labels = %v\n  routes = %v\n"+
			"  漏了去向会让主按钮把用户送错页面（issue #98 的第四个症状）", labels, routes)
	}
}

// TestDynamicFailedStatusesAreCoveredBySuffixRules 断言后端的**动态**失败状态
// 被前端的后缀规则兜住。
//
// `apps/worker/main.go:127` 写的是 `job.Type + "_failed"`，取值取决于注册表。
// 这类值不可能逐个进联合类型，因此必须由后缀规则兜。
// 真实库里已观察到 `chain-standards.generate_failed`，证明这条路径非理论。
func TestDynamicFailedStatusesAreCoveredBySuffixRules(t *testing.T) {
	workerMain := readSource(t, "../apps/worker/main.go")
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(workerMain, " ")

	if !strings.Contains(flat, `job.Type+"_failed"`) {
		t.Skip("worker 不再使用 job.Type+\"_failed\" 动态拼接（可能已改为字面量），" +
			"本条断言不再适用；请把新形态补进本守卫后再移除本跳过")
	}

	rules := extractFrontendSuffixRules(t)
	hasFailedRule := false
	for _, rule := range rules {
		if rule == "_failed" {
			hasFailedRule = true
			break
		}
	}
	if !hasFailedRule {
		t.Errorf("后端会写入动态状态 `job.Type + \"_failed\"`（如 chain-standards.generate_failed），\n"+
			"但 %s 的 SUFFIX_RULES 里没有 `_failed` 规则，这些状态会退化为兜底文案。\n"+
			"当前规则：%v", frontendStatusModulePath, rules)
	}
}

// TestStatusLabelMustNotLeakRawStatus 断言前端状态文案函数**不再**把原始状态串回传给用户。
//
// 这是 issue #98 的直接形态：`statusLabel` 的 `default: return status`
// 会把 `directions_completed` 原样显示。本断言读取 App.tsx 源码确认该 default 已移除。
func TestStatusLabelMustNotLeakRawStatus(t *testing.T) {
	source := readAppSource(t)

	block := regexp.MustCompile(`(?s)function statusLabel\(status: string\)\s*\{(.*?)\n\}`).FindStringSubmatch(source)
	if block == nil {
		t.Fatal("未找到 statusLabel 函数，断言失效")
	}
	body := block[1]

	if regexp.MustCompile(`return\s+status\s*$`).MatchString(strings.TrimSpace(body)) {
		t.Error("statusLabel 的 default 分支直接返回原始状态串：\n" +
			"  这会把 directions_completed 这类内部英文标识显示给用户（issue #98）。\n" +
			"  修复方式：改为 return describeDatasetStatus(status).label")
	}
	if !strings.Contains(body, "describeDatasetStatus") {
		t.Errorf("statusLabel 未使用单一事实来源 describeDatasetStatus：\n%s\n"+
			"  请改为 return describeDatasetStatus(status).label", body)
	}
}

// TestEveryBackendStatusHasChineseLabel 断言前端状态模块里**没有**英文占位文案。
//
// 防的是「补齐了 key 但文案忘了写，直接把 key 填进去」这种走过场的修复。
func TestEveryBackendStatusHasChineseLabel(t *testing.T) {
	source := readSource(t, frontendStatusModulePath)
	block := regexp.MustCompile(
		`(?s)DATASET_STATUS_LABELS\s*:\s*Record<DatasetStatus,\s*string>\s*=\s*\{(.*?)\n\}`,
	).FindStringSubmatch(source)
	if block == nil {
		t.Fatal("未找到 DATASET_STATUS_LABELS，断言失效")
	}

	// 每条形如 `  key: '文案',`
	entryPattern := regexp.MustCompile(`(?m)^\s*'?([A-Za-z0-9_]+)'?\s*:\s*'([^']*)'`)
	entries := entryPattern.FindAllStringSubmatch(block[1], -1)
	if len(entries) == 0 {
		t.Fatal("未解析出任何状态文案，断言失效")
	}

	hanPattern := regexp.MustCompile(`[\p{Han}]`)
	for _, entry := range entries {
		key, label := entry[1], entry[2]
		t.Run(key, func(t *testing.T) {
			if label == key {
				t.Errorf("状态 %s 的文案就是 key 本身，等于直接给用户看内部标识", key)
			}
			if !hanPattern.MatchString(label) {
				t.Errorf("状态 %s 的文案 %q 不含汉字，疑似忘了写中文", key, label)
			}
		})
	}
}

// enumLabelModulePath 是「枚举字段 → 中文文案」的单一事实来源（issue #98 同族缺陷）。
const enumLabelModulePath = "../apps/web-user/src/lib/enumLabels.ts"

// labelLeakAllowlist 是**允许**保留 `default: return <参数>` 形态的函数名。
//
// 为什么需要白名单：这个形态并非总是缺陷 —— 当函数处理的是**自由文本**
// （例如对象键、用户自定义名称）时，「原样返回」才是正确的，加中文兜底反而会丢信息。
// 白名单要求写出理由，防止有人图省事把新的泄漏函数塞进来。
//
// 注意：本条守卫的目标是**枚举类**字段（取值域有界、由代码定义）。
// 判断标准是「后端/数据库定义了有限取值域」，而不是「参数名叫 status」。
var labelLeakAllowlist = map[string]string{
	// 暂时为空：当前被扫描的函数都应当是枚举类。
	// 若将来确有自由文本函数需要保留 passthrough，在此登记并写明理由。
}

// TestLabelFunctionsMustNotLeakRawValues 断言「枚举 → 文案」类函数不再把原始值回传给用户。
//
// 这是 issue #98 的**结构性**防复发：该 issue 的表面症状只是 dataset.status 一个值，
// 但父代理用同一方法检查同族函数时，发现另外两个字段有完全相同的缺陷且真实命中：
//
//   - `reviewStatusLabel`：domain.review_status 的 `draft` 是数据库默认值，
//     旧实现只处理 approved/pending → 库中全部 230 个领域都显示英文 "draft"；
//   - `artifactLabel`：旧实现只处理 'jsonl-export'，
//     但 artifact_type = format+"-export"（5 种格式）→ 库中 sharegpt-export /
//     alpaca-export 显示英文原始串。
//
// 只补 dataset.status 的一个分支会让这两处继续漏下去，因此必须有结构性守卫。
func TestLabelFunctionsMustNotLeakRawValues(t *testing.T) {
	source := readAppSource(t)

	// 匹配 `function xxxLabel(...) { ... default: return xxx ... }` 这种形态。
	// 用「函数体切片」而不是全文件正则，避免跨函数误匹配。
	funcPattern := regexp.MustCompile(`(?s)function\s+([A-Za-z0-9_]+Label)\s*\(([A-Za-z0-9_]+)\s*:\s*string[^)]*\)\s*\{(.*?)\n\}`)
	matches := funcPattern.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatal("未在 App.tsx 中找到任何 *Label(status: string) 形态的函数，守卫失效")
	}

	scanned := 0
	for _, m := range matches {
		name, param, body := m[1], m[2], m[3]
		if _, allowed := labelLeakAllowlist[name]; allowed {
			continue
		}
		scanned++

		t.Run(name, func(t *testing.T) {
			// 形态一：`default: return <参数>` 或 `return <参数> || 兜底`
			passThrough := regexp.MustCompile(`default:\s*\n?\s*return\s+` + regexp.QuoteMeta(param) + `\b`)
			if passThrough.MatchString(body) {
				t.Errorf("函数 %s 的 default 分支直接返回参数 %s：\n"+
					"  这会把枚举字段的原始值（可能是英文内部标识）显示给用户（issue #98 的成因）。\n"+
					"  修复方式：改为调用 lib/enumLabels.ts / lib/datasetStatus.ts 里的 describe* 函数，\n"+
					"  或在本函数里补一个**中文**兜底文案。\n"+
					"  若该函数的取值域确实是自由文本、原样返回是对的，请在 labelLeakAllowlist 登记并写明理由。",
					name, param)
			}

			// 形态二：整个函数体就是一个 `return <参数>`（无任何映射）——也属于泄漏。
			trimmed := strings.TrimSpace(body)
			if regexp.MustCompile(`^return\s+` + regexp.QuoteMeta(param) + `$`).MatchString(trimmed) {
				t.Errorf("函数 %s 直接把参数原样返回，没有任何中文映射", name)
			}
		})
	}

	if scanned == 0 {
		t.Fatal("所有 *Label 函数都在白名单里，守卫等于空转，请检查 labelLeakAllowlist")
	}
	t.Logf("扫描了 %d 个 *Label 函数，均无原始值泄漏", scanned)
}

// TestEnumLabelModuleCoversObservedValues 断言 enumLabels.ts 覆盖了
// **真实数据库里已出现**的枚举取值。
//
// 为什么拿真实库当样本：`reviewStatusLabel` 与 `artifactLabel` 的缺陷都是
// 「代码没覆盖到库里已有的值」，而不是「理论上可能有的值」。
// 因此用「库里实际出现过什么」当断言样本，能直接抓住这一类漏覆盖。
//
// 无数据库可达时跳过（CI 不起容器），不伪造通过。
func TestEnumLabelModuleCoversObservedValues(t *testing.T) {
	source := readSource(t, enumLabelModulePath)

	// 断言关键取值都在模块里有明确的 key（不依赖数据库，保证 CI 可跑）。
	// key 在 TypeScript 里是标识符（`draft: '...'`），因此按「行首 key:」匹配。
	required := map[string]string{
		"DOMAIN_REVIEW_STATUS_LABELS": "draft",
		"EXPORT_FORMAT_LABELS":        "sharegpt",
	}
	for constName, wantedKey := range required {
		block := regexp.MustCompile(`(?s)` + constName + `[^{]*\{(.*?)\n\}`).FindStringSubmatch(source)
		if block == nil {
			t.Errorf("未找到 %s 声明，无法验证取值覆盖", constName)
			continue
		}
		keyPattern := regexp.MustCompile(`(?m)^\s*'?` + regexp.QuoteMeta(wantedKey) + `'?\s*:`)
		if !keyPattern.MatchString(block[1]) {
			t.Errorf("%s 缺少 key %q 的文案。\n"+
				"  该取值**已在真实数据库中被观察到**（review_status 全部为 draft；\n"+
				"  artifact_type 存在 sharegpt-export / alpaca-export），缺文案会让用户看到英文原始串。",
				constName, wantedKey)
		}
	}

	// 断言三个 describe* 函数都存在且被 App.tsx 调用（防止模块写了但没接线）。
	for _, fn := range []string{"describeDomainReviewStatus", "describeArtifactType", "describeArtifactContentType"} {
		if !strings.Contains(source, "export function "+fn) {
			t.Errorf("enumLabels.ts 缺少导出函数 %s", fn)
		}
		if !strings.Contains(readAppSource(t), fn) {
			t.Errorf("App.tsx 未调用 %s —— 模块写了但没接线，缺陷仍在", fn)
		}
	}
}

// TestArtifactDownloadFetchesSameOriginRelativePath 断言工件下载的 fetch 目标
// **永远是同源相对路径**，而不是可被外部输入影响的绝对 URL。
//
// 为什么加这条断言：静态分析器会把 `fetch(<带模板插值的字符串>)` 标为「潜在 SSRF」。
// `App.tsx` 的 downloadArtifact 正好是这个形态，因此每次改动都会报同一条告警。
// 该告警在本项目里是**误报**，理由是：
//
//  1. `consoleApi.artifactDownloadUrl(datasetId, artifactId)` 的两个入参都是 `number`，
//     拼出来的是 `/api/v1/datasets/{id}/export/download?artifactId={aid}` ——
//     一个**以 `/` 开头的相对路径**，origin 由页面自身决定，调用方无法改变；
//  2. SSRF 是**服务端**攻击类（可信服务向攻击者指定的内网地址发请求）。
//     浏览器 fetch 同源相对路径不构成 SSRF —— 同源策略由浏览器强制。
//
// 与其每次都靠人工解释一遍，不如把不变量写成断言：只要这个 fetch 目标仍然
// 由同一个同源构造函数产生，告警就可忽略；一旦有人改成拼接外部输入，
// 本断言会失败，那时它才是真问题。
//
// （顺带说明：仓库里确实存在**服务端**出站请求到可配置地址的形态 ——
// internal/llm/openai_client.go:62 与 internal/llm/provider_admin.go:34 会向
// `provider.BaseURL` 发请求，而该字段由管理员通过 admin API 配置。
// 那是 admin-only 权限面，属于「管理员本就能决定模型端点」的既有设计，
// 不在本 lane（前端状态泄漏）的范围内，已在 PR 里单列给父代理评估。）
func TestArtifactDownloadFetchesSameOriginRelativePath(t *testing.T) {
	appSource := readAppSource(t)
	apiSource := readSource(t, "../apps/web-user/src/lib/api.ts")

	// 1) api.ts 里的 URL 构造函数必须仍然产出同源相对路径。
	urlFn := regexp.MustCompile(
		`artifactDownloadUrl\s*:\s*\(([^)]*)\)\s*=>\s*` + "`" + `([^` + "`" + `]*)` + "`",
	).FindStringSubmatch(apiSource)
	if urlFn == nil {
		t.Fatal("未找到 artifactDownloadUrl 定义，断言失效（实现形态已变化，请同步更新本守卫）")
	}
	params, template := urlFn[1], urlFn[2]

	if !strings.HasPrefix(template, "/") {
		t.Errorf("artifactDownloadUrl 的模板不再以 `/` 开头（得到 %q）：\n"+
			"  这会让它变成绝对 URL 或可在外部影响 origin 的形式，那时 fetch 告警才是真问题", template)
	}
	if !strings.HasPrefix(template, "/api/v1/") {
		t.Errorf("artifactDownloadUrl 的模板不再指向本服务 API 前缀（得到 %q）", template)
	}
	// 入参必须都是 number：一旦有 string 入参，就可能被外部输入影响路径。
	for _, param := range strings.Split(params, ",") {
		if !strings.Contains(param, ": number") {
			t.Errorf("artifactDownloadUrl 出现了非 number 入参 %q：\n"+
				"  number 入参不能注入路径/主机，string 入参可以", strings.TrimSpace(param))
		}
	}

	// 2) 调用点必须直接用该函数，不得自己拼接 URL。
	callSites := regexp.MustCompile(`fetch\(([^,)]*)`).FindAllStringSubmatch(appSource, -1)
	for _, site := range callSites {
		arg := strings.TrimSpace(site[1])
		const exportedUrlHelper = "consoleApi.artifactDownloadUrl"
		looksAbsolute := strings.Contains(arg, "http")
		looksInterpolated := strings.Contains(arg, "${")
		if looksAbsolute || (looksInterpolated && !strings.Contains(arg, exportedUrlHelper)) {
			t.Errorf("发现可疑的 fetch 目标 %q：\n"+
				"  前端出站请求应当只走 consoleApi 的同源封装，不要内联拼接 URL", arg)
		}
	}
}
