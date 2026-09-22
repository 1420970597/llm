package studio

import (
	"context"
	"fmt"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 的**可观测性读模型**（Issue #160 T33）。
//
// 契约：#160 T33 的原文要求观测「排队年龄、租约回收、未派发 outbox、
// 模型错误/成本未知、预算预留、评估缺分、发布失败与迁移差异，日志关联
// project/batch/experiment/release/request ID」。
//
// 为什么做成**一个读模型**而不是一堆散落的指标查询：
// 这些数字只有在**互相印证**时才有诊断价值。例如「发布失败 3 个」本身
// 没有信息量，而「发布失败 3 个 + 预算 uncertain 很高 + 队列年龄 0」
// 指向「模型调用在烧钱但不产出」；「发布失败 3 个 + 队列年龄 40 分钟 + 租约回收 12 次」
// 指向「worker 在反复崩溃」。把查询散开就看不到这种组合。
//
// 所有计数都来自**既有表**（jobs/outbox/job_attempts/budget_reservations/
// usage_ledger/experiments/releases），不新增表、不新增埋点写入路径：
// 埋点写失败会污染业务事务，而「读时聚合」最多是慢一点。
//
// 一个刻意的取舍：**不把未知成本算成 0**。`usage_ledger` 里
// amount_state='unknown' 的行单独计数，并在 Notes 里说明「未知不等于 0」
//（与 §2.4 的费用四态一致）。把未知算成 0 会让运维看到一个「成本正常」
// 的假象，而那正是超时/断连场景下的默认形态。

// StudioHealth 是运维诊断快照。
type StudioHealth struct {
	GeneratedAt time.Time    `json:"generatedAt"`
	Rollout     RolloutState `json:"rollout"`

	Outbox StudioOutboxHealth `json:"outbox"`
	Jobs   StudioJobHealth    `json:"jobs"`
	Leases StudioLeaseHealth  `json:"leases"`
	Budget StudioBudgetHealth `json:"budget"`
	Usage  StudioUsageHealth  `json:"usage"`

	Experiments StudioExperimentHealth `json:"experiments"`
	Releases    StudioReleaseHealth    `json:"releases"`

	Notes []string `json:"notes"`
}

// StudioOutboxHealth 是派发侧的健康度。
type StudioOutboxHealth struct {
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
	// OldestPendingAgeSeconds 是最老的未派发事件的年龄。
	// 它是「worker 派发循环是否在工作」的唯一直接信号：
	// pending 数字可以在派发停滞时保持不变（没有新事件），但年龄会一直涨。
	OldestPendingAgeSeconds int64 `json:"oldestPendingAgeSeconds"`
}

// StudioJobHealth 是作业侧的健康度。
type StudioJobHealth struct {
	Pending int `json:"pending"`
	Leased  int `json:"leased"`
	Running int `json:"running"`
	// Failed 仍可人工重试；Dead 表示自动路径已放弃（需要人工介入）。
	Failed int `json:"failed"`
	Dead   int `json:"dead"`
	// OldestPendingAgeSeconds 是排队年龄（最老的未开始作业）。
	OldestPendingAgeSeconds int64 `json:"oldestPendingAgeSeconds"`
}

// StudioLeaseHealth 是租约回收情况。
type StudioLeaseHealth struct {
	// Expired24h 是过去 24 小时内因租约过期被回收的尝试次数。
	// 持续大于 0 说明有 worker 在心跳丢失/崩溃 —— 那是「作业被重复执行」的前兆。
	Expired24h int `json:"expired24h"`
	// Fenced24h 是提交被 fencing 拒绝的次数（同一作业被两个 worker 执行过）。
	Fenced24h int `json:"fenced24h"`
}

// StudioBudgetHealth 是按币种的预算台账。
type StudioBudgetHealth struct {
	Currencies []StudioBudgetCurrency `json:"currencies"`
}

// StudioBudgetCurrency 是单个币种的预算数字（整数最小货币单位）。
type StudioBudgetCurrency struct {
	Currency       string `json:"currency"`
	LimitMinor     int64  `json:"limitMinor"`
	ReservedMinor  int64  `json:"reservedMinor"`
	SettledMinor   int64  `json:"settledMinor"`
	UncertainMinor int64  `json:"uncertainMinor"`
}

// StudioUsageHealth 是用量与对账健康度。
type StudioUsageHealth struct {
	// UnknownAmount24h 是 24 小时内「可能已收费但金额未知」的请求数。
	UnknownAmount24h int `json:"unknownAmount24h"`
	// ReconciliationAttention 是尚未确认对账的请求数（not_attempted/pending/mismatch/impossible）。
	ReconciliationAttention int `json:"reconciliationAttention"`
	// ReconciliationImpossible 是永久无法对账的请求数（供应商不给明细）。
	ReconciliationImpossible int `json:"reconciliationImpossible"`
}

// StudioExperimentHealth 是质量实验健康度。
type StudioExperimentHealth struct {
	PartialFailed int `json:"partialFailed"`
	// WithMissingScores 是有缺分的实验数（缺分 != 0 分，必须单独看）。
	WithMissingScores int `json:"withMissingScores"`
}

// StudioReleaseHealth 是发布健康度。
type StudioReleaseHealth struct {
	Building    int `json:"building"`
	BuildFailed int `json:"buildFailed"`
	Published   int `json:"published"`
}

// LoadStudioHealth 读取运维诊断快照。
func LoadStudioHealth(ctx context.Context, pool *pgxpool.Pool, rollout Rollout) (StudioHealth, error) {
	health := StudioHealth{
		GeneratedAt: time.Now().UTC(),
		Rollout:     rollout.State(),
		Notes:       []string{},
	}
	queries := []struct {
		target *int
		sql    string
	}{
		{&health.Outbox.Pending, `SELECT COUNT(*) FROM outbox WHERE status = 'pending'`},
		{&health.Outbox.Failed, `SELECT COUNT(*) FROM outbox WHERE status = 'failed'`},
		{&health.Jobs.Pending, `SELECT COUNT(*) FROM jobs WHERE status = 'pending'`},
		{&health.Jobs.Leased, `SELECT COUNT(*) FROM jobs WHERE status = 'leased'`},
		{&health.Jobs.Running, `SELECT COUNT(*) FROM jobs WHERE status = 'running'`},
		{&health.Jobs.Failed, `SELECT COUNT(*) FROM jobs WHERE status = 'failed'`},
		{&health.Jobs.Dead, `SELECT COUNT(*) FROM jobs WHERE status = 'dead'`},
		{&health.Leases.Expired24h, `SELECT COUNT(*) FROM job_attempts WHERE outcome = 'lease_expired' AND started_at > NOW() - INTERVAL '24 hours'`},
		{&health.Leases.Fenced24h, `SELECT COUNT(*) FROM job_attempts WHERE outcome = 'fenced' AND started_at > NOW() - INTERVAL '24 hours'`},
		{&health.Usage.UnknownAmount24h, `SELECT COUNT(*) FROM usage_ledger WHERE amount_state = '` + model.AmountStateUnknown + `' AND created_at > NOW() - INTERVAL '24 hours'`},
		{&health.Usage.ReconciliationAttention, `SELECT COUNT(*) FROM usage_ledger WHERE reconciliation IN ('not_attempted', 'pending', 'mismatch', 'impossible')`},
		{&health.Usage.ReconciliationImpossible, `SELECT COUNT(*) FROM usage_ledger WHERE reconciliation = 'impossible'`},
		{&health.Experiments.PartialFailed, `SELECT COUNT(*) FROM experiments WHERE status = 'partial_failed'`},
		{&health.Experiments.WithMissingScores, `SELECT COUNT(*) FROM experiments WHERE missing_count > 0`},
		{&health.Releases.Building, `SELECT COUNT(*) FROM releases WHERE status = 'building'`},
		{&health.Releases.BuildFailed, `SELECT COUNT(*) FROM releases WHERE status = 'build_failed'`},
		{&health.Releases.Published, `SELECT COUNT(*) FROM releases WHERE status = 'published'`},
	}
	for _, query := range queries {
		if err := pool.QueryRow(ctx, query.sql).Scan(query.target); err != nil {
			return StudioHealth{}, fmt.Errorf("读取运维指标失败：%w", err)
		}
	}

	// 队列年龄与 outbox 年龄分开取：它们是两个不同的循环（派发/执行）是否停滞的证据。
	if err := pool.QueryRow(ctx, `
    SELECT COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::bigint, 0)
    FROM jobs WHERE status = 'pending'`).Scan(&health.Jobs.OldestPendingAgeSeconds); err != nil {
		return StudioHealth{}, fmt.Errorf("读取排队年龄失败：%w", err)
	}
	if err := pool.QueryRow(ctx, `
    SELECT COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::bigint, 0)
    FROM outbox WHERE status = 'pending'`).Scan(&health.Outbox.OldestPendingAgeSeconds); err != nil {
		return StudioHealth{}, fmt.Errorf("读取 outbox 年龄失败：%w", err)
	}

	rows, err := pool.Query(ctx, `
    SELECT currency, COALESCE(SUM(limit_minor), 0), COALESCE(SUM(reserved_minor), 0),
           COALESCE(SUM(settled_minor), 0), COALESCE(SUM(uncertain_minor), 0)
    FROM budget_reservations GROUP BY currency ORDER BY currency`)
	if err != nil {
		return StudioHealth{}, fmt.Errorf("读取预算台账失败：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry StudioBudgetCurrency
		if err := rows.Scan(&entry.Currency, &entry.LimitMinor, &entry.ReservedMinor,
			&entry.SettledMinor, &entry.UncertainMinor); err != nil {
			return StudioHealth{}, err
		}
		health.Budget.Currencies = append(health.Budget.Currencies, entry)
	}
	if err := rows.Err(); err != nil {
		return StudioHealth{}, err
	}
	if health.Budget.Currencies == nil {
		health.Budget.Currencies = []StudioBudgetCurrency{}
	}

	buildHealthNotes(&health)
	return health, nil
}

// buildHealthNotes 把「怎么读这些数字」写进快照。
//
// 为什么把解读放进响应而不是只放在文档里：值班的人看的是这个接口，
// 而不是 runbook 的第 3 节。数字没有解读就等于没有信息。
func buildHealthNotes(health *StudioHealth) {
	if health.Outbox.Pending > 0 && health.Outbox.OldestPendingAgeSeconds > 600 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"未派发 outbox 有 %d 条且最老的已等待 %d 秒：派发循环可能停滞（检查 worker 的 dispatch 与 Redis 连通性）",
			health.Outbox.Pending, health.Outbox.OldestPendingAgeSeconds))
	}
	if health.Jobs.Pending > 0 && health.Jobs.OldestPendingAgeSeconds > 3600 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"有 %d 个作业排队超过 %d 秒：worker 可能不足或被暂停（检查 STUDIO_ENABLED 与 worker 健康）",
			health.Jobs.Pending, health.Jobs.OldestPendingAgeSeconds))
	}
	if health.Leases.Expired24h > 0 || health.Leases.Fenced24h > 0 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"过去 24 小时租约过期 %d 次、提交被 fencing 拒绝 %d 次：有 worker 心跳丢失或崩溃，"+
				"同一作业可能被执行过两次（费用与产出都可能重复）",
			health.Leases.Expired24h, health.Leases.Fenced24h))
	}
	if health.Usage.UnknownAmount24h > 0 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"过去 24 小时有 %d 次请求的金额**未知**（超时/断连或供应商未回传用量）："+
				"未知不等于 0，它按当时的预留金额占用额度",
			health.Usage.UnknownAmount24h))
	}
	if health.Usage.ReconciliationImpossible > 0 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"%d 条用量记录**无法与供应商账单核对**（供应商不提供明细或已无请求 ID）：这些成本不会有精确值",
			health.Usage.ReconciliationImpossible))
	}
	if health.Experiments.WithMissingScores > 0 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"%d 个实验存在缺分：缺分**不是 0 分**，报告的均值只覆盖已评分的格，结论必须带覆盖范围",
			health.Experiments.WithMissingScores))
	}
	if health.Releases.BuildFailed > 0 {
		health.Notes = append(health.Notes, fmt.Sprintf(
			"%d 个发布处于 build_failed：制品未确认前不会进入 published，已发布文件仍可下载",
			health.Releases.BuildFailed))
	}
	if len(health.Notes) == 0 {
		health.Notes = append(health.Notes, "未发现需要关注的信号（阈值：outbox 等待 > 600 秒、排队 > 3600 秒、任何租约回收）")
	}
}

// ---------------------------------------------------------------------------
// 日志关联
// ---------------------------------------------------------------------------

// LogContext 是写日志时应当带的关联字段（T33「日志关联 project/batch/
// experiment/release/request ID」）。
//
// 为什么做成一个小类型而不是到处拼字符串：字段名一旦拼错，
// 日志就不再可关联，而那种偏差不会报错。集中一处 + 测试断言字段名。
type LogContext struct {
	RequestID    string
	ProjectID    int64
	BatchID      int64
	ExperimentID int64
	ReleaseID    int64
}

// Fields 返回可 JSON 序列化的关联字段（空值不出现在输出里，
// 避免每条日志带一串 0 让真正有值的字段被淹没）。
func (logContext LogContext) Fields() map[string]any {
	fields := map[string]any{}
	if logContext.RequestID != "" {
		fields["requestId"] = logContext.RequestID
	}
	if logContext.ProjectID > 0 {
		fields["projectId"] = logContext.ProjectID
	}
	if logContext.BatchID > 0 {
		fields["batchId"] = logContext.BatchID
	}
	if logContext.ExperimentID > 0 {
		fields["experimentId"] = logContext.ExperimentID
	}
	if logContext.ReleaseID > 0 {
		fields["releaseId"] = logContext.ReleaseID
	}
	return fields
}
