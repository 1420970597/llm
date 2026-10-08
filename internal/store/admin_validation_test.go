package store

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件覆盖 issue #209 的**单一来源**契约：保存时的校验
// （ValidateProviderInput）与展示时的"这条连接为什么不能用"
// （ProviderConfigIssues）必须由同一张规则表驱动。
//
// 为什么这条契约值得单独的测试：缺陷的形态不是"某条规则写错了"，而是
// **同一份事实在两处各自实现**。只测其中一处，"能存进去但界面说不可用"
// （或反过来）这类漂移仍然可以复现 —— 而它正是 #191/#206 反复出现的成因。

// 完整可用的连接：所有规则都不触发。
func completeProvider() model.ModelProvider {
	return model.ModelProvider{
		Name:           "hy3-judge",
		BaseURL:        "http://152.53.126.151:8885/v1",
		Model:          "global:hy3",
		MaxConcurrency: 4,
		TimeoutSeconds: 300,
		IsActive:       true,
	}
}

// TestProviderConfigIssuesEmptyForCompleteProvider 是正常路径：
// 一条完整配置的连接不得被标注任何问题。
func TestProviderConfigIssuesEmptyForCompleteProvider(t *testing.T) {
	if issues := ProviderConfigIssues(completeProvider()); len(issues) != 0 {
		t.Fatalf("完整连接不应有任何配置问题，实际 %v", issues)
	}
	// 停用是**显式意图**，不是配置错误：它必须不出现在配置问题里，
	// 否则界面会把"我故意停用的连接"标成坏数据。
	inactive := completeProvider()
	inactive.IsActive = false
	if issues := ProviderConfigIssues(inactive); len(issues) != 0 {
		t.Fatalf("停用本身不是配置问题，实际 %v", issues)
	}
}

// TestProviderConfigIssuesReportEveryBrokenField 是边界路径：
// 空连接（#209 实测的 6 条历史记录形态）必须被逐项列出，
// 而不是只报第一条 —— 用户需要一次看清"还缺什么"。
func TestProviderConfigIssuesReportEveryBrokenField(t *testing.T) {
	empty := model.ModelProvider{IsActive: true}
	issues := ProviderConfigIssues(empty)
	// 空连接违反 5 条规则：名称/基础 URL 为空、模型名称为空、
	// 并发数为 0、超时秒数为 0。baseUrl 的**格式**规则不触发（空值不算格式错），
	// 因此不是 6 条 —— 这正是"空 baseUrl 不该同时报为空与格式错"的不变式。
	if len(issues) != 5 {
		t.Fatalf("完全为空的连接应报 5 条问题（不含重复的 baseUrl 格式错），实际 %d 条：%v", len(issues), issues)
	}

	// base_url='not-a-url'（#209 实测的 id 16/20 形态）必须指出格式问题。
	invalidURL := completeProvider()
	invalidURL.BaseURL = "not-a-url"
	issues = ProviderConfigIssues(invalidURL)
	if len(issues) != 1 || !strings.Contains(issues[0], "http") {
		t.Fatalf("not-a-url 应报且只报基础 URL 不是完整的 http(s) 地址，实际 %v", issues)
	}
}

// TestProviderValidationAndIssuesStayInSync 是防漂移断言：
// 校验拒了而展示没标（或反之）都是自相矛盾，两个方向都要兜住。
//
// 实现方式是**逐条**用规则的最小违反样本过一遍两个入口：
// 若将来有人只改了其中一处，这里会有一侧的对不上。
func TestProviderValidationAndIssuesStayInSync(t *testing.T) {
	// 每个样本只违反恰好一条规则。
	broken := []struct {
		name           string
		mutate         func(*model.ModelProvider)
		wantIssueMatch string
	}{
		{"名称为空", func(p *model.ModelProvider) { p.Name = "" }, "服务名称为空"},
		{"基础 URL 为空", func(p *model.ModelProvider) { p.BaseURL = "" }, "基础 URL 为空"},
		{"基础 URL 不是完整地址", func(p *model.ModelProvider) { p.BaseURL = "example.invalid/v1" }, "http"},
		{"模型名称为空", func(p *model.ModelProvider) { p.Model = "" }, "模型名称为空"},
		{"并发数为 0", func(p *model.ModelProvider) { p.MaxConcurrency = 0 }, "最大并发数不是正数"},
		{"超时秒数为 0", func(p *model.ModelProvider) { p.TimeoutSeconds = 0 }, "超时秒数不是正数"},
	}
	for _, testCase := range broken {
		t.Run(testCase.name, func(t *testing.T) {
			input := completeProvider()
			testCase.mutate(&input)

			validationErr := ValidateProviderInput(input)
			if validationErr == nil {
				t.Fatalf("%s：保存路径必须拒绝该输入", testCase.name)
			}
			issues := ProviderConfigIssues(input)
			if len(issues) != 1 {
				t.Fatalf("%s：应恰好报 1 条问题，实际 %d 条 %v", testCase.name, len(issues), issues)
			}
			if !strings.Contains(issues[0], testCase.wantIssueMatch) {
				t.Fatalf("%s：问题文案应包含 %q，实际 %q", testCase.name, testCase.wantIssueMatch, issues[0])
			}
		})
	}

	// 反向：完整连接必须同时"能保存"且"没有问题"。
	valid := completeProvider()
	if err := ValidateProviderInput(valid); err != nil {
		t.Fatalf("完整连接被保存路径拒绝: %v", err)
	}
	if issues := ProviderConfigIssues(valid); len(issues) != 0 {
		t.Fatalf("完整连接被展示路径标注为有问题: %v", issues)
	}
}
