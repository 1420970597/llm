package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T07 的预算与用量不变量。
//
// 对应 T07 验收项逐条列出的六个场景：
//  1. 预算边界（恰好用满通过、再多一分被拒且不留痕）；
//  2. 多 worker 同时预留（不超卖，且不是靠「先查后写」碰运气）；
//  3. 超时但供应商可能已收费（记为 unknown，**不是 0**）；
//  4. 无 usage / 无价格（费用未知且阻止自动执行）；
//  5. 重试结算（幂等键命中不重复占用；重复结算报错而不是静默覆盖）；
//  6. 撤销连接时不偷偷换模型（价格与能力查询按连接严格匹配，
//     不回退到别的连接的配置）。
//
// 为什么必须连真实 Postgres：全部核心断言都是**并发下的单语句条件更新**
// 与**可空列的 NULL 语义**。内存实现「测」的话测的是测试自己的实现。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

// usageFixture 是一个项目 + 一个批次，供预算测试使用。
type usageFixture struct {
	pool      *pgxpool.Pool
	projectID int64
	userID    int64
	batchID   int64
	usage     *UsageStore
	batches   *BatchStore
}

// newUsageFixture 建项目、批次与用量 store。
func newUsageFixture(t *testing.T) usageFixture {
	t.Helper()
	batchFixture := newBatchFixture(t)
	ctx := context.Background()

	batch, err := batchFixture.batches.CreateBatch(ctx, batchFixture.projectID, batchFixture.editorID,
		model.TargetKindSFT, batchFixture.createBatchInput(model.BatchPurposePilot, 5))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	return usageFixture{
		pool:      batchFixture.pool,
		projectID: batchFixture.projectID,
		userID:    batchFixture.editorID,
		batchID:   batch.ID,
		usage:     NewUsageStore(batchFixture.pool),
		batches:   batchFixture.batches,
	}
}

// reserve 组装一次预留请求。limitMinor 是项目级生效上限。
func (fixture usageFixture) reserve(key string, amountMinor, limitMinor int64) ReserveUsageInput {
	batchID := fixture.batchID
	return ReserveUsageInput{
		ProjectID:         fixture.projectID,
		BatchID:           &batchID,
		Purpose:           "generation",
		IdempotencyKey:    key,
		ModelName:         "test-model",
		Currency:          "CNY",
		ReservationMinor:  amountMinor,
		ProjectLimitMinor: limitMinor,
	}
}

// TestReserveUsageEnforcesBudgetBoundary 覆盖验收项 1。
//
// 「恰好用满」必须通过，「再多一分」必须被拒 —— 边界包含关系写错一个符号
// 就会让预算形同虚设或多拦一次合法调用，而两者都不会抛异常。
// 同时断言被拒时**不留痕**：账目与台账都不能有半条记录，
// 否则一次失败的预留会永久占用额度。
func TestReserveUsageEnforcesBudgetBoundary(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	// 上限 1000：先占 400，再占 600（恰好用满），再占 1 分必须被拒。
	if _, created, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("boundary-1", 400, 1000)); err != nil || !created {
		t.Fatalf("第一次预留应当成功：created=%v err=%v", created, err)
	}
	if _, created, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("boundary-2", 600, 1000)); err != nil || !created {
		t.Fatalf("恰好用满上限必须通过：created=%v err=%v", created, err)
	}
	_, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("boundary-3", 1, 1000))
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("用满后再预留必须返回 ErrBudgetExhausted，实际 %v", err)
	}

	// 被拒的那笔不得留下任何记录。
	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM usage_ledger WHERE idempotency_key = $1`, "boundary-3").Scan(&count); err != nil {
		t.Fatalf("count usage: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒的预留不得写入账目，实际 %d 条", count)
	}

	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.ReservedMinor != 1000 {
		t.Fatalf("失败预留不得改变台账：reserved 应为 1000，实际 %d", snapshot.ReservedMinor)
	}
	if !snapshot.Exhausted() {
		t.Fatal("用满上限后必须报告已耗尽（界面据此阻止新提交）")
	}
}

// TestReserveUsageWithoutLimitNeverBlocks 覆盖「0 = 未设上限」。
//
// 未设上限时仍然累积用量（界面要显示已用多少），但绝不拦截。
// 把 0 当成「没有额度」会让所有未配置预算的项目完全无法运行 —— 那是
// 把「没设限」误读成「限 0」的典型后果。
func TestReserveUsageWithoutLimitNeverBlocks(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	for index := 0; index < 5; index++ {
		if _, created, err := fixture.usage.ReserveUsage(ctx,
			fixture.reserve(fmt.Sprintf("unlimited-%d", index), 1_000_000, 0)); err != nil || !created {
			t.Fatalf("未设上限时预留必须成功：created=%v err=%v", created, err)
		}
	}
	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.LimitMinor != 0 {
		t.Fatalf("未设上限时 limit 必须是 0，实际 %d", snapshot.LimitMinor)
	}
	if snapshot.ReservedMinor != 5_000_000 {
		t.Fatalf("未设上限也必须累积用量，实际 %d", snapshot.ReservedMinor)
	}
	if snapshot.Exhausted() {
		t.Fatal("未设上限永远不应报告耗尽")
	}
	if _, limited := snapshot.RemainingMinor(); limited {
		t.Fatal("未设上限不应报告有限剩余额度")
	}
}

// TestConcurrentReservationsDoNotOversell 覆盖验收项 2。
//
// 这是本任务的核心并发断言：上限只够 7 笔（每笔 100 分，上限 700），
// 20 个 goroutine 同时预留，**恰好 7 个**成功。
//
// 为什么必须并发测：「先 SELECT 判额度再 INSERT」的实现在单线程下完全正确，
// 只在并发时超卖。而超卖的直接后果是真实花钱超过预算 —— 它不会报错。
func TestConcurrentReservationsDoNotOversell(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	const (
		workers   = 20
		perAmount = 100
		limit     = 700
	)

	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	succeeded, rejected, otherErr := 0, 0, 0
	start := make(chan struct{})

	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			<-start // 尽量同时起跑，最大化竞争窗口
			_, created, err := fixture.usage.ReserveUsage(ctx,
				fixture.reserve(fmt.Sprintf("race-%d", index), perAmount, limit))
			mutex.Lock()
			defer mutex.Unlock()
			switch {
			case err == nil && created:
				succeeded++
			case errors.Is(err, ErrBudgetExhausted):
				rejected++
			default:
				otherErr++
				t.Logf("非预期错误（worker %d）：%v", index, err)
			}
		}(index)
	}
	close(start)
	waitGroup.Wait()

	if otherErr != 0 {
		t.Fatalf("并发预留不应出现非预期错误，实际 %d 个", otherErr)
	}
	if succeeded != 7 {
		t.Fatalf("上限 700 / 每笔 100 必须恰好成功 7 笔，实际 %d 笔（被拒 %d）", succeeded, rejected)
	}
	if rejected != workers-7 {
		t.Fatalf("其余必须全部被拒，实际被拒 %d", rejected)
	}

	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.ReservedMinor != limit {
		t.Fatalf("台账预留必须恰好等于上限 %d，实际 %d", limit, snapshot.ReservedMinor)
	}

	// 数据库层面的行数也必须一致：不能有「算成功但没写进去」的账目。
	var rows int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM usage_ledger WHERE idempotency_key LIKE 'race-%'`).Scan(&rows); err != nil {
		t.Fatalf("count race usage: %v", err)
	}
	if rows != succeeded {
		t.Fatalf("账目行数（%d）必须等于成功次数（%d）", rows, succeeded)
	}
}

// TestSettleUnknownKeepsReservationAsUncertain 覆盖验收项 3：
// 「超时但供应商可能已收费」必须记为 unknown，**不得直接记 0**。
//
// 断言三件事：
//   - actual_minor 是 NULL（不是 0）；
//   - 释放的在途预留转成 uncertain（不是凭空消失）；
//   - 台账的「已花」包含这笔不确定费用，因此剩余额度不会虚假变大。
func TestSettleUnknownKeepsReservationAsUncertain(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	reservation, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("timeout-1", 300, 1000))
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}

	// 超时：没有 usage、没有精确金额。
	settled, err := fixture.usage.SettleUsage(ctx, reservation.ID, SettleUsageInput{
		Charge: model.UsageCharge{
			AmountMinor: nil,
			AmountState: model.AmountStateUnknown,
			Usage:       model.TokenUsage{Source: model.UsageSourceUnknown},
		},
		ErrorClass: model.ErrorClassTimeout,
	})
	if err != nil {
		t.Fatalf("SettleUsage: %v", err)
	}
	if settled.State != model.UsageStateSettled {
		t.Fatalf("结算后状态必须是 settled，实际 %s", settled.State)
	}
	if settled.AmountState != model.AmountStateUnknown {
		t.Fatalf("金额态必须是 unknown，实际 %s", settled.AmountState)
	}
	if settled.ActualMinor != nil {
		t.Fatalf("未知费用不得写入实际金额（尤其不得是 0），实际 %d", *settled.ActualMinor)
	}
	if settled.Reconciliation != model.ReconciliationImpossible {
		t.Fatalf("没有可核对的金额必须标记为 impossible，实际 %s", settled.Reconciliation)
	}

	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.ReservedMinor != 0 {
		t.Fatalf("结算后不应再有在途预留，实际 %d", snapshot.ReservedMinor)
	}
	if snapshot.UncertainMinor != 300 {
		t.Fatalf("未知费用必须按预留金额进入 uncertain（记 0 会让额度虚假变大），实际 %d", snapshot.UncertainMinor)
	}
	if snapshot.SettledMinor != 0 {
		t.Fatalf("未知费用不得进入 settled（那会伪装成精确值），实际 %d", snapshot.SettledMinor)
	}
	if snapshot.SpentMinor() != 300 {
		t.Fatalf("「已花」必须包含不确定费用，实际 %d", snapshot.SpentMinor())
	}
}

// TestSettleWithUsageRecordsExactCost 覆盖正常路径：实际金额精确记账。
func TestSettleWithUsageRecordsExactCost(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	price, err := fixture.usage.UpsertPriceVersion(ctx, PriceVersionInput{
		PriceVersion: "pv-1", ConnectionID: fixture.connectionID(t),
		ModelName: "test-model", InputPerMillion: 2000, OutputPerMillion: 8000,
	})
	if err != nil {
		t.Fatalf("UpsertPriceVersion: %v", err)
	}

	input := fixture.reserve("exact-1", 820, 1000)
	priceID := price.ID
	input.PriceVersionID = &priceID
	input.PriceVersion = price.PriceVersion
	input.ConnectionID = &price.ConnectionID
	input.EndpointFP = price.EndpointFP
	reservation, _, err := fixture.usage.ReserveUsage(ctx, input)
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}

	usage := model.TokenUsage{
		InputTokens:  int64Pointer(10_000),
		OutputTokens: int64Pointer(100_000),
		Source:       model.UsageSourceProvider,
	}
	charge := model.ComputeCharge(usage, price, price.PriceVersion)
	if charge.AmountMinor == nil || *charge.AmountMinor != 820 {
		t.Fatalf("按 10000 输入 / 100000 输出应算出 820 分，实际 %v", charge.AmountMinor)
	}

	settled, err := fixture.usage.SettleUsage(ctx, reservation.ID, SettleUsageInput{
		Charge: charge, RequestID: "req-1", ResponseModelID: "test-model-2026",
	})
	if err != nil {
		t.Fatalf("SettleUsage: %v", err)
	}
	if settled.AmountState != model.AmountStateActual {
		t.Fatalf("金额态必须是 actual，实际 %s", settled.AmountState)
	}
	if settled.ActualMinor == nil || *settled.ActualMinor != 820 {
		t.Fatalf("实际金额应为 820，实际 %v", settled.ActualMinor)
	}
	if settled.RequestID != "req-1" || settled.ResponseModelID != "test-model-2026" {
		t.Fatalf("请求 ID 与响应模型必须留存（对账与「是否被换模型」取证），实际 %+v", settled)
	}

	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.SettledMinor != 820 || snapshot.ReservedMinor != 0 || snapshot.UncertainMinor != 0 {
		t.Fatalf("台账应为 settled=820/reserved=0/uncertain=0，实际 %d/%d/%d",
			snapshot.SettledMinor, snapshot.ReservedMinor, snapshot.UncertainMinor)
	}
}

// TestReleaseUsageFreesReservationWithoutCost 覆盖「能证明没花钱」的路径。
func TestReleaseUsageFreesReservationWithoutCost(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	reservation, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("release-1", 500, 1000))
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}
	released, err := fixture.usage.ReleaseUsage(ctx, reservation.ID, model.ErrorClassConfig, "连接被拒绝，请求未发出")
	if err != nil {
		t.Fatalf("ReleaseUsage: %v", err)
	}
	if released.State != model.UsageStateReleased {
		t.Fatalf("释放后状态必须是 released，实际 %s", released.State)
	}

	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.CommittedMinor() != 0 {
		t.Fatalf("释放必须把额度还回去，实际占用 %d", snapshot.CommittedMinor())
	}
}

// TestReserveUsageIdempotentAndSettlementIsNotRepeatable 覆盖验收项 5
// 「重试结算」。
//
// 两半都必须成立：
//   - 同一幂等键重复预留**不重复占用额度**（断网重放的真实形态）；
//   - 重复结算**报错**而不是静默覆盖（静默覆盖会让多扣的钱无从发现）。
func TestReserveUsageIdempotentAndSettlementIsNotRepeatable(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	first, created, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("retry-1", 200, 1000))
	if err != nil || !created {
		t.Fatalf("首次预留：created=%v err=%v", created, err)
	}
	again, createdAgain, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("retry-1", 200, 1000))
	if err != nil {
		t.Fatalf("幂等预留不应报错：%v", err)
	}
	if createdAgain {
		t.Fatal("同幂等键必须命中既有账目，不得新建")
	}
	if again.ID != first.ID {
		t.Fatalf("必须返回同一条账目：%d vs %d", first.ID, again.ID)
	}

	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.ReservedMinor != 200 {
		t.Fatalf("幂等重放不得重复占用额度，实际 %d", snapshot.ReservedMinor)
	}

	amount := int64(150)
	if _, err := fixture.usage.SettleUsage(ctx, first.ID, SettleUsageInput{
		Charge: model.UsageCharge{
			AmountMinor: &amount, AmountState: model.AmountStateActual,
			Usage: model.TokenUsage{Source: model.UsageSourceProvider},
		},
	}); err != nil {
		t.Fatalf("首次结算：%v", err)
	}
	if _, err := fixture.usage.SettleUsage(ctx, first.ID, SettleUsageInput{
		Charge: model.UsageCharge{
			AmountMinor: &amount, AmountState: model.AmountStateActual,
			Usage: model.TokenUsage{Source: model.UsageSourceProvider},
		},
	}); !errors.Is(err, ErrUsageNotReserved) {
		t.Fatalf("重复结算必须返回 ErrUsageNotReserved，实际 %v", err)
	}

	// 重复结算不得改变台账（否则会重复扣钱）。
	snapshot, err = fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.SettledMinor != 150 || snapshot.ReservedMinor != 0 {
		t.Fatalf("重复结算不得改变台账，实际 settled=%d reserved=%d",
			snapshot.SettledMinor, snapshot.ReservedMinor)
	}
	if _, err := fixture.usage.ReleaseUsage(ctx, first.ID, "", ""); !errors.Is(err, ErrUsageNotReserved) {
		t.Fatalf("已结算的账目不得再释放，实际 %v", err)
	}
}

// TestPriceLookupNeverFallsBackToAnotherConnection 覆盖验收项 6
// 「撤销连接时不偷偷换模型」的价格面。
//
// 语义：价格与能力都按 (连接, 模型) 严格匹配。如果它按模型名模糊匹配，
// 那么连接 A 被撤销后系统会**静默地用连接 B 的价格**计价 —— 用户看到的
// 成本与实际扣款不一致，而且不会有任何提示。
func TestPriceLookupNeverFallsBackToAnotherConnection(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	connectionA := fixture.connectionID(t)
	connectionB := fixture.connectionID(t)

	if _, err := fixture.usage.UpsertPriceVersion(ctx, PriceVersionInput{
		PriceVersion: "pv-a", ConnectionID: connectionA, ModelName: "shared-model",
		InputPerMillion: 100, OutputPerMillion: 100,
	}); err != nil {
		t.Fatalf("UpsertPriceVersion A: %v", err)
	}
	if _, err := fixture.usage.UpsertPriceVersion(ctx, PriceVersionInput{
		PriceVersion: "pv-b", ConnectionID: connectionB, ModelName: "shared-model",
		InputPerMillion: 999, OutputPerMillion: 999,
	}); err != nil {
		t.Fatalf("UpsertPriceVersion B: %v", err)
	}

	priceA, found, err := fixture.usage.ResolvePriceVersion(ctx, connectionA, "shared-model", time.Now())
	if err != nil || !found {
		t.Fatalf("连接 A 的价格必须能解析：found=%v err=%v", found, err)
	}
	if priceA.ConnectionID != connectionA || priceA.InputPerMillion != 100 {
		t.Fatalf("连接 A 必须解析到自己的价格，实际 %+v", priceA)
	}
	priceB, found, err := fixture.usage.ResolvePriceVersion(ctx, connectionB, "shared-model", time.Now())
	if err != nil || !found {
		t.Fatalf("连接 B 的价格必须能解析：found=%v err=%v", found, err)
	}
	if priceB.InputPerMillion != 999 {
		t.Fatalf("连接 B 必须解析到自己的价格，实际 %d", priceB.InputPerMillion)
	}

	// 没有价格的连接：必须是「没找到」，绝不是「回退到别人的价格」。
	unpriced := fixture.connectionID(t)
	if _, found, err := fixture.usage.ResolvePriceVersion(ctx, unpriced, "shared-model", time.Now()); err != nil || found {
		t.Fatalf("没有配置价格的连接必须返回 found=false（不得回退到别的连接），实际 found=%v err=%v", found, err)
	}
}

// TestCapabilitiesDeclarationRoundTrip 覆盖能力声明的读写与「未声明」语义。
func TestCapabilitiesDeclarationRoundTrip(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()
	connectionID := fixture.connectionID(t)

	if _, found, err := fixture.usage.ResolveCapabilities(ctx, connectionID, "unregistered"); err != nil || found {
		t.Fatalf("未声明的模型必须返回 found=false，实际 found=%v err=%v", found, err)
	}

	declared, err := fixture.usage.UpsertCapabilities(ctx, CapabilityInput{
		ConnectionID: connectionID, ModelName: "reasoner-x",
		SupportsTemperature: false, SupportsReasoningEffort: true,
		SupportsStructuredOutput: true, MaxOutputTokens: 32000,
	})
	if err != nil {
		t.Fatalf("UpsertCapabilities: %v", err)
	}
	if declared.Source != model.CapabilitySourceDeclaration {
		t.Fatalf("声明的来源必须是 declaration，实际 %s", declared.Source)
	}

	loaded, found, err := fixture.usage.ResolveCapabilities(ctx, connectionID, "reasoner-x")
	if err != nil || !found {
		t.Fatalf("ResolveCapabilities：found=%v err=%v", found, err)
	}
	if loaded.SupportsTemperature || loaded.MaxOutputTokens != 32000 {
		t.Fatalf("能力声明必须原样读回，实际 %+v", loaded)
	}

	// 声明必须能真正拦住不支持的参数（否则声明只是装饰）。
	errs := loaded.ValidateAgainstCapabilities(model.ModelRequestSpec{
		ModelName: "reasoner-x", Temperature: floatPointer(0.7),
	})
	if len(errs) == 0 {
		t.Fatal("不支持 temperature 的模型必须拒绝 temperature")
	}
}

// TestPriceVersionRejectsZeroPricesUnlessFree 覆盖「宁可报错也不静默存 0」。
//
// 这是 T07「不得伪造精确成本」在录入侧的对应要求：一行全 0 的价格会让
// 这个连接的所有调用都被算成 0 元，而实际账单在涨。
func TestPriceVersionRejectsZeroPricesUnlessFree(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()
	connectionID := fixture.connectionID(t)

	if _, err := fixture.usage.UpsertPriceVersion(ctx, PriceVersionInput{
		PriceVersion: "pv-zero", ConnectionID: connectionID, ModelName: "m",
	}); !IsStoreValidationError(err) {
		t.Fatalf("全 0 单价必须被拒绝（确实免费要显式声明），实际 err=%v", err)
	}

	free, err := fixture.usage.UpsertPriceVersion(ctx, PriceVersionInput{
		PriceVersion: "pv-free", ConnectionID: connectionID, ModelName: "m", IsFree: true,
	})
	if err != nil {
		t.Fatalf("显式免费的接入点应当被接受：%v", err)
	}
	if !free.IsFree || free.IsZero() {
		t.Fatalf("免费接入点必须可识别且不算「未配置」，实际 %+v", free)
	}

	// 免费接入点的费用是**精确的 0**，而预留是 0（不需要额度）。
	charge := model.ComputeCharge(model.TokenUsage{Source: model.UsageSourceUnknown}, free, free.PriceVersion)
	if charge.AmountState != model.AmountStateActual || charge.AmountMinor == nil || *charge.AmountMinor != 0 {
		t.Fatalf("免费接入点的费用是精确 0，实际 state=%s amount=%v", charge.AmountState, charge.AmountMinor)
	}
	quote := model.QuoteReservation(0, 4096, free)
	if !quote.Ok || quote.AmountMinor != 0 {
		t.Fatalf("免费接入点不需要预留额度，实际 %+v", quote)
	}
}

// TestBatchBudgetIsEnforcedIndependently 覆盖 §6.1「批次自带 budget」。
//
// 批次上限必须**独立**成立：项目还有额度不代表这一批可以再花。
// 否则「本批最多花 2000」会变成一句空话。
func TestBatchBudgetIsEnforcedIndependently(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	// 给批次设 500 分的上限（项目不设限）。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE batches SET budget_limit_minor = 500 WHERE id = $1`, fixture.batchID); err != nil {
		t.Fatalf("set batch budget: %v", err)
	}

	if _, created, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("batch-1", 300, 0)); err != nil || !created {
		t.Fatalf("批次内首次预留：created=%v err=%v", created, err)
	}
	if _, created, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("batch-2", 200, 0)); err != nil || !created {
		t.Fatalf("恰好用满批次上限必须通过：created=%v err=%v", created, err)
	}
	if _, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("batch-3", 1, 0)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("超出批次上限必须被拒，实际 %v", err)
	}

	// 项目台账不受批次上限影响（它记的是真实占用，不是拦截阈值）。
	snapshot, err := fixture.usage.ProjectBudget(ctx, fixture.projectID, "CNY")
	if err != nil {
		t.Fatalf("ProjectBudget: %v", err)
	}
	if snapshot.ReservedMinor != 500 {
		t.Fatalf("项目台账应记 500（无上限也不拦截），实际 %d", snapshot.ReservedMinor)
	}

	reloaded, err := fixture.batches.GetBatch(ctx, fixture.batchID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if reloaded.BudgetReservedMinor != 500 {
		t.Fatalf("批次计数器应为 500，实际 %d", reloaded.BudgetReservedMinor)
	}

	// 项目上限更严格时也必须生效（两层都要拦）。
	if _, _, err := fixture.usage.ReserveUsage(ctx, ReserveUsageInput{
		ProjectID:         fixture.projectID,
		Purpose:           "generation",
		IdempotencyKey:    "batch-4",
		ModelName:         "test-model",
		Currency:          "CNY",
		ReservationMinor:  100,
		ProjectLimitMinor: 100, // 项目只剩 100，但这一批已经用满
		BatchID:           &fixture.batchID,
	}); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("批次已用满时，即使项目还有额度也必须被拒，实际 %v", err)
	}
}

// TestReservationWithMismatchedBatchCurrencyIsConfigError 覆盖
// 「配置错误不能被伪装成额度不足」。
//
// 币种不一致时若返回 ErrBudgetExhausted，用户会去加预算；而真正的问题是
// 拿错了配置 —— 那会让他反复加预算都不管用。
func TestReservationWithMismatchedBatchCurrencyIsConfigError(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	if _, err := fixture.pool.Exec(ctx, `
    UPDATE batches SET budget_currency = 'USD' WHERE id = $1`, fixture.batchID); err != nil {
		t.Fatalf("switch batch currency: %v", err)
	}

	_, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("currency-1", 100, 0))
	if err == nil || errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("币种不一致必须是配置错误而不是额度不足，实际 %v", err)
	}
	if !IsStoreValidationError(err) {
		t.Fatalf("币种不一致必须可展示给用户（字段级错误），实际 %v", err)
	}
}

// TestUsageLedgerColumnListMatchesScanTargets 兜住列清单与扫描目标的漂移。
//
// usageLedgerColumns 被插值进语句，而扫描目标列在 usageScanTargets 里。
// 两者长度不一致时，Go 的 Scan 会报错（列数与目标不匹配），
// 但那时已经是在运行时；这里在测试期就固定住。
func TestUsageLedgerColumnListMatchesScanTargets(t *testing.T) {
	columns := selectColumnsOf(t, usageLedgerColumns)
	if got := UsageLedgerColumnCount(); len(columns) != got {
		t.Fatalf("列清单 %d 列，扫描目标 %d 个：两者必须一一对应", len(columns), got)
	}
	if len(columns) < 25 {
		t.Fatalf("列清单异常偏少（%d），可能被改坏", len(columns))
	}
}

// TestListOpenUsageFindsUnsettled 覆盖 T33 需要的「未结算账目」巡检。
func TestListOpenUsageFindsUnsettled(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	open, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("open-1", 100, 0))
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}
	settled, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("open-2", 100, 0))
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}
	amount := int64(50)
	if _, err := fixture.usage.SettleUsage(ctx, settled.ID, SettleUsageInput{
		Charge: model.UsageCharge{
			AmountMinor: &amount, AmountState: model.AmountStateActual,
			Usage: model.TokenUsage{Source: model.UsageSourceProvider},
		},
	}); err != nil {
		t.Fatalf("SettleUsage: %v", err)
	}

	items, err := fixture.usage.ListOpenUsage(ctx, time.Nanosecond, 100)
	if err != nil {
		t.Fatalf("ListOpenUsage: %v", err)
	}
	foundOpen := false
	for _, item := range items {
		if item.ID == open.ID {
			foundOpen = true
		}
		if item.ID == settled.ID {
			t.Fatal("已结算的账目不得出现在未结算清单里")
		}
		if item.ActualMinor != nil {
			t.Fatalf("未结算账目不应有实际金额，实际 %d", *item.ActualMinor)
		}
	}
	if !foundOpen {
		t.Fatal("未结算的账目必须能被巡检到（否则永远不会被结算/核对）")
	}
}

// TestMarkReconciliationStates 覆盖「核对供应商账单的状态」。
func TestMarkReconciliationStates(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	reservation, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("recon-1", 100, 0))
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}
	if reservation.Reconciliation != model.ReconciliationNotAttempted {
		t.Fatalf("新账目默认尚未核对，实际 %s", reservation.Reconciliation)
	}

	// mismatch 必须带说明：没有说明的「不一致」无法被任何人处理。
	if _, err := fixture.usage.MarkReconciliation(ctx, ReconciliationInput{
		UsageID: reservation.ID, Status: model.ReconciliationMismatch,
	}); !IsStoreValidationError(err) {
		t.Fatalf("mismatch 缺说明必须被拒，实际 %v", err)
	}

	updated, err := fixture.usage.MarkReconciliation(ctx, ReconciliationInput{
		UsageID: reservation.ID, Status: model.ReconciliationMismatch, Note: "账单多算 3 分",
	})
	if err != nil {
		t.Fatalf("MarkReconciliation: %v", err)
	}
	if updated.Reconciliation != model.ReconciliationMismatch || updated.ReconciliationNote != "账单多算 3 分" {
		t.Fatalf("核对结果必须留存，实际 %+v", updated)
	}

	// 允许更正（账单可能后补），但不得回到 not_attempted（那会抹掉审计事实）。
	corrected, err := fixture.usage.MarkReconciliation(ctx, ReconciliationInput{
		UsageID: reservation.ID, Status: model.ReconciliationConfirmed, Note: "供应商已更正",
	})
	if err != nil {
		t.Fatalf("更正确认：%v", err)
	}
	if corrected.Reconciliation != model.ReconciliationConfirmed {
		t.Fatalf("必须允许更正为 confirmed，实际 %s", corrected.Reconciliation)
	}
	if _, err := fixture.usage.MarkReconciliation(ctx, ReconciliationInput{
		UsageID: reservation.ID, Status: model.ReconciliationNotAttempted,
	}); !IsStoreValidationError(err) {
		t.Fatalf("不得把核对状态改回 not_attempted，实际 %v", err)
	}

	if _, err := fixture.usage.MarkReconciliation(ctx, ReconciliationInput{
		UsageID: 1 << 62, Status: model.ReconciliationConfirmed,
	}); !errors.Is(err, ErrUsageNotFound) {
		t.Fatalf("不存在的账目必须返回 ErrUsageNotFound，实际 %v", err)
	}
}

// TestSettleUnknownKeepsReservationOnReserveFailurePath 覆盖
// 「结算态与金额态互斥校验」：不能声明 precision 却不给金额。
func TestSettleRejectsInconsistentAmountState(t *testing.T) {
	fixture := newUsageFixture(t)
	ctx := context.Background()

	reservation, _, err := fixture.usage.ReserveUsage(ctx, fixture.reserve("inconsistent-1", 100, 0))
	if err != nil {
		t.Fatalf("ReserveUsage: %v", err)
	}
	// 声称是 actual 却不给金额：这是矛盾输入。若接受它写入 0，
	// 下游会把「0 元」当成真实成本 —— 正是要禁止的「伪造精确成本」。
	if _, err := fixture.usage.SettleUsage(ctx, reservation.ID, SettleUsageInput{
		Charge: model.UsageCharge{AmountMinor: nil, AmountState: model.AmountStateActual},
	}); !IsStoreValidationError(err) {
		t.Fatalf("actual 但没有金额必须被拒，实际 %v", err)
	}
	// 被拒后账目必须仍是未结算（不能留下半结算状态）。
	reloaded, err := fixture.usage.GetUsage(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if reloaded.State != model.UsageStateReserved {
		t.Fatalf("被拒的结算不得改变账目状态，实际 %s", reloaded.State)
	}
}

// connectionID 建一个新的模型连接（model_providers 行）并返回 ID。
//
// 为什么不复用某个全局连接：价格/能力都按连接隔离，复用会让
// 「按连接严格匹配」的断言失去意义（多个测试共用一个 ID 时，
// 回退到别的连接与命中自己的连接无法区分）。
func (fixture usageFixture) connectionID(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	name := fmt.Sprintf("price-conn-%d-%s", os.Getpid(), t.Name())
	if err := fixture.pool.QueryRow(ctx, `
    INSERT INTO model_providers (name, base_url, model, api_key_masked)
    VALUES ($1, 'https://api.example.test/v1', 'test-model', '')
    ON CONFLICT DO NOTHING
    RETURNING id`, name).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := fixture.pool.QueryRow(ctx, `
        SELECT id FROM model_providers WHERE name = $1`, name).Scan(&id); err != nil {
				t.Fatalf("resolve connection: %v", err)
			}
			return id
		}
		// model_providers.name 可能没有唯一约束，此时上面的 RETURNING 会正常返回。
		t.Fatalf("create connection: %v", err)
	}
	t.Cleanup(func() {
		// 价格与能力引用连接（ON DELETE RESTRICT），因此先删引用再删连接。
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM model_price_versions WHERE provider_connection_id = $1`, id)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM model_capabilities WHERE provider_connection_id = $1`, id)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM model_providers WHERE id = $1`, id)
	})
	return id
}

func int64Pointer(value int64) *int64     { return &value }
func floatPointer(value float64) *float64 { return &value }
