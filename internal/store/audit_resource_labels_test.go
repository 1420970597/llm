package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是 issue #191「中文界面漏出内部英文键」第四条渲染路径的回归守卫。
//
// 缺陷形态：审计记录有两个独立取值域 ——「动作」与「资源」。
// 旧实现只翻译了动作（`auditActionLabels`），审计表的「资源」列直接渲染
// `audit_logs.resource_type`（`sample_version` / `workspace_member` …），
// 于是中文界面里仍然混着英文内部键。
//
// 本文件断言 `auditResourceLabels` 覆盖全部**后端实际写入**的资源类型，
// 且每个值都是中文（不含 ASCII 下划线式内部 code）。
// 取值域从 Go 源码提取，而不是手抄清单 —— 手抄只能证明「表和自己一致」，
// 证明不了「表和写入方一致」，而漂移恰恰发生在两者之间。

// auditResourceWriteSources 是可能写入 `audit_logs.resource_type` 的后端源码。
//
// 为什么不手抄清单：本仓库已因「手工清单会漏文件」而把 #192 的 Markdown 守卫
// 改成递归扫描。同一道理适用于这里 —— 新增一个 audit 写入点却忘改本文件，
// 守卫会静默空转（而空转的守卫比没有守卫更危险：它给人已受保护的错觉）。
// 因此这里用 `discoverAuditWriteSources` 递归扫描 apps/ 与 internal/。
var auditWriteSourceRoots = []string{"../../apps", ".."}

// auditResourceArgIndex 是各调用形态里「资源类型」参数的下标（0 起）。
//
// 用参数下标而不是正则直接匹配字面量，是因为实参里含 `r.Context()` 与
// `strconv.FormatInt(...)` —— 括号会截断朴素的 `[^)]*` 匹配，
// 导致守卫静默漏掉整类写入点（漏掉就等于守卫空转）。
var auditResourceArgIndex = []struct {
	call  string
	index int
}{
	{"WriteAuditLog(", 3},       // (ctx, actor, action, resourceType, ...)
	{".audit(", 2},              // (ctx, action, resourceType, resourceID, detail)
	{"writeProjectAuditTx(", 4}, // (ctx, tx, actorID, action, resourceType, ...)
}

// readRepoSource 读取仓库内源码（路径相对 internal/store/）。
func readRepoSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(raw)
}

// splitCallArgs 从 `src[open+1:]` 起按括号配平切出顶层逗号分隔的实参。
//
// 为什么必须配平：实参含嵌套括号与字符串字面量里的逗号，
// 朴素 `strings.Split(argText, ",")` 会把一个实参切成多个，下标随之错位。
func splitCallArgs(src string, open int) []string {
	args := []string{}
	depth := 0
	inString := byte(0)
	start := open + 1
	for i := open + 1; i < len(src); i++ {
		c := src[i]
		if inString != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}
		switch c {
		case '"', '`', '\'':
			inString = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if c == ')' && depth == 0 {
				if arg := strings.TrimSpace(src[start:i]); arg != "" {
					args = append(args, arg)
				}
				return args
			}
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(src[start:i]))
				start = i + 1
			}
		}
	}
	return args
}

// stringLiteralArg 在实参是**纯字符串字面量**时返回其内容，否则返回 ""。
func stringLiteralArg(arg string) string {
	m := regexp.MustCompile(`^"([a-z_]+)"$`).FindStringSubmatch(arg)
	if m == nil {
		return ""
	}
	return m[1]
}

// discoverAuditWriteSources 递归找出所有调用审计写入函数的 Go 源文件。
//
// 返回**仓库相对**路径（与 roots 的父目录对齐），便于报错时给出可点击的位置。
func discoverAuditWriteSources(t *testing.T) []string {
	t.Helper()
	pattern := regexp.MustCompile(`WriteAuditLog\(|\.audit\(|writeProjectAuditTx\(|writeStudioAuditTx\(|StudioAudit\{`)
	// 有意不匹配 `AuditEntry{` 与 `model.AuditLog{`：它们是**结构体类型**，
	// 会出现在纯读路径（列表/测试夹具）上，把它们当写入点会让扫描结果失焦。
	found := []string{}
	for _, root := range auditWriteSourceRoots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if pattern.Match(raw) {
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", root, err)
		}
	}
	sort.Strings(found)
	if len(found) < 5 {
		t.Fatalf("只发现 %d 个审计写入点，扫描可能失效（预期至少 5 个）", len(found))
	}
	return found
}

// literalResourceTypes 从 Go 源码提取全部资源类型字面量。
//
// `document_store.go` 写的是 `string(kind) + "_version"` 这种派生形态，
// 因此同时展开 model 层的 `DocumentKind` 常量。
func literalResourceTypes(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}

	entryPattern := regexp.MustCompile(`Resource:\s+"([a-z_]+)"`)
	for _, path := range discoverAuditWriteSources(t) {
		src := readRepoSource(t, path)
		for _, m := range entryPattern.FindAllStringSubmatch(src, -1) {
			found[m[1]] = true
		}
		for _, call := range auditResourceArgIndex {
			for idx := 0; ; {
				next := strings.Index(src[idx:], call.call)
				if next < 0 {
					break
				}
				open := idx + next + len(call.call) - 1
				args := splitCallArgs(src, open)
				if len(args) > call.index {
					if v := stringLiteralArg(args[call.index]); v != "" {
						found[v] = true
					}
				}
				idx = open + 1
			}
		}
	}

	// 文档版本资源：`document_store.go` 用 `string(kind) + "_version"` 派生。
	docs := readRepoSource(t, "document_store.go")
	if !strings.Contains(docs, `string(kind) + "_version"`) {
		t.Fatalf("document_store.go 的版本资源派生形态已变化（未找到 `string(kind) + \"_version\"`）；" +
			"本守卫会漏掉全部 `*_version` 资源类型，请同步更新提取规则")
	}
	for _, kind := range documentKinds(t) {
		found[kind+"_version"] = true
	}

	if len(found) == 0 {
		t.Fatal("未提取到任何 audit_logs.resource_type 字面量，守卫失效（写入点形态可能已变化）")
	}
	return found
}

// documentKinds 从 model 层提取 DocumentKind 常量取值。
func documentKinds(t *testing.T) []string {
	t.Helper()
	src := readRepoSource(t, "../model/studio_docs.go")
	matches := regexp.MustCompile(`DocumentKind\s*=\s*"([a-z_]+)"`).FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatal("未从 studio_docs.go 提取到任何 DocumentKind 常量，守卫失效")
	}
	out := []string{}
	for _, m := range matches {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// TestFrontendResourceLabelsMatchBackendMap 断言前端资源文案表与后端键集一致。
//
// 为什么必须跨语言比对：同一个 `resource_type` 既有 Go 侧映射（动态/搜索文案），
// 又有 TS 侧映射（审计表渲染）。只测各自“不空”证明不了两者一致，
// 而漂移恰恰发生在两者之间（#191 的成因就是只有一侧有映射）。
func TestFrontendResourceLabelsMatchBackendMap(t *testing.T) {
	src := readRepoSource(t, "../../apps/web-user/src/lib/enumLabels.ts")
	block := regexp.MustCompile(`(?s)AUDIT_RESOURCE_LABELS[^{]*\{(.*?)\n\}`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("未在 enumLabels.ts 找到 AUDIT_RESOURCE_LABELS 声明，断言失效")
	}

	frontendKeys := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\s*:`).FindAllStringSubmatch(block[1], -1) {
		frontendKeys[m[1]] = true
	}
	if len(frontendKeys) == 0 {
		t.Fatal("前端 AUDIT_RESOURCE_LABELS 未解析出任何 key，断言失效")
	}

	missing := []string{}
	for resource := range auditResourceLabels {
		if !frontendKeys[resource] {
			missing = append(missing, resource)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("前端 AUDIT_RESOURCE_LABELS 缺少后端已有的资源类型：%v\n"+
			"  这会让同一个资源在「动态」里是中文、在审计表里是英文（issue #191 的原始形态）。", missing)
	}
}

// hasCJK 判断字符串是否含中日韩字符（用于「文案必须是中文」的断言）。
func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// TestAuditResourceLabelsCoverEveryWrittenResourceType 断言资源文案表覆盖全部写入值。
//
// 这是 issue #191 的**结构性**防复发：只补 `sample_version` / `workspace_member`
// 两个键会随下一次新增资源类型再次漏出，因此改为从写入方提取后全量比对。
func TestAuditResourceLabelsCoverEveryWrittenResourceType(t *testing.T) {
	written := literalResourceTypes(t)
	t.Logf("从后端源码提取到 %d 种 resource_type：%v", len(written), sortedKeys(written))

	missing := []string{}
	for resource := range written {
		if _, ok := auditResourceLabels[resource]; !ok {
			missing = append(missing, resource)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("auditResourceLabels 缺少以下资源类型的中文文案：%v\n"+
			"  这些值**确实会被写入 audit_logs.resource_type**（从后端源码提取），\n"+
			"  缺文案时旧控制台「操作记录」的「资源」列会直接把英文内部键显示给用户（issue #191）。\n"+
			"  修复方式：在 internal/store/activity_store.go 的 auditResourceLabels 与\n"+
			"  apps/web-user/src/lib/enumLabels.ts 的 AUDIT_RESOURCE_LABELS 同步补上中文。", missing)
	}
}

// TestAuditResourceLabelsAreChinese 断言资源文案表里没有残留的英文内部 code。
//
// 边界路径：即使键补全了，值本身也可能是直接从 code 复制过来的英文串
// （「补了 key 却没真修」的形态）。这里要求值含中日韩字符且与 code 不同。
func TestAuditResourceLabelsAreChinese(t *testing.T) {
	if len(auditResourceLabels) == 0 {
		t.Fatal("auditResourceLabels 为空，守卫失效")
	}
	for resource, label := range auditResourceLabels {
		if label == "" {
			t.Errorf("资源 %q 的文案为空", resource)
			continue
		}
		if !hasCJK(label) {
			t.Errorf("资源 %q 的文案 %q 不含中文 —— 这等于把内部英文 code 显示给用户（issue #191）", resource, label)
		}
		if label == resource {
			t.Errorf("资源 %q 的文案与 code 相同，映射没有生效", resource)
		}
	}
}

// TestAuditActionFallbackNeverLeaksRawCode 断言未登记动作的派生兜底不返回原始 code。
//
// 正常路径：能识别后缀的动作派生出「资源 + 动词」。
// 边界路径：完全不认识的动作必须落到中性中文，而不是把入参回传。
func TestAuditActionFallbackNeverLeaksRawCode(t *testing.T) {
	cases := []struct {
		action string
		want   string
	}{
		// 正常路径：已知资源前缀 + 已知后缀。
		{"blueprint_version_created", "蓝图保存新版本"},
		{"batch_resumed", "批次恢复"},
		// 正常路径：未知资源前缀 + 已知后缀 → 中性「配置 + 动词」。
		{"mystery_thing_created", "配置创建"},
		// 边界路径：后缀完全不认识 → 中性兜底，绝不回传原始 code。
		{"something_entirely_unknown", "配置变更"},
		// 边界路径：空串。
		{"", "配置变更"},
	}
	for _, tc := range cases {
		got := auditActionFallback(tc.action)
		if got != tc.want {
			t.Errorf("auditActionFallback(%q) = %q, 期望 %q", tc.action, got, tc.want)
		}
		if !hasCJK(got) {
			t.Errorf("auditActionFallback(%q) = %q 不含中文（会漏出内部 code）", tc.action, got)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
