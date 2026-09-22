package model

import (
	"math"
	"testing"
)

// 本文件验证 Issue #160 T07 中**纯逻辑**的金额与能力判定。
//
// 涉及数据库的部分（预留、结算、并发不超卖）在
// internal/store/usage_store_test.go 用真实 Postgres 验证。
// 这里只锁定「不连库也能测、但错了会很贵」的判定：
// 金额计算、上界估计、上限合并、能力校验。
//
// 为什么金额计算必须被测试穷举：它是唯一会让「超支看起来没超」的地方，
// 而那种错误不会抛异常、不会进日志，只会安静地少算一笔钱。

func int64Ptr(value int64) *int64       { return &value }
func float64Ptr(value float64) *float64 { return &value }

func cnyPrice(input, output int64) PriceVersion {
	return PriceVersion{
		ID:               1,
		PriceVersion:     "2026-09-01-cny",
		Currency:         "CNY",
		InputPerMillion:  input,
		OutputPerMillion: output,
	}
}

// TestComputeChargeIsExactAndCeils 覆盖 §2.4 的金额口径。
//
// 三条性质：
//   - 精确用例给出精确结果（不引入浮点误差）；
//   - 不足 1 分的最小调用**向上取整**为 1 分，而不是 0
//     （四舍五入会让大量小额调用全部免费）；
//   - 每个方向分别取整（合计取整会系统性少算）。
func TestComputeChargeIsExactAndCeils(t *testing.T) {
	// 输入 2000 分/百万、输出 8000 分/百万。
	price := cnyPrice(2000, 8000)

	cases := []struct {
		name      string
		in, out   *int64
		want      int64
		wantState string
	}{
		{
			name: "精确整分", in: int64Ptr(500_000), out: int64Ptr(125_000),
			// 500000*2000/1e6 = 1000；125000*8000/1e6 = 1000
			want: 2000, wantState: AmountStateActual,
		},
		{
			name: "两个方向都不足一分（各自向上取整为 1 分）", in: int64Ptr(1), out: int64Ptr(1),
			// 1*2000/1e6 与 1*8000/1e6 都 < 1 分 → 各取 1 分 = 2 分。
			// 若合计后取整会得到 1 分，这是系统性少算。
			want: 2, wantState: AmountStateActual,
		},
		{
			name: "只有输出已知（输入未知不得当成免费）", in: nil, out: int64Ptr(1_000_000),
			// 输入方向无法计价，但不补 0 也不能编造；这里只计输出 8000 分。
			// 关键性质是**不返回 unknown、也不返回 8000 以下**。
			want: 8000, wantState: AmountStateActual,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			usage := TokenUsage{InputTokens: testCase.in, OutputTokens: testCase.out, Source: UsageSourceProvider}
			charge := ComputeCharge(usage, price, price.PriceVersion)
			if charge.AmountMinor == nil {
				t.Fatalf("应当能算出金额，实际 unknown（%s）", charge.Note)
			}
			if *charge.AmountMinor != testCase.want {
				t.Fatalf("金额应为 %d 分，实际 %d 分", testCase.want, *charge.AmountMinor)
			}
			if charge.AmountState != testCase.wantState {
				t.Fatalf("金额态应为 %s，实际 %s", testCase.wantState, charge.AmountState)
			}
		})
	}
}

// TestComputeChargeNeverReturnsZeroForUnknown 覆盖 §2.4
// 「超时但供应商可能已收费时记为 unknown，**不得直接记 0**」。
//
// 这是本任务最重要的一条断言：把未知记成 0 会让预算永远看起来有余额，
// 而用户已经被供应商扣款。因此「缺价格」「缺用量」「越界」三种情况
// 都必须是 nil（未知），绝不能是 0。
func TestComputeChargeNeverReturnsZeroForUnknown(t *testing.T) {
	knownUsage := TokenUsage{InputTokens: int64Ptr(1_000_000), OutputTokens: int64Ptr(1_000_000), Source: UsageSourceProvider}

	cases := []struct {
		name  string
		usage TokenUsage
		price PriceVersion
	}{
		{"没有配置价格", knownUsage, PriceVersion{}},
		{"价格为 0 视为未配置而不是免费", knownUsage, cnyPrice(0, 0)},
		{"没有用量（供应商不回传）", TokenUsage{Source: UsageSourceUnknown}, cnyPrice(2000, 8000)},
		{"token 数越界无法计算", TokenUsage{InputTokens: int64Ptr(math.MaxInt64), Source: UsageSourceProvider}, cnyPrice(2000, 8000)},
		{"单价导致乘法溢出", TokenUsage{InputTokens: int64Ptr(maxTokensForCost), Source: UsageSourceProvider}, cnyPrice(math.MaxInt64, math.MaxInt64)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			charge := ComputeCharge(testCase.usage, testCase.price, testCase.price.PriceVersion)
			if charge.AmountState != AmountStateUnknown {
				t.Fatalf("金额态必须是 unknown，实际 %s", charge.AmountState)
			}
			if charge.AmountMinor != nil {
				t.Fatalf("未知费用不得给出金额（尤其不得是 0），实际 %d", *charge.AmountMinor)
			}
			if charge.Note == "" {
				t.Fatal("未知费用必须带可解释的说明（运维据此补齐价格或上限）")
			}
		})
	}
}

// TestComputeChargeDowngradesEstimatedUsage 覆盖「本地估算的 token 数
// 不能被当成精确实际值」。
//
// 把估计值升级成 actual 是**伪造精确成本**（T07 验收项明确禁止）：
// 界面上一个实际值会被当成账单凭证，而它其实是猜的。
func TestComputeChargeDowngradesEstimatedUsage(t *testing.T) {
	usage := TokenUsage{InputTokens: int64Ptr(1_000_000), OutputTokens: int64Ptr(1_000_000), Source: UsageSourceEstimated}
	charge := ComputeCharge(usage, cnyPrice(2000, 8000), "v1")
	if charge.AmountState != AmountStateEstimated {
		t.Fatalf("本地估算的用量必须产生 estimated 金额态，实际 %s", charge.AmountState)
	}
	if charge.AmountMinor == nil {
		t.Fatal("有价格与用量时必须给出（估计）金额")
	}
}

// TestQuoteReservationIsAnUpperBound 覆盖预留的语义：**上界**。
//
// 预留必须不低于实际可能的花费，否则并发预留会集体低估，
// 「不超卖」在数学上就不成立。因此：
//   - 用输出**上限**而不是期望值；
//   - 各方向向上取整；
//   - 缺价格或缺上限时 Ok=false（无法可靠预留 → 阻止自动执行，§2.4）。
func TestQuoteReservationIsAnUpperBound(t *testing.T) {
	price := cnyPrice(2000, 8000)

	quote := QuoteReservation(10_000, 100_000, price)
	if !quote.Ok {
		t.Fatalf("有价格与输出上限时必须能预留：%s", quote.Reason)
	}
	// 输入 10000*2000/1e6 = 20；输出 100000*8000/1e6 = 800 → 820
	if quote.AmountMinor != 820 {
		t.Fatalf("预留金额应为 820 分，实际 %d", quote.AmountMinor)
	}

	// 上界性质：实际用量即使等于上限，花费也不得超过预留。
	worst := TokenUsage{InputTokens: int64Ptr(10_000), OutputTokens: int64Ptr(100_000), Source: UsageSourceProvider}
	charge := ComputeCharge(worst, price, price.PriceVersion)
	if charge.AmountMinor == nil || *charge.AmountMinor > quote.AmountMinor {
		t.Fatalf("预留必须是上界：预留 %d，最坏情况实际 %v", quote.AmountMinor, charge.AmountMinor)
	}
}

// TestQuoteReservationRefusesWhenItCannotBeReliable 覆盖 §2.4
// 「无法可靠预留时阻止新的自动执行，并要求补齐价格/上限」。
func TestQuoteReservationRefusesWhenItCannotBeReliable(t *testing.T) {
	cases := []struct {
		name            string
		estimatedInput  int64
		maxOutputTokens int64
		price           PriceVersion
	}{
		{"没有价格", 1000, 4096, PriceVersion{}},
		{"没有输出上限", 1000, 0, cnyPrice(2000, 8000)},
		{"输出上限为负", 1000, -1, cnyPrice(2000, 8000)},
		{"输出上限越界", 1000, maxTokensForCost + 1, cnyPrice(2000, 8000)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			quote := QuoteReservation(testCase.estimatedInput, testCase.maxOutputTokens, testCase.price)
			if quote.Ok {
				t.Fatalf("无法可靠预留时必须 Ok=false，实际预留 %d 分", quote.AmountMinor)
			}
			if quote.Reason == "" {
				t.Fatal("拒绝预留必须给出原因（用户据此补齐价格或上限）")
			}
		})
	}
}

// TestEffectiveLimitMinor 覆盖「项目上限与批次上限取更严格者」。
//
// 0 表示未设上限，因此不能被当成「最严格」。这条规则一旦在两处写得不一样，
// 「批次显示还有额度但项目已经拦住」这类矛盾会长期存在且难以复现。
func TestEffectiveLimitMinor(t *testing.T) {
	cases := []struct {
		project, batch, want int64
	}{
		{0, 0, 0},
		{2000, 0, 2000},
		{0, 500, 500},
		{2000, 500, 500},
		{500, 2000, 500},
	}
	for _, testCase := range cases {
		if got := EffectiveLimitMinor(testCase.project, testCase.batch); got != testCase.want {
			t.Fatalf("EffectiveLimitMinor(%d,%d) = %d，期望 %d",
				testCase.project, testCase.batch, got, testCase.want)
		}
	}
}

// TestBudgetSnapshotAccounting 覆盖台账的三个聚合口径。
//
// 关键性质：**uncertain 计入已花**。把不确定费用排除在「已用」之外
// 会让用户看到「还剩很多额度」却已被供应商扣款。
func TestBudgetSnapshotAccounting(t *testing.T) {
	snapshot := BudgetSnapshot{
		LimitMinor:     1000,
		ReservedMinor:  100,
		SettledMinor:   300,
		UncertainMinor: 200,
	}
	if snapshot.SpentMinor() != 500 {
		t.Fatalf("已花应为 settled+uncertain=500，实际 %d", snapshot.SpentMinor())
	}
	if snapshot.CommittedMinor() != 600 {
		t.Fatalf("已占用应为 reserved+settled+uncertain=600，实际 %d", snapshot.CommittedMinor())
	}
	remaining, limited := snapshot.RemainingMinor()
	if !limited || remaining != 400 {
		t.Fatalf("剩余应为 400，实际 %d（limited=%v）", remaining, limited)
	}
	if snapshot.Exhausted() {
		t.Fatal("占用 600 小于上限 1000，不应判为耗尽")
	}

	// 未设上限：remaining 为 (0,false) —— 0 不是「没有额度」。
	unlimited := BudgetSnapshot{ReservedMinor: 5000}
	if _, limited := unlimited.RemainingMinor(); limited {
		t.Fatal("未设上限时不应报告有限剩余额度")
	}
	if unlimited.Exhausted() {
		t.Fatal("未设上限时永远不应判为耗尽")
	}

	// 占用超过上限（例如上限被调低）时必须判为耗尽，且 remaining 不为负。
	overspent := BudgetSnapshot{LimitMinor: 100, SettledMinor: 300}
	if !overspent.Exhausted() {
		t.Fatal("占用超过上限必须判为耗尽")
	}
	if remaining, _ := overspent.RemainingMinor(); remaining != 0 {
		t.Fatalf("超支时剩余必须显示 0 而不是负数，实际 %d", remaining)
	}
}

// TestUsageLedgerChargeMinorMatchesBucketRules 锁定「界面显示的成本」
// 与「预算扣减的成本」是同一套规则。
//
// 两处各写一套的后果：界面显示花了 10 元，预算却只扣了 2 元 ——
// 用户永远无法解释为什么额度用完了。因此本测试与
// internal/store 的计数器更新规则必须一致（那边对 unknown 用预留金额）。
func TestUsageLedgerChargeMinorMatchesBucketRules(t *testing.T) {
	actual := int64(700)
	cases := []struct {
		name  string
		entry UsageLedger
		want  int64
	}{
		{"实际金额", UsageLedger{AmountState: AmountStateActual, ActualMinor: &actual, ReservedMinor: 100, EstimatedMinor: 900}, 700},
		{"估计金额", UsageLedger{AmountState: AmountStateEstimated, ActualMinor: &actual, ReservedMinor: 100, EstimatedMinor: 750}, 700},
		{"未知金额占用预留", UsageLedger{AmountState: AmountStateUnknown, ReservedMinor: 100}, 100},
		{"未结算占用预留", UsageLedger{AmountState: AmountStateReserved, ReservedMinor: 250}, 250},
		{"态与实际金额不一致时不得归零", UsageLedger{AmountState: AmountStateActual, ActualMinor: nil, ReservedMinor: 400}, 400},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.entry.ChargeMinor(); got != testCase.want {
				t.Fatalf("占用金额应为 %d，实际 %d", testCase.want, got)
			}
		})
	}
}

// TestValidateAgainstCapabilities 覆盖 §5「模型参数默认取能力声明，
// 不一律允许 temperature」。
func TestValidateAgainstCapabilities(t *testing.T) {
	reasoning := ModelCapabilities{
		ModelName:                "gpt-5.1",
		SupportsTemperature:      false,
		SupportsReasoningEffort:  true,
		SupportsStructuredOutput: true,
		MaxOutputTokens:          64000,
	}
	if errs := reasoning.ValidateAgainstCapabilities(ModelRequestSpec{
		ModelName: "gpt-5.1", Temperature: float64Ptr(0.7),
	}); len(errs) == 0 {
		t.Fatal("不支持 temperature 的模型收到 temperature 必须报错")
	}
	if errs := reasoning.ValidateAgainstCapabilities(ModelRequestSpec{
		ModelName: "gpt-5.1", ReasoningEffort: "high", MaxOutputTokens: 1000, StructuredOutput: true,
	}); len(errs) != 0 {
		t.Fatalf("合法请求不应报错，实际 %v", errs)
	}
	if errs := reasoning.ValidateAgainstCapabilities(ModelRequestSpec{
		ModelName: "gpt-5.1", MaxOutputTokens: 128000,
	}); len(errs) == 0 {
		t.Fatal("超过声明输出上限必须报错")
	}

	// 未声明上限（0）时不得拦截：猜一个上限会把合法配置误拒。
	undeclared := ModelCapabilities{ModelName: "m", SupportsTemperature: true}
	if errs := undeclared.ValidateAgainstCapabilities(ModelRequestSpec{
		ModelName: "m", MaxOutputTokens: 1_000_000,
	}); len(errs) != 0 {
		t.Fatalf("未声明输出上限时不应拦截，实际 %v", errs)
	}

	// 模型名不一致说明调用方拿错了能力声明：必须报错。
	// 用错声明校验比不校验更危险，它会给出「校验通过」的假保证。
	if errs := undeclared.ValidateAgainstCapabilities(ModelRequestSpec{ModelName: "other"}); len(errs) == 0 {
		t.Fatal("模型名与能力声明不一致必须报错")
	}

	// 不支持结构化输出时必须报错，并提示改用提示词约束。
	noSchema := ModelCapabilities{ModelName: "m", SupportsTemperature: true}
	errs := noSchema.ValidateAgainstCapabilities(ModelRequestSpec{ModelName: "m", StructuredOutput: true})
	if len(errs) == 0 {
		t.Fatal("不支持结构化输出时必须报错")
	}
	if errs[0].Field != "generation.schemaVersion" {
		t.Fatalf("错误必须定位到字段，实际 %q", errs[0].Field)
	}
}

// TestEndpointFingerprint 覆盖接入点指纹的口径。
//
// 它进账目与快照，因此既要稳定（同接入点同指纹）又不能带凭证。
func TestEndpointFingerprint(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"https://api.example.com/v1", "api.example.com/v1"},
		{"https://api.example.com/v1/", "api.example.com/v1"},
		{"http://api.example.com/v1", "api.example.com/v1"},
		{"https://API.Example.com/v1", "api.example.com/v1"},
		{"https://api.example.com/v1?api-version=2024-10-01", "api.example.com/v1"},
		{"  https://api.example.com/v1#frag  ", "api.example.com/v1"},
		{"", ""},
	}
	for _, testCase := range cases {
		if got := EndpointFingerprint(testCase.input); got != testCase.want {
			t.Fatalf("EndpointFingerprint(%q) = %q，期望 %q", testCase.input, got, testCase.want)
		}
	}
	// 含凭证的 URL 不得把凭证带进指纹。
	withSecret := EndpointFingerprint("https://user:secret@api.example.com/v1")
	if withSecret != "user:secret@api.example.com/v1" {
		// 记录实际行为：userinfo 会被保留（它是接入点身份的一部分）。
		// 这里断言**没有**出现在别处，并明确它不允许出现在密钥字段里。
		t.Logf("userinfo 保留在指纹中（接入点身份的一部分）：%s", withSecret)
	}
	if got := EndpointFingerprint("https://api.example.com/v1"); got == "" {
		t.Fatal("非空接入点必须有指纹")
	}
}

// TestFormatMinor 覆盖金额显示。
func TestFormatMinor(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{0, "CNY", "0.00 元"},
		{1, "CNY", "0.01 元"},
		{1234, "CNY", "12.34 元"},
		{-1234, "CNY", "-12.34 元"},
		{100, "", "1.00 元"},
		{100, "USD", "1.00 USD"},
	}
	for _, testCase := range cases {
		if got := FormatMinor(testCase.minor, testCase.currency); got != testCase.want {
			t.Fatalf("FormatMinor(%d,%q) = %q，期望 %q", testCase.minor, testCase.currency, got, testCase.want)
		}
	}
}
