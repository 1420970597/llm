package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 可靠作业派发（Issue #160 T06）。
//
// 契约：docs/plans/atelier-implementation.md §4.1；#160 §1 的 worker 行。
//
// 四条必须成立的性质（对应 T06 的四个验收故障场景）：
//  1. **API 落库后派发前崩溃** → 作业仍在，outbox 仍是 pending，
//     dispatcher 下一轮会派发。不丢任务。
//  2. **BRPOP 后进程退出** → 租约过期后被回收，别的 worker 重新执行。
//  3. **重复消息** → 抢占是 CAS 的，第二次抢占拿不到租约；
//     终态提交是条件更新，重放不改变结果。
//  4. **租约过期后旧 worker 返回** → fencing token 不匹配，提交被拒绝。
//     这是最容易漏的一条：只有 lease_until 检查无法阻止
//     「持有者以为租约还有效」的窗口。
//
// 另外：**不承诺外部 LLM 恰好调用一次或只计费一次**（T06 原文）。
// 本层保证的是「本地成果不重复」，而外部副作用由 T07 的用量账本
// 与幂等键各自处理。任何声称「恰好一次」的实现都是在对供应商撒谎。

// 作业相关的哨兵错误。分别对应不同的用户可操作路径，
// 因此不能压成一个「作业失败」。
var (
	// ErrJobNotClaimable 表示作业当前不可抢占（已被别人持有、已终态、或未到退避时间）。
	ErrJobNotClaimable = errors.New("该作业当前不可执行")
	// ErrJobLeaseLost 表示租约已失效（被回收或被别人抢走）。
	// 提交时返回它，worker 必须**丢弃**自己的结果而不是重试提交。
	ErrJobLeaseLost = errors.New("作业租约已失效，本次执行结果被丢弃")
	// ErrJobNotFound 表示作业不存在。
	ErrJobNotFound = errors.New("未找到该作业")
)

// fencingMismatchTolerance 是允许的 token 落后量。刻意是 0。
//
// 为什么不给「容忍窗口」：容忍窗口的存在意味着「旧 worker 的提交仍可能成功」，
// 而那正是 fencing 要消除的场景。宁可让一个仍在运行的旧 worker 白做一次工作
// （它的结果被丢弃，但它在数据库里留下了 fenced 记录可供取证），
// 也不能让它把过期结果写进权威状态。
const fencingMismatchTolerance = 0

type JobStore struct {
	db *pgxpool.Pool
}

func NewJobStore(db *pgxpool.Pool) *JobStore {
	return &JobStore{db: db}
}

// ---------------------------------------------------------------------------
// 入队（业务事务内调用）
// ---------------------------------------------------------------------------

// EnqueueJobInput 是创建一个作业的请求。
//
// IdempotencyKey 为空表示「不做幂等去重」（内部作业）。
// 一旦非空，同 (kind, key) 只会有一个作业 —— 这是「同键同请求返回原结果」的
// 数据层保证（契约 §1.3），不依赖短 TTL。
type EnqueueJobInput struct {
	ProjectID      *int64
	BatchID        *int64
	Kind           string
	Payload        any
	IdempotencyKey string
	MaxAttempts    int
	CreatedBy      *int64
}

// EnqueueJobTx 在**给定事务内**创建作业与 outbox 事件。
//
// 为什么必须由调用方传事务（而不是自己开）：业务事务与作业创建必须原子。
// 分两次写会留下两个坏状态：
//   - 业务写了但作业没写 → 用户看到「已提交」但永远不会被处理（旧实现的形态）；
//   - 作业写了但业务没写 → worker 处理一个不存在的业务意图。
//
// 返回值 (job, created, err)：created=false 表示幂等命中，返回既有作业。
// 调用方必须据此**跳过**重复的副作用（例如不再插入 batch_items）。
func EnqueueJobTx(ctx context.Context, tx pgx.Tx, input EnqueueJobInput) (model.Job, bool, error) {
	if strings.TrimSpace(input.Kind) == "" {
		return model.Job{}, false, &apiStoreError{Message: "作业类型必填"}
	}
	maxAttempts := input.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = model.DefaultMaxAttempts
	}

	payload := json.RawMessage(`{}`)
	if input.Payload != nil {
		raw, err := json.Marshal(input.Payload)
		if err != nil {
			return model.Job{}, false, err
		}
		payload = raw
	}

	var job model.Job
	var leaseUntil *time.Time
	err := tx.QueryRow(ctx, `
    INSERT INTO jobs (project_id, batch_id, job_kind, payload, status, max_attempts,
                      idempotency_key, created_by)
    VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7)
    ON CONFLICT (job_kind, idempotency_key) WHERE idempotency_key <> '' DO NOTHING
    RETURNING id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
              next_run_at, lease_owner, lease_until, fencing_token,
              error_class, error_message, retryable, idempotency_key,
              created_by, started_at, finished_at, created_at, updated_at`,
		input.ProjectID, input.BatchID, input.Kind, payload, maxAttempts,
		input.IdempotencyKey, input.CreatedBy,
	).Scan(&job.ID, &job.ProjectID, &job.BatchID, &job.Kind, &job.Payload, &job.Status,
		&job.Attempt, &job.MaxAttempts, &job.NextRunAt, &job.LeaseOwner, &leaseUntil,
		&job.FencingToken, &job.ErrorClass, &job.ErrorMessage, &job.Retryable,
		&job.IdempotencyKey, &job.CreatedBy, &job.StartedAt, &job.FinishedAt,
		&job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 幂等命中：取回既有作业。调用方据此跳过重复副作用。
		existing, getErr := getJobByIdempotencyTx(ctx, tx, input.Kind, input.IdempotencyKey)
		if getErr != nil {
			return model.Job{}, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return model.Job{}, false, err
	}
	job.LeaseUntil = leaseUntil

	// outbox 事件与作业同事务。event_id 用作业 ID 保证幂等：
	// 即使调用方重试整个事务，也不会产生两条派发意图。
	if err := appendOutboxTx(ctx, tx, outboxEventIDForJob(job.ID), "studio.jobs", map[string]any{
		"jobId": job.ID,
		"kind":  job.Kind,
	}); err != nil {
		return model.Job{}, false, err
	}

	return job, true, nil
}

// outboxEventIDForJob 生成作业派发事件的幂等 ID。
func outboxEventIDForJob(jobID int64) string {
	return "job:" + strconv.FormatInt(jobID, 10)
}

// appendOutboxTx 写入一条 outbox 事件（幂等：同 event_id 只保留一条）。
func appendOutboxTx(ctx context.Context, tx pgx.Tx, eventID, topic string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
    INSERT INTO outbox (event_id, topic, payload, status)
    VALUES ($1, $2, $3, 'pending')
    ON CONFLICT (event_id) DO NOTHING`, eventID, topic, raw)
	return err
}

// EnqueueJob 独立事务版本（供没有自己事务的调用方使用）。
func (s *JobStore) EnqueueJob(ctx context.Context, input EnqueueJobInput) (model.Job, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Job{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	job, created, err := EnqueueJobTx(ctx, tx, input)
	if err != nil {
		return model.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Job{}, false, err
	}
	return job, created, nil
}

// getJobByIdempotencyTx 按幂等键取回作业。
func getJobByIdempotencyTx(ctx context.Context, tx pgx.Tx, kind, key string) (model.Job, error) {
	row := tx.QueryRow(ctx, `
    SELECT
      id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
      next_run_at, lease_owner, lease_until, fencing_token,
      error_class, error_message, retryable, idempotency_key,
      created_by, started_at, finished_at, created_at, updated_at
    FROM jobs WHERE job_kind = $1 AND idempotency_key = $2`, kind, key)
	return scanJob(row)
}

// ---------------------------------------------------------------------------
// 抢占（CAS + 租约 + fencing）
// ---------------------------------------------------------------------------

// ClaimJob 以 CAS 方式抢占一个待执行作业。
//
// 返回 (job, true, nil) 表示抢占成功，调用方获得租约；
// 返回 (zero, false, nil) 表示没有可执行作业（正常情况，不是错误）。
//
// 实现要点（这是 T06 的技术核心）：
//  1. **单语句条件更新**：`WHERE status='pending' AND next_run_at <= now()`
//     由数据库保证「恰有一个 worker 成功」。用 SELECT-then-UPDATE 会让
//     两个 worker 同时通过检查，于是同一作业被执行两次。
//  2. **fencing_token 递增**：每次抢占 +1，形成全序。持有旧 token 的 worker
//     提交时会被拒绝（见 CompleteJob）。这是「过期 worker 迟到提交」的正解。
//  3. **attempt 递增在抢占时**（不是完成时）：抢占就等于「开始了一次尝试」。
//     若在完成时递增，一个崩溃的 worker 的尝试不会被计数，于是
//     max_attempts 永远无法耗尽，作业会无限重试。
func (s *JobStore) ClaimJob(ctx context.Context, kind, owner string, leaseDuration time.Duration) (model.Job, bool, error) {
	if strings.TrimSpace(owner) == "" {
		return model.Job{}, false, &apiStoreError{Message: "租约持有者标识必填"}
	}
	if leaseDuration <= 0 {
		leaseDuration = model.DefaultLeaseDuration
	}

	var job model.Job
	var leaseUntil *time.Time
	err := s.db.QueryRow(ctx, `
    WITH candidate AS (
      SELECT id FROM jobs
      WHERE status = 'pending' AND job_kind = $1 AND next_run_at <= NOW()
      ORDER BY next_run_at, id
      FOR UPDATE SKIP LOCKED
      LIMIT 1
    )
    UPDATE jobs
    SET status = 'leased',
        attempt = attempt + 1,
        lease_owner = $2,
        lease_until = NOW() + $3::interval,
        fencing_token = fencing_token + 1,
        started_at = COALESCE(started_at, NOW()),
        updated_at = NOW()
    WHERE id = (SELECT id FROM candidate)
    RETURNING
      id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
      next_run_at, lease_owner, lease_until, fencing_token,
      error_class, error_message, retryable, idempotency_key,
      created_by, started_at, finished_at, created_at, updated_at`,
		kind, owner, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds())),
	).Scan(&job.ID, &job.ProjectID, &job.BatchID, &job.Kind, &job.Payload, &job.Status,
		&job.Attempt, &job.MaxAttempts, &job.NextRunAt, &job.LeaseOwner, &leaseUntil,
		&job.FencingToken, &job.ErrorClass, &job.ErrorMessage, &job.Retryable,
		&job.IdempotencyKey, &job.CreatedBy, &job.StartedAt, &job.FinishedAt,
		&job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Job{}, false, nil
	}
	if err != nil {
		return model.Job{}, false, err
	}
	job.LeaseUntil = leaseUntil

	// 记录这次尝试（取证 + 保留历次错误）。
	if err := insertJobAttemptTx(ctx, s.db, job.ID, job.Attempt, owner, job.FencingToken); err != nil {
		return model.Job{}, false, err
	}
	return job, true, nil
}

// ClaimJobByID 抢占**指定**作业（用于重投：dispatcher 知道是哪条消息）。
//
// 与 ClaimJob 的区别：这里面向「一条具体的消息」，因此必须先确认该作业
// 仍可执行，再尝试抢占。若作业已是终态或不满足条件，返回 (zero,false,nil) ——
// worker 应当**静默跳过**，因为重复投递一条已完成的消息是正常现象。
func (s *JobStore) ClaimJobByID(ctx context.Context, jobID int64, owner string, leaseDuration time.Duration) (model.Job, bool, error) {
	if strings.TrimSpace(owner) == "" {
		return model.Job{}, false, &apiStoreError{Message: "租约持有者标识必填"}
	}
	if leaseDuration <= 0 {
		leaseDuration = model.DefaultLeaseDuration
	}

	var job model.Job
	var leaseUntil *time.Time
	err := s.db.QueryRow(ctx, `
    UPDATE jobs
    SET status = 'leased',
        attempt = attempt + 1,
        lease_owner = $2,
        lease_until = NOW() + $3::interval,
        fencing_token = fencing_token + 1,
        started_at = COALESCE(started_at, NOW()),
        updated_at = NOW()
    WHERE id = $1
      AND status = 'pending'
      AND next_run_at <= NOW()
      AND attempt < max_attempts
    RETURNING
      id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
      next_run_at, lease_owner, lease_until, fencing_token,
      error_class, error_message, retryable, idempotency_key,
      created_by, started_at, finished_at, created_at, updated_at`,
		jobID, owner, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds())),
	).Scan(&job.ID, &job.ProjectID, &job.BatchID, &job.Kind, &job.Payload, &job.Status,
		&job.Attempt, &job.MaxAttempts, &job.NextRunAt, &job.LeaseOwner, &leaseUntil,
		&job.FencingToken, &job.ErrorClass, &job.ErrorMessage, &job.Retryable,
		&job.IdempotencyKey, &job.CreatedBy, &job.StartedAt, &job.FinishedAt,
		&job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Job{}, false, nil
	}
	if err != nil {
		return model.Job{}, false, err
	}
	job.LeaseUntil = leaseUntil

	if err := insertJobAttemptTx(ctx, s.db, job.ID, job.Attempt, owner, job.FencingToken); err != nil {
		return model.Job{}, false, err
	}
	return job, true, nil
}

// HeartbeatJob 续约。返回 false 表示租约已经不属于自己（必须尽快停止工作）。
//
// 续约同样用 token 校验：仅有 job_id + owner 不足以证明自己仍是持有者 ——
// 一个被回收后又被别人抢占的作业，owner 字符串可能因为 worker 复用了
// 同名字（例如同一个容器名重启）而巧合相同，但 token 不会相同。
func (s *JobStore) HeartbeatJob(ctx context.Context, jobID int64, owner string, fencingToken int64, leaseDuration time.Duration) (bool, error) {
	if leaseDuration <= 0 {
		leaseDuration = model.DefaultLeaseDuration
	}
	tag, err := s.db.Exec(ctx, `
    UPDATE jobs
    SET lease_until = NOW() + $4::interval, updated_at = NOW()
    WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3
      AND status IN ('leased', 'running')`,
		jobID, owner, fencingToken, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds())))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkJobRunning 把租约持有者的作业标记为「正在执行」。
//
// 与 leased 区分：leased 表示「已分配但尚未真正开始调用外部服务」。
// 这个区分让「回收一个刚被抢占但进程立刻挂掉」的作业与
// 「回收一个已经调用了外部模型」的作业可以被分别观测（T33 的指标）。
func (s *JobStore) MarkJobRunning(ctx context.Context, jobID int64, owner string, fencingToken int64) error {
	_, err := s.db.Exec(ctx, `
    UPDATE jobs SET status = 'running', updated_at = NOW()
    WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3
      AND status IN ('leased', 'running')`, jobID, owner, fencingToken)
	return err
}

// ---------------------------------------------------------------------------
// 完成与失败（fencing 校验）
// ---------------------------------------------------------------------------

// CompleteJob 提交成功结果。**必须**带 fencing token。
//
// 这是「过期 worker 迟到提交」的拦截点（T06 验收项）。三种拒绝情形：
//   - token 不匹配 → 租约已被回收并被别人抢占（ErrJobLeaseLost）；
//   - owner 不匹配 → 同上；
//   - 已经是终态 → 重复提交（幂等返回成功，不报错，见下）。
//
// 为什么「已是 succeeded」要返回成功而不是错误：worker 在提交后崩溃、
// 消息被重投时会走到这里。此时作业确实已成功，报错会让上层以为失败
// 并触发重试（而重试是多余的）。这是幂等提交的正确语义。
func (s *JobStore) CompleteJob(ctx context.Context, jobID int64, owner string, fencingToken int64, payload any) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status, currentOwner string
	var currentToken int64
	if err := tx.QueryRow(ctx, `
    SELECT status, lease_owner, fencing_token FROM jobs WHERE id = $1 FOR UPDATE`,
		jobID).Scan(&status, &currentOwner, &currentToken); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrJobNotFound
		}
		return false, err
	}

	// 已经是终态：幂等成功（见方法注释）。
	if status == model.JobStatusSucceeded {
		if err := finishJobAttemptTx(ctx, tx, jobID, model.AttemptOutcomeSucceeded, "", ""); err != nil {
			return false, err
		}
		return tx.Commit(ctx) == nil, nil
	}
	if status == model.JobStatusCancelled {
		// 用户明确取消：迟到的成功结果不得把它改回成功。
		return false, ErrJobLeaseLost
	}

	if currentOwner != owner || currentToken != fencingToken+fencingMismatchTolerance {
		// 记录 fenced 痕迹：这是取证依据（谁的结果被丢弃）。
		if err := finishJobAttemptTx(ctx, tx, jobID, model.AttemptOutcomeFenced, model.ErrorClassInternal,
			"租约已被接管（fencing token 不匹配）"); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, ErrJobLeaseLost
	}

	var result json.RawMessage
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return false, err
		}
		result = raw
	} else {
		result = json.RawMessage(`{}`)
	}

	// 成功结果并入 payload：worker 用它把「产出了什么」写回权威状态
	// （例如 batchId 与 sampleVersionId 列表），供 API 读模型直接使用。
	if _, err := tx.Exec(ctx, `
    UPDATE jobs
    SET status = 'succeeded', payload = $2, lease_owner = '', lease_until = NULL,
        error_class = '', error_message = '', finished_at = NOW(), updated_at = NOW()
    WHERE id = $1`, jobID, result); err != nil {
		return false, err
	}

	if err := finishJobAttemptTx(ctx, tx, jobID, model.AttemptOutcomeSucceeded, "", ""); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// FailJob 记录失败，并按「是否可重试 + 尝试次数」决定回到 pending 还是终态。
//
// 三条语义：
//   - 可重试且未达上限 → 回到 pending 并设置退避时间（**不**立即重试）；
//   - 不可重试 → failed 终态（重试同样的输入只会得到同样的结果）；
//   - 已达上限 → dead（自动路径放弃，等人工介入）。
//
// 返回 (requeued, err)：requeued 表示是否会再被执行，供调用方记录与展示。
func (s *JobStore) FailJob(ctx context.Context, jobID int64, owner string, fencingToken int64, errorClass, message string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status, currentOwner string
	var currentToken int64
	var attempt, maxAttempts int
	if err := tx.QueryRow(ctx, `
    SELECT status, lease_owner, fencing_token, attempt, max_attempts
    FROM jobs WHERE id = $1 FOR UPDATE`, jobID).
		Scan(&status, &currentOwner, &currentToken, &attempt, &maxAttempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrJobNotFound
		}
		return false, err
	}

	// 终态不被迟到的失败覆盖（与 batch_items 的同一原则）。
	//
	// 但**必须**返回 ErrJobLeaseLost 而不是 (false, nil)：
	// succeeded/cancelled 都意味着租约已经释放，迟到者此时上报失败属于
	// 「你不是租约持有者」。静默返回成功会让 worker 以为自己上报成功，
	// 于是既不重试也不留痕 —— 那正是 fencing 要防的那种「内容是谁写的」无从回答。
	// 顺序因此不能与 CompleteJob 相同：那边「已是 succeeded」是**真正的**幂等重放，
	// 这边「已是 succeeded」只可能是迟到者（成功路径不会走到 FailJob）。
	if status == model.JobStatusSucceeded || status == model.JobStatusCancelled {
		if err := finishJobAttemptTx(ctx, tx, jobID, model.AttemptOutcomeFenced, errorClass,
			"作业已进入终态，迟到的失败上报被拒绝："+message); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, ErrJobLeaseLost
	}
	if currentOwner != owner || currentToken != fencingToken+fencingMismatchTolerance {
		if err := finishJobAttemptTx(ctx, tx, jobID, model.AttemptOutcomeFenced, errorClass,
			"租约已被接管（fencing token 不匹配）："+message); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, ErrJobLeaseLost
	}

	if len([]rune(message)) > 1000 {
		message = string([]rune(message)[:1000])
	}
	if !isKnownErrorClass(errorClass) {
		errorClass = model.ErrorClassInternal
	}

	retryable := model.IsRetryableJobError(errorClass)
	requeue := retryable && attempt < maxAttempts

	nextStatus := model.JobStatusFailed
	if requeue {
		nextStatus = model.JobStatusPending
	} else if retryable && attempt >= maxAttempts {
		nextStatus = model.JobStatusDead
	}

	backoff := model.JobBackoff(attempt)
	if _, err := tx.Exec(ctx, `
    UPDATE jobs
    SET status = $2,
        lease_owner = '', lease_until = NULL,
        error_class = $3, error_message = $4, retryable = $5,
        next_run_at = CASE WHEN $2 = 'pending' THEN NOW() + $6::interval ELSE next_run_at END,
        finished_at = CASE WHEN $2 IN ('failed', 'dead') THEN NOW() ELSE finished_at END,
        updated_at = NOW()
    WHERE id = $1`,
		jobID, nextStatus, errorClass, message, retryable,
		fmt.Sprintf("%d seconds", int(backoff.Seconds()))); err != nil {
		return false, err
	}

	if err := finishJobAttemptTx(ctx, tx, jobID, model.AttemptOutcomeFailed, errorClass, message); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return requeue, nil
}

// ReclaimExpiredJobs 回收过期租约的作业（心跳停止的 worker 留下的）。
//
// 语义：把租约过期的 leased/running 作业放回 pending，并在 job_attempts 里
// 标记 lease_expired。**不**回退 attempt —— 那次尝试确实开始了，
// 不计数会让 max_attempts 失效（同 ClaimJob 的说明）。
//
// 返回被回收的作业 ID 列表：dispatcher 需要重新投递它们
// （T06 验收项「周期回收 pending/过期租约并重投」）。
func (s *JobStore) ReclaimExpiredJobs(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
    UPDATE jobs
    SET status = 'pending',
        lease_owner = '', lease_until = NULL,
        error_class = CASE WHEN error_class = '' THEN $2 ELSE error_class END,
        error_message = CASE WHEN error_message = '' THEN $3 ELSE error_message END,
        next_run_at = NOW(),
        updated_at = NOW()
    WHERE id IN (
      SELECT id FROM jobs
      WHERE status IN ('leased', 'running')
        AND lease_until IS NOT NULL AND lease_until < NOW()
      ORDER BY lease_until
      FOR UPDATE SKIP LOCKED
      LIMIT $1
    )
    RETURNING id, attempt`,
		limit, model.ErrorClassTimeout, "租约过期，执行者未在租约内完成")
	if err != nil {
		return nil, err
	}

	type reclaimed struct {
		id      int64
		attempt int
	}
	var items []reclaimed
	for rows.Next() {
		var item reclaimed
		if err := rows.Scan(&item.id, &item.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if err := finishJobAttemptTx(ctx, tx, item.id, model.AttemptOutcomeLeaseExpired,
			model.ErrorClassTimeout, "租约过期，已回收到待执行"); err != nil {
			return nil, err
		}
		// 回收也要重新派发：否则作业回到 pending 但没有任何消息唤醒 worker，
		// 只能等下一次「启动时全量扫描」—— 那正是旧实现的形态。
		if err := appendOutboxTx(ctx, tx, outboxEventIDForJobReclaim(item.id, item.attempt),
			"studio.jobs", map[string]any{"jobId": item.id, "reason": "lease_expired"}); err != nil {
			return nil, err
		}
		ids = append(ids, item.id)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

// outboxEventIDForJobReclaim 生成回收事件的幂等 ID。
//
// 带上 attempt：同一个作业的**每一次**回收都需要一次新的派发，
// 而如果用 `job:<id>` 作为 event_id，第二次回收会被 outbox 的唯一约束
// 静默吞掉（事件已存在），于是作业永远收不到新的唤醒消息。
func outboxEventIDForJobReclaim(jobID int64, attempt int) string {
	return "job:" + strconv.FormatInt(jobID, 10) + ":reclaim:" + strconv.Itoa(attempt)
}

// RearmUndeliveredJobOutbox 重新武装「作业还没跑、但派发意图已经被消耗掉」的 outbox 事件。
//
// 这是 T06 验收项「Redis 重启不丢本地任务」的正解。丢消息有两种形态，
// 而它们的共同特征是「jobs 表说 pending，outbox 表说没事了」：
//
//  1. **投递成功但 Redis 丢了它**（重启/未持久化/flushdb）：事件是 dispatched，
//     消息却从未被任何 worker 看到。因为 Redis List 是破坏性读取且没有 ack，
//     「投递成功」无法证明「有人拿到」。
//  2. **投递本身反复失败到上限**（Redis 长时间不可用）：事件被标记 failed，
//     但作业从未被执行。此时给它终态等于**静默丢掉用户的命令**。
//
// 为什么用「重新武装同一条事件」而不是另写一套从 jobs 表直接投递的旁路：
// 旁路会让「待派发」有两个真相来源，而重新武装保持了**单一派发通道** ——
// 所有消息都经过 outbox，可观测指标（PendingOutboxCount）也才不失真。
//
// 为什么阈值是「已经 dispatched 超过 stalledAfter」而不是立即重投：
// 刚投出去的消息可能正在被 worker 幂等抢占，立刻重投只会制造无意义的重复消息。
// 而重投本身是**安全**的（抢占是 CAS，重复消息会被 ClaimJobByID 拒绝），
// 所以这里宁可偶尔多投一次，也不要漏掉一个永远不会再被唤醒的作业。
//
// 返回被重新武装的事件数。
func (s *JobStore) RearmUndeliveredJobOutbox(ctx context.Context, stalledAfter time.Duration, limit int) (int, error) {
	if stalledAfter <= 0 {
		stalledAfter = time.Minute
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	rows, err := s.db.Query(ctx, `
    UPDATE outbox
    SET status = 'pending',
        next_attempt_at = NOW(),
        -- failed 事件重新武装时把 attempts 归零：它的重试上限是「连续失败」的
        -- 上界，不是「这个作业一生只能派发几次」。不清零会让刚武装好的事件
        -- 在下一轮就被 MarkOutboxFailed 再次判死，形成「武装→判死」的空转。
        attempts = CASE WHEN status = 'failed' THEN 0 ELSE attempts END,
        last_error = CASE
          WHEN last_error = '' THEN $3
          ELSE last_error || ' ｜ 重投：' || $3
        END
    WHERE id IN (
      SELECT o.id FROM outbox o
      JOIN jobs j ON o.event_id = 'job:' || j.id::text
      WHERE j.status = 'pending'
        AND j.next_run_at <= NOW()
        AND (
          (o.status = 'dispatched' AND o.dispatched_at IS NOT NULL
             AND o.dispatched_at < NOW() - make_interval(secs => $1))
          OR o.status = 'failed'
        )
      ORDER BY o.id
      FOR UPDATE OF o SKIP LOCKED
      LIMIT $2
    )
    RETURNING id`, stalledAfter.Seconds(), limit,
		"作业仍未被执行，重新派发（Redis 可能丢过消息）")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	rearmed := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return rearmed, err
		}
		rearmed++
	}
	return rearmed, rows.Err()
}

// ---------------------------------------------------------------------------
// 重试（人工）
// ---------------------------------------------------------------------------

// RetryJob 人工重试一个终态作业（failed/dead）。
//
// 只允许终态：对 running 的作业「重试」等于并发执行两次同一作业，
// 那不是重试而是重复执行（会多花钱）。
func (s *JobStore) RetryJob(ctx context.Context, jobID int64) (model.Job, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if err := tx.QueryRow(ctx, `
    SELECT status FROM jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Job{}, ErrJobNotFound
		}
		return model.Job{}, err
	}
	if status != model.JobStatusFailed && status != model.JobStatusDead {
		return model.Job{}, fmt.Errorf("%w：只有失败或已放弃的作业可以重试（当前 %s）",
			ErrJobNotClaimable, status)
	}

	// 重置 attempt：人工重试意味着「我已经修好了原因，给我完整的重试预算」。
	// 若保留旧 attempt，一个耗尽预算的作业重试一次又会立刻 dead，用户会困惑。
	if _, err := tx.Exec(ctx, `
    UPDATE jobs
    SET status = 'pending', attempt = 0, next_run_at = NOW(),
        error_class = '', error_message = '', lease_owner = '', lease_until = NULL,
        finished_at = NULL, updated_at = NOW()
    WHERE id = $1`, jobID); err != nil {
		return model.Job{}, err
	}

	// 重新派发：人工重试的生命周期与新作业一致，都必须被唤醒。
	if err := appendOutboxTx(ctx, tx, outboxEventIDForJobReclaim(jobID, -1),
		"studio.jobs", map[string]any{"jobId": jobID, "reason": "manual_retry"}); err != nil {
		return model.Job{}, err
	}

	job, err := scanJob(tx.QueryRow(ctx, jobSelectByIDSQL, jobID))
	if err != nil {
		return model.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Job{}, err
	}
	return job, nil
}

// CancelJob 取消一个未完成的作业。
func (s *JobStore) CancelJob(ctx context.Context, jobID int64) (model.Job, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
    UPDATE jobs
    SET status = 'cancelled', lease_owner = '', lease_until = NULL,
        finished_at = NOW(), updated_at = NOW()
    WHERE id = $1 AND status IN ('pending', 'leased', 'running')`, jobID)
	if err != nil {
		return model.Job{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Job{}, fmt.Errorf("%w：作业不在可取消状态", ErrJobNotClaimable)
	}

	job, err := scanJob(tx.QueryRow(ctx, jobSelectByIDSQL, jobID))
	if err != nil {
		return model.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Job{}, err
	}
	return job, nil
}

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

// jobSelectByIDSQL 按 ID 读取作业（静态语句，见 jobColumns 的说明）。
const jobSelectByIDSQL = `
  SELECT
    id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
    next_run_at, lease_owner, lease_until, fencing_token,
    error_class, error_message, retryable, idempotency_key,
    created_by, started_at, finished_at, created_at, updated_at
  FROM jobs WHERE id = $1`

// jobColumns 是作业的统一列清单，**只作为文档与人工核对的单一来源**。
//
// 为什么不把它拼进 SQL（曾是初版实现）：把列清单常量插值到语句里会让
// 每一处调用都多一个「这段 SQL 不是静态的」的审查面 —— 静态分析器无法
// 区分「可信常量」与「用户输入」，于是每一处都会报可疑注入点，
// 而真正的注入点会因此被淹没在噪声里。
//
// 因此本文件里每条语句都**完整静态**地写出列清单。代价是列清单重复出现，
// 由 jobScanDest 统一保证「读的顺序」不会漂移，并由
// TestJobSelectColumnsMatchScanOrder 断言静态语句与扫描目标的列数一致。
const jobColumns = `
  id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
  next_run_at, lease_owner, lease_until, fencing_token,
  error_class, error_message, retryable, idempotency_key,
  created_by, started_at, finished_at, created_at, updated_at`

// GetJob 读取单个作业。
func (s *JobStore) GetJob(ctx context.Context, jobID int64) (model.Job, error) {
	return scanJob(s.db.QueryRow(ctx, jobSelectByIDSQL, jobID))
}

// ListJobsByBatch 列出某批次的作业（按创建时间倒序）。
func (s *JobStore) ListJobsByBatch(ctx context.Context, batchID int64, limit int) ([]model.Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    SELECT
      id, project_id, batch_id, job_kind, payload, status, attempt, max_attempts,
      next_run_at, lease_owner, lease_until, fencing_token,
      error_class, error_message, retryable, idempotency_key,
      created_by, started_at, finished_at, created_at, updated_at
    FROM jobs WHERE batch_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, batchID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, job)
	}
	return items, rows.Err()
}

// ListJobAttempts 列出某作业的尝试历史。
func (s *JobStore) ListJobAttempts(ctx context.Context, jobID int64) ([]model.JobAttempt, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, job_id, attempt, lease_owner, fencing_token, outcome,
           error_class, error_message, started_at, finished_at
    FROM job_attempts WHERE job_id = $1 ORDER BY attempt DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.JobAttempt{}
	for rows.Next() {
		var item model.JobAttempt
		if err := rows.Scan(&item.ID, &item.JobID, &item.Attempt, &item.LeaseOwner,
			&item.FencingToken, &item.Outcome, &item.ErrorClass, &item.ErrorMessage,
			&item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CountJobsByStatus 按状态统计（T33 的可观测性；也用于测试对账）。
//
// 返回按状态分组的计数。项目级统计用 projectID > 0 过滤。
func (s *JobStore) CountJobsByStatus(ctx context.Context, projectID int64) (map[string]int, error) {
	var rows pgx.Rows
	var err error
	if projectID > 0 {
		rows, err = s.db.Query(ctx, `
      SELECT status, COUNT(*) FROM jobs WHERE project_id = $1 GROUP BY status`, projectID)
	} else {
		rows, err = s.db.Query(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// ---------------------------------------------------------------------------
// outbox 派发
// ---------------------------------------------------------------------------

// ClaimOutboxBatch 取一批待派发事件并标记为「正在派发」（attempts +1）。
//
// 为什么在取的时候就 +1：派发是一个「至少一次」的动作，取走即视为一次尝试。
// 若在成功后才计数，一个反复失败的事件会永远显示 attempts=0，
// 而它其实已经占用了无数轮 dispatcher。
func (s *JobStore) ClaimOutboxBatch(ctx context.Context, limit int) ([]model.OutboxEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
    UPDATE outbox
    SET attempts = attempts + 1, next_attempt_at = NOW() + INTERVAL '30 seconds'
    WHERE id IN (
      SELECT id FROM outbox
      WHERE status = 'pending' AND next_attempt_at <= NOW()
      ORDER BY next_attempt_at, id
      FOR UPDATE SKIP LOCKED
      LIMIT $1
    )
    RETURNING id, event_id, topic, payload, status, attempts, next_attempt_at,
              last_error, created_at, dispatched_at`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.OutboxEvent{}
	for rows.Next() {
		var item model.OutboxEvent
		if err := rows.Scan(&item.ID, &item.EventID, &item.Topic, &item.Payload, &item.Status,
			&item.Attempts, &item.NextAttemptAt, &item.LastError, &item.CreatedAt,
			&item.DispatchedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// MarkOutboxDispatched 标记事件已成功派发。
func (s *JobStore) MarkOutboxDispatched(ctx context.Context, eventDBID int64) error {
	_, err := s.db.Exec(ctx, `
    UPDATE outbox
    SET status = 'dispatched', dispatched_at = NOW(), last_error = ''
    WHERE id = $1`, eventDBID)
	return err
}

// MarkOutboxFailed 记录派发失败。
//
// 超过 OutboxMaxAttempts 后标记为 failed 终态：无限重试会让一个永久坏事件
// （例如 topic 无人消费）永远占着 dispatcher 的轮次，而它需要的是人工介入。
func (s *JobStore) MarkOutboxFailed(ctx context.Context, eventDBID int64, message string) error {
	if len([]rune(message)) > 500 {
		message = string([]rune(message)[:500])
	}
	_, err := s.db.Exec(ctx, `
    UPDATE outbox
    SET status = CASE WHEN attempts >= $2 THEN 'failed' ELSE 'pending' END,
        last_error = $3,
        next_attempt_at = NOW() + INTERVAL '30 seconds'
    WHERE id = $1`, eventDBID, model.OutboxMaxAttempts, message)
	return err
}

// PendingOutboxCount 返回待派发事件数（T33 的「未派发 outbox」指标）。
//
// 这个数字是「API 已接受但任务可能还没被唤醒」的直接度量：
// 它持续增长说明 dispatcher 没有工作，而用户会看到「排队中」不动。
func (s *JobStore) PendingOutboxCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE status = 'pending'`).Scan(&count)
	return count, err
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// insertJobAttemptTx 插入一条尝试记录。
//
// 用 ON CONFLICT DO NOTHING：抢占与尝试记录之间有极小的并发窗口
// （例如重投 + 回收同时发生），此时**不**让尝试记录的唯一约束
// 把整个抢占回滚 —— 抢占本身是更重要的事实。
func insertJobAttemptTx(ctx context.Context, execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, jobID int64, attempt int, owner string, fencingToken int64) error {
	_, err := execer.Exec(ctx, `
    INSERT INTO job_attempts (job_id, attempt, lease_owner, fencing_token, outcome)
    VALUES ($1, $2, $3, $4, 'running')
    ON CONFLICT (job_id, attempt) DO UPDATE
    SET lease_owner = EXCLUDED.lease_owner,
        fencing_token = EXCLUDED.fencing_token,
        outcome = 'running',
        started_at = NOW()`,
		jobID, attempt, owner, fencingToken)
	return err
}

// finishJobAttemptTx 结束当前这次尝试的记录。
//
// 以 (job_id, 最大 attempt) 为目标：调用方不需要自己传 attempt 号，
// 因为「正在结束的尝试」永远是最后一个 —— 传错 attempt 会让错误的记录被标记，
// 而那种错误在取证时是致命的（会把一次 fenced 记成 success）。
func finishJobAttemptTx(ctx context.Context, tx pgx.Tx, jobID int64, outcome, errorClass, message string) error {
	if len([]rune(message)) > 1000 {
		message = string([]rune(message)[:1000])
	}
	_, err := tx.Exec(ctx, `
    UPDATE job_attempts
    SET outcome = $2, error_class = $3, error_message = $4, finished_at = NOW()
    WHERE job_id = $1
      AND attempt = (SELECT MAX(attempt) FROM job_attempts WHERE job_id = $1)
      AND outcome = 'running'`,
		jobID, outcome, errorClass, message)
	return err
}

// scanJob 读取一行作业。
func scanJob(row pgx.Row) (model.Job, error) {
	var job model.Job
	var leaseUntil *time.Time
	err := row.Scan(&job.ID, &job.ProjectID, &job.BatchID, &job.Kind, &job.Payload, &job.Status,
		&job.Attempt, &job.MaxAttempts, &job.NextRunAt, &job.LeaseOwner, &leaseUntil,
		&job.FencingToken, &job.ErrorClass, &job.ErrorMessage, &job.Retryable,
		&job.IdempotencyKey, &job.CreatedBy, &job.StartedAt, &job.FinishedAt,
		&job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return model.Job{}, err
	}
	job.LeaseUntil = leaseUntil
	return job, nil
}
