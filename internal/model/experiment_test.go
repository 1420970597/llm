package model

import (
	"strings"
	"testing"
)

// 本文件验证 Issue #160 T14 的**判据**（不依赖数据库的部分）。
//
// T14 的验收项大多与具体数值和判定方向有关，而方向写反不会报错：
// 它会安静地让「没评」显示成「评得很差」，或让「自评」通过独立性检查。

// TestCheckJudgeIndependenceRejectsAliasConnections 覆盖验收项
// 「至少一名独立裁判，同真实来源的别名连接不得自评」。
//
// 「别名连接」是真实形态：同一个人用两条 provider 记录指向同一个 endpoint
// （主账号/备用账号）。判据必须是 **endpoint 指纹**，而不是连接 ID ——
// 用连接 ID 判断会让别名连接看起来是两名裁判，于是自评被放行。
func TestCheckJudgeIndependenceRejectsAliasConnections(t *testing.T) {
	sources := []GeneratorSource{{ConnectionID: 1, EndpointFingerprint: "api.vendor.com/v1"}}

	// 只有一条指向同一 endpoint 的别名连接：不独立 → 不能跑。
	aliasOnly := []JudgeSpec{{ConnectionID: 2, EndpointFingerprint: "api.vendor.com/v1", Label: "备用账号"}}
	ok, coverage := CheckJudgeIndependence(aliasOnly, sources)
	if ok {
		t.Fatal("同真实来源的别名连接不得算作独立裁判（否则是自评）")
	}
	if len(coverage["api.vendor.com/v1"]) != 0 {
		t.Fatalf("别名连接不应出现在独立裁判清单里：%v", coverage)
	}

	// 一条真正的独立裁判即可跑。
	mixed := []JudgeSpec{
		{ConnectionID: 2, EndpointFingerprint: "api.vendor.com/v1"},
		{ConnectionID: 3, EndpointFingerprint: "other-judge.example.com/v1"},
	}
	ok, coverage = CheckJudgeIndependence(mixed, sources)
	if !ok {
		t.Fatal("存在独立裁判时必须允许创建实验")
	}
	if len(coverage["api.vendor.com/v1"]) != 1 || coverage["api.vendor.com/v1"][0] != 3 {
		t.Fatalf("逐来源的独立裁判清单必须准确，实际 %v", coverage)
	}
}

// TestCheckJudgeIndependenceIsConservativeWhenFingerprintMissing 覆盖
// 「配置不完整时保守判定」。
//
// 指纹缺失时判为**不独立**：反过来的取舍会让独立性在配置不全时静默失效，
// 而失效方向是「本该拦住的自评被放行」——那正是这条检查要防的。
func TestCheckJudgeIndependenceIsConservativeWhenFingerprintMissing(t *testing.T) {
	missingSource := []GeneratorSource{{ConnectionID: 1}}
	judge := JudgeSpec{ConnectionID: 2}
	if ok, _ := CheckJudgeIndependence([]JudgeSpec{judge}, missingSource); ok {
		t.Fatal("生成来源没有指纹时必须保守判为不独立")
	}
	// 同一条连接更是明确不独立。
	sameConn := []GeneratorSource{{ConnectionID: 5, EndpointFingerprint: "x"}}
	if ok, _ := CheckJudgeIndependence([]JudgeSpec{{ConnectionID: 5}}, sameConn); ok {
		t.Fatal("同一条连接不得与自己互为独立裁判")
	}
	// 大小写与首尾差异不应影响判定。
	upper := []GeneratorSource{{ConnectionID: 1, EndpointFingerprint: "API.Vendor.com/v1"}}
	if ok, _ := CheckJudgeIndependence([]JudgeSpec{{ConnectionID: 3, EndpointFingerprint: "api.vendor.com/v1"}}, upper); ok {
		t.Fatal("指纹比较必须大小写无关")
	}
}

// TestBuildExperimentStatsKeepsDenominatorFixed 覆盖验收项
// 「固定分母、完成覆盖和量表归一化可复算」。
//
// 关键性质：缺分与错误**不缩小**分母，也不被算成 0 分。
func TestBuildExperimentStatsKeepsDenominatorFixed(t *testing.T) {
	// 100 个冻结单元：87 已评、8 缺分、3 出错、2 不适用。
	stats := BuildExperimentStats(100, 87, 8, 3, 2, 0, 80)
	if stats.Inspected != 100 {
		t.Fatalf("分母必须固定为冻结的 100，实际 %d", stats.Inspected)
	}
	if stats.Coverage < 0.869 || stats.Coverage > 0.871 {
		t.Fatalf("完成覆盖应为 87/100，实际 %.4f", stats.Coverage)
	}
	if !strings.Contains(stats.CoverageDisplay, "87") {
		t.Fatalf("覆盖文案必须含实际覆盖：%q", stats.CoverageDisplay)
	}
	// 接纳率的分母是**纳入检查**的数（100），不是已评数（87）——
	// 用已评数当分母会让「少评一些」也能提高接纳率。
	if stats.AcceptanceRate == nil || *stats.AcceptanceRate != 0.8 {
		t.Fatalf("接纳率应为 80/100=0.8，实际 %v", stats.AcceptanceRate)
	}
}

// TestBuildExperimentStatsShowsNoConclusionOnZeroDenominator 覆盖
// 「零分母显示无结论，不是 100%」。
func TestBuildExperimentStatsShowsNoConclusionOnZeroDenominator(t *testing.T) {
	stats := BuildExperimentStats(0, 0, 0, 0, 0, 0, 0)
	if stats.AcceptanceRate != nil {
		t.Fatalf("零分母必须返回 nil 而不是 0 或 1，实际 %v", *stats.AcceptanceRate)
	}
	if stats.AcceptanceRateDisplay != "无结论" {
		t.Fatalf("零分母的展示文案必须是「无结论」，实际 %q", stats.AcceptanceRateDisplay)
	}
}

// TestNormalizedScoreRefusesInvalidRange 覆盖「无法归一化必须可区分」。
//
// 范围非法时返回 ok=false，调用方据此标成 missing；返回 0 会把它
// 当成最差分，从而把「量表配错了」显示成「内容质量差」。
func TestNormalizedScoreRefusesInvalidRange(t *testing.T) {
	if _, ok := NormalizedScore(5, RubricDimension{Key: "d", Min: 3, Max: 3}); ok {
		t.Fatal("上界等于下界时无法归一化，必须返回 ok=false")
	}
	normalized, ok := NormalizedScore(7.5, RubricDimension{Key: "d", Min: 0, Max: 10})
	if !ok || normalized != 0.75 {
		t.Fatalf("7.5/0–10 应归一化为 0.75，实际 %v（ok=%v）", normalized, ok)
	}
	// 越界如实返回（不夹紧）：夹紧会把量表配错这件事藏起来。
	if out, ok := NormalizedScore(20, RubricDimension{Key: "d", Min: 0, Max: 10}); !ok || out != 2 {
		t.Fatalf("越界必须如实返回 2 而不是夹到 1，实际 %v（ok=%v）", out, ok)
	}
}

// TestRubricSpecValidate 覆盖 §5「权重和必须为 1」与范围校验。
func TestRubricSpecValidate(t *testing.T) {
	valid := RubricSpec{Dimensions: []RubricDimension{
		{Key: "accuracy", Weight: 0.6, Min: 0, Max: 10},
		{Key: "reasoning", Weight: 0.4, Min: 0, Max: 10},
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法量表不应报错：%v", err)
	}

	// 浮点误差容差：0.1+0.2+0.7 在浮点下不等于 1.0，而它是合法配置。
	tolerant := RubricSpec{Dimensions: []RubricDimension{
		{Key: "a", Weight: 0.1, Min: 0, Max: 1},
		{Key: "b", Weight: 0.2, Min: 0, Max: 1},
		{Key: "c", Weight: 0.7, Min: 0, Max: 1},
	}}
	if err := tolerant.Validate(); err != nil {
		t.Fatalf("浮点误差内应视为权重和为 1：%v", err)
	}

	cases := []struct {
		name   string
		rubric RubricSpec
	}{
		{"空量表", RubricSpec{}},
		{"权重和不为 1", RubricSpec{Dimensions: []RubricDimension{
			{Key: "a", Weight: 0.6, Min: 0, Max: 1},
		}}},
		{"维度键重复", RubricSpec{Dimensions: []RubricDimension{
			{Key: "a", Weight: 0.5, Min: 0, Max: 1},
			{Key: "a", Weight: 0.5, Min: 0, Max: 1},
		}}},
		{"范围非法", RubricSpec{Dimensions: []RubricDimension{
			{Key: "a", Weight: 1, Min: 5, Max: 5},
		}}},
		{"权重为负", RubricSpec{Dimensions: []RubricDimension{
			{Key: "a", Weight: -1, Min: 0, Max: 1},
			{Key: "b", Weight: 2, Min: 0, Max: 1},
		}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.rubric.Validate(); err == nil {
				t.Fatalf("非法量表必须被拒绝：%+v", testCase.rubric)
			}
		})
	}
}

// TestExperimentRunnableBlocksGRPOUntilT24 覆盖验收项
// 「GRPO 在 T24 接入前明确不可运行」。
//
// 让 GRPO 用 SFT 的量表跑起来会产出「看起来正常、其实语义错误」的结论，
// 那比直接拒绝危险得多。
func TestExperimentRunnableBlocksGRPOUntilT24(t *testing.T) {
	ok, reason := ExperimentRunnable(TargetKindGRPO, "T14")
	if ok {
		t.Fatal("GRPO 实验在 T24 之前必须不可运行")
	}
	if !strings.Contains(reason, "T24") {
		t.Fatalf("拒绝原因必须点名负责的任务，实际 %q", reason)
	}
	if ok, _ := ExperimentRunnable(TargetKindSFT, "T14"); !ok {
		t.Fatal("SFT 实验应当可以运行")
	}
}

// TestExperimentCapabilitiesRerunCreatesNewExperiment 覆盖
// 「修改配置或完整重评新建 experiment」的形状：没有「就地编辑」能力。
func TestExperimentCapabilitiesRerunCreatesNewExperiment(t *testing.T) {
	capabilities := ExperimentCapabilities(ProjectRoleOwner, ExperimentStatusCompleted)
	if capabilities.CanEdit {
		t.Fatal("实验不得有就地编辑能力：改配置必须新建实验（否则历史报告会变）")
	}
	if !capabilities.CanRun {
		t.Fatal("owner 在非运行状态下应能重跑（重跑创建新实验）")
	}
	viewer := ExperimentCapabilities(ProjectRoleViewer, ExperimentStatusCompleted)
	if viewer.CanRun || viewer.CanEdit {
		t.Fatal("viewer 不得有运行/编辑能力")
	}
	if !viewer.CanDownload {
		t.Fatal("viewer 应能下载已发布内容（读权限）")
	}
}
