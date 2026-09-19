package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// L7 核心需求：数据集评估用多 LLM 互评，生成该数据集的模型必须被排除。
// 这些测试用纯函数 ResolveJudges，不依赖 DB 与网络。

// provider 造一个「配置完整」的 provider。
//
// 必须填 APIKeyMasked：ResolveJudges 现在会剔除「没有密钥 / 没有 BaseURL / 没有模型名」
// 的 provider —— 因为 worker 执行侧本来就要求这些（见 LoadJudgeRefs 的
// `full.APIKey == ""` 判定）。候选列表与执行侧口径必须一致，否则界面会把
// 「根本没法调用」的 provider 当成可用裁判，用户选了之后运行才失败
// （父代理用真实验收复现：自动审查 harness 遗留的空 provider 被当成可用裁判）。
func provider(id int64, name, baseURL, modelName string) model.ModelProvider {
	return model.ModelProvider{
		ID:           id,
		Name:         name,
		BaseURL:      baseURL,
		Model:        modelName,
		ProviderType: "openai-compatible",
		IsActive:     true,
		APIKeyMasked: "****test",
	}
}

// 三个 provider（A/B/C），A 是生成者 —— 需求原文描述的场景。
func TestResolveJudgesExcludesGenerator(t *testing.T) {
	providers := []model.ModelProvider{
		provider(1, "A", "http://a.example/v1", "model-a"),
		provider(2, "B", "http://b.example/v1", "model-b"),
		provider(3, "C", "http://c.example/v1", "model-c"),
	}

	judges, records, err := ResolveJudges(context.Background(), providers, 1)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}

	if len(judges) != 2 {
		t.Fatalf("expected 2 usable judges (B, C), got %d: %+v", len(judges), judges)
	}
	for _, judge := range judges {
		if judge.ProviderID == 1 {
			t.Fatalf("generator provider 1 must not be a judge, got %+v", judge)
		}
	}
	if judges[0].ProviderID != 2 || judges[1].ProviderID != 3 {
		t.Fatalf("expected judges [2 3], got [%d %d]", judges[0].ProviderID, judges[1].ProviderID)
	}

	// 被剔除项也要落库，且原因可读。
	if len(records) != 3 {
		t.Fatalf("expected 3 records (including excluded), got %d", len(records))
	}
	var generator model.EvalRunJudge
	for _, record := range records {
		if record.ProviderID == 1 {
			generator = record
		}
	}
	if !generator.Excluded {
		t.Fatalf("generator record must be marked excluded: %+v", generator)
	}
	if generator.ExcludeReason != ExcludeReasonGenerator {
		t.Fatalf("expected reason %q, got %q", ExcludeReasonGenerator, generator.ExcludeReason)
	}
	if generator.ProviderName != "A" || generator.Model != "model-a" {
		t.Fatalf("excluded record must still carry provider identity: %+v", generator)
	}
}

// 同一模型用不同 id/名字注册成多行时，仍须识别为同源并剔除。
func TestResolveJudgesExcludesSameSource(t *testing.T) {
	providers := []model.ModelProvider{
		provider(1, "A", "http://a.example/v1", "model-a"),
		// 同源：同 baseURL + 同 model，只是注册成了另一个 provider。
		provider(7, "A-alias", "http://a.example/v1/", "Model-A"),
		provider(2, "B", "http://b.example/v1", "model-b"),
	}

	judges, records, err := ResolveJudges(context.Background(), providers, 1)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}

	if len(judges) != 1 || judges[0].ProviderID != 2 {
		t.Fatalf("expected only provider 2 usable, got %+v", judges)
	}

	reasons := map[int64]string{}
	for _, record := range records {
		if record.Excluded {
			reasons[record.ProviderID] = record.ExcludeReason
		}
	}
	if reasons[1] != ExcludeReasonGenerator {
		t.Fatalf("provider 1 reason: got %q", reasons[1])
	}
	if reasons[7] != ExcludeReasonSameSource {
		t.Fatalf("same-source provider 7 must be excluded, got %q", reasons[7])
	}
}

// 本地环境只配了 1 个 provider（且它就是生成者）时，全部裁判都被剔除。
// 这是正确行为而非缺陷：要多 LLM 互评至少需再配 2 个 provider（配置输入缺失）。
func TestResolveJudgesSingleProviderYieldsNoJudges(t *testing.T) {
	providers := []model.ModelProvider{provider(1, "only", "http://a.example/v1", "model-a")}

	judges, records, err := ResolveJudges(context.Background(), providers, 1)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}
	if len(judges) != 0 {
		t.Fatalf("expected no usable judges, got %+v", judges)
	}
	if len(records) != 1 || !records[0].Excluded {
		t.Fatalf("expected 1 excluded record, got %+v", records)
	}
}

// 未启用的 provider 不能被选为裁判。
func TestResolveJudgesExcludesInactive(t *testing.T) {
	inactive := provider(2, "B", "http://b.example/v1", "model-b")
	inactive.IsActive = false
	providers := []model.ModelProvider{
		provider(1, "A", "http://a.example/v1", "model-a"),
		inactive,
	}

	judges, records, err := ResolveJudges(context.Background(), providers, 1)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}
	if len(judges) != 0 {
		t.Fatalf("inactive provider must not be a judge, got %+v", judges)
	}
	for _, record := range records {
		if record.ProviderID == 2 && record.ExcludeReason != ExcludeReasonInactive {
			t.Fatalf("expected inactive reason, got %q", record.ExcludeReason)
		}
	}
}

// generatorProviderID=0 表示生成者未知：不做剔除，全部可选。
func TestResolveJudgesUnknownGeneratorKeepsAll(t *testing.T) {
	providers := []model.ModelProvider{
		provider(1, "A", "http://a.example/v1", "model-a"),
		provider(2, "B", "http://b.example/v1", "model-b"),
	}

	judges, records, err := ResolveJudges(context.Background(), providers, 0)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}
	if len(judges) != 2 {
		t.Fatalf("expected 2 judges when generator unknown, got %+v", judges)
	}
	for _, record := range records {
		if record.Excluded {
			t.Fatalf("no record should be excluded when generator unknown: %+v", record)
		}
	}
}

// 重复 provider id 会让 eval_run_judges 的 UNIQUE 约束在写库时炸，提前拦下。
func TestResolveJudgesRejectsDuplicateProvider(t *testing.T) {
	providers := []model.ModelProvider{
		provider(1, "A", "http://a.example/v1", "model-a"),
		provider(1, "A-dup", "http://a.example/v1", "model-a"),
	}

	if _, _, err := ResolveJudges(context.Background(), providers, 0); err == nil {
		t.Fatal("expected error for duplicate provider id, got nil")
	} else if !strings.Contains(err.Error(), "duplicate provider id") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// 空候选列表是合法输入（尚未配置任何 provider），返回空结果而非报错。
func TestResolveJudgesEmptyCandidates(t *testing.T) {
	judges, records, err := ResolveJudges(context.Background(), nil, 1)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}
	if len(judges) != 0 || len(records) != 0 {
		t.Fatalf("expected empty results, got judges=%+v records=%+v", judges, records)
	}
}

// 裁判引用转成调用配置时，密钥与地址必须原样带过去，否则打分必然失败。
func TestJudgeRefProviderConfig(t *testing.T) {
	judge := JudgeRef{
		ProviderID:   2,
		ProviderName: "B",
		Model:        "model-b",
		BaseURL:      "http://b.example/v1",
		ProviderType: "openai-compatible",
		APIKey:       "secret-key",
	}

	config := judge.ProviderConfig()
	if config.BaseURL != judge.BaseURL || config.APIKey != judge.APIKey ||
		config.Model != judge.Model || config.ProviderType != judge.ProviderType {
		t.Fatalf("provider config mismatch: %+v", config)
	}
}

// 无裁判时不得发出调用，否则会带着空配置打网络请求。
func TestScoreJSONRejectsZeroJudge(t *testing.T) {
	err := ScoreJSON(context.Background(), JudgeRef{}, "", "", nil, 0)
	if err == nil {
		t.Fatal("expected error for zero judge, got nil")
	}
	if !strings.Contains(err.Error(), "judge provider is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// 缺 API Key 的裁判不得被当作可用裁判（否则评分阶段才静默失败）。
func TestLoadJudgeRefsRejectsNilStore(t *testing.T) {
	if _, _, err := LoadJudgeRefs(context.Background(), nil, 1); err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
}

// markExcluded 只改目标 provider，不得误伤其他记录。
func TestMarkExcludedTargetsOnlyGivenProvider(t *testing.T) {
	records := []model.EvalRunJudge{
		{ProviderID: 1, ProviderName: "A"},
		{ProviderID: 2, ProviderName: "B"},
	}

	markExcluded(records, 2, ExcludeReasonNoAPIKey)

	if records[0].Excluded || records[0].ExcludeReason != "" {
		t.Fatalf("provider 1 must stay untouched: %+v", records[0])
	}
	if !records[1].Excluded || records[1].ExcludeReason != ExcludeReasonNoAPIKey {
		t.Fatalf("provider 2 must be excluded with reason: %+v", records[1])
	}
}

// TestResolveJudgesExcludesMisconfiguredProviders 锁定「候选列表与执行侧口径一致」。
//
// 背景（父代理用真实验收发现的缺陷）：界面的「可用裁判」列表此前只判
// 「生成者 / 同源 / 未启用」，而 worker 执行侧（LoadJudgeRefs）还要求
// provider 有可用的 API Key、BaseURL 与 Model。于是自动审查 harness 遗留的
// 空 provider（无 model、base_url=not-a-url、无 API key）会出现在候选里，
// 用户按界面提示选了它们，运行到 worker 才失败，且错误信息把用户指向
// 「生成者模型禁止自评」这个**错误方向**。
//
// 本测试断言：配置不完整的 provider 必须在**候选阶段**就被剔除并给出可操作原因。
func TestResolveJudgesExcludesMisconfiguredProviders(t *testing.T) {
	full := provider(2, "B", "http://b.example/v1", "model-b")

	noKey := provider(3, "C", "http://c.example/v1", "model-c")
	noKey.APIKeyMasked = ""

	noBaseURL := provider(4, "D", "", "model-d")

	noModel := provider(5, "E", "http://e.example/v1", "")

	providers := []model.ModelProvider{full, noKey, noBaseURL, noModel}

	judges, records, err := ResolveJudges(context.Background(), providers, 0)
	if err != nil {
		t.Fatalf("ResolveJudges returned error: %v", err)
	}

	// 只有配置完整的 B 可用。
	if len(judges) != 1 || judges[0].ProviderID != 2 {
		t.Fatalf("只有配置完整的 provider 2 应可用，实际 judges=%+v", judges)
	}

	// 三个配置不完整的都必须带**可操作**的剔除原因（而不是笼统的「未启用」）。
	wantReason := map[int64]string{
		3: ExcludeReasonNoAPIKey,
		4: ExcludeReasonNoBaseURL,
		5: ExcludeReasonNoModel,
	}
	got := map[int64]string{}
	for _, record := range records {
		if record.Excluded {
			got[record.ProviderID] = record.ExcludeReason
		}
	}
	for id, reason := range wantReason {
		if got[id] != reason {
			t.Errorf("provider %d 的剔除原因应为 %q，实际 %q（用户需要知道**具体缺什么**才能去修）",
				id, reason, got[id])
		}
	}
	if len(got) != 3 {
		t.Errorf("应有 3 个被剔除项，实际 %d：%+v", len(got), got)
	}
}
