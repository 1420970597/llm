package store

import (
	"context"
	"encoding/json"
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

// 本文件验证 Issue #160 T06 的可靠派发不变量。
//
// 对应 T06 验收项逐条列出的四个故障场景：
//  1. API 落库后、派发前崩溃 → 作业仍在 pending + outbox 仍待派发；
//  2. BRPOP 后进程退出 → 租约过期被回收并重投；
//  3. 重复消息 → 抢占是 CAS，第二次拿不到；终态提交幂等；
//  4. 租约过期后旧 worker 返回 → fencing token 不匹配，提交被拒绝。
//
// 为什么必须连真实 Postgres：全部断言都关于**并发与事务语义**——
// 行锁、SKIP LOCKED、CAS 条件更新、租约过期判定。
// 这些用内存实现「测」的话测的是测试自己的实现，不是产品的。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

// jobFixture 是一个项目，供作业测试使用。
type jobFixture struct {
	pool      *pgxpool.Pool
	projectID int64
	userID    int64
	jobs      *JobStore
}

// newJobFixture 建一个项目 + 一个用户。
func newJobFixture(t *testing.T) jobFixture {
	t.Helper()
	pool := newStudioTestPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", os.Getpid(), t.Name())
	workspaceID := seedAuthzWorkspace(t, pool, suffix)
	userID := seedStudioUser(t, pool, "job-user-"+suffix)

	project, err := NewProjectStore(pool).CreateProject(ctx, workspaceID, userID, validProjectInput("作业测试项目"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	t.Cleanup(func() {
		// 作业是项目级联删除的；outbox 没有项目外键，因此按 event_id 前缀清理。
		_, _ = pool.Exec(context.Background(), `DELETE FROM outbox WHERE event_id LIKE 'job:%'`)
	})

	return jobFixture{pool: pool, projectID: project.ID, userID: userID, jobs: NewJobStore(pool)}
}

// TestEnqueueJobIsIdempotentAndAtomicWithOutbox 覆盖验收场景 1 的一半：
// 「业务事务里创建作业」必须同时产生 outbox 派发意图，且幂等键只产生一个作业。
//
// 「API 落库后派发前崩溃」之所以不丢任务，正是因为这一条：
// 作业与 outbox 同事务落库，dispatcher 之后一定能看到它。
func TestEnqueueJobIsIdempotentAndAtomicWithOutbox(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	input := EnqueueJobInput{
		ProjectID:      &projectID,
		Kind:           model.JobKindBatchGenerate,
		Payload:        map[string]any{"batchId": 1, "units": 5},
		IdempotencyKey: "batch-generate-key-1",
		CreatedBy:      &fixture.userID,
	}

	job, created, err := fixture.jobs.EnqueueJob(ctx, input)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if !created {
		t.Fatal("首次入队必须 created=true")
	}
	if job.Status != model.JobStatusPending {
		t.Fatalf("新作业必须是 pending，实际 %s", job.Status)
	}

	// 幂等：同键再次入队必须返回同一个作业，且不新建。
	again, createdAgain, err := fixture.jobs.EnqueueJob(ctx, input)
	if err != nil {
		t.Fatalf("EnqueueJob again: %v", err)
	}
	if createdAgain {
		t.Fatal("幂等键相同必须命中既有作业，不得新建")
	}
	if again.ID != job.ID {
		t.Fatalf("必须返回同一作业：%d vs %d", job.ID, again.ID)
	}

	// outbox 必须恰好一条（幂等事件 ID）。
	var outboxCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM outbox WHERE event_id = $1`,
		"job:"+fmt.Sprint(job.ID)).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("作业派发意图必须恰好一条，实际 %d 条", outboxCount)
	}

	pending, err := fixture.jobs.PendingOutboxCount(ctx)
	if err != nil {
		t.Fatalf("PendingOutboxCount: %v", err)
	}
	if pending < 1 {
		t.Fatalf("必须有待派发事件，实际 %d", pending)
	}
}

// TestOutboxDispatchSurvivesCrashBeforeDispatch 覆盖验收场景 1 的另一半：
// 「API 落库后派发前崩溃」不丢任务 —— outbox 仍是 pending，可被重新派发。
//
// 手法：写入后**不做任何派发动作**，断言 outbox 仍可被 ClaimOutboxBatch 取到，
// 且作业仍在 pending。这就是崩溃后的真实状态。
func TestOutboxDispatchSurvivesCrashBeforeDispatch(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// 模拟「落库后崩溃」：什么都不做，直接让 dispatcher 下一轮来取。
	events, err := fixture.jobs.ClaimOutboxBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimOutboxBatch: %v", err)
	}
	found := false
	for _, event := range events {
		if event.EventID == "job:"+fmt.Sprint(job.ID) {
			found = true
			// 载荷只含 id 与 kind，不含业务载荷（契约 §5）。
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("outbox payload 必须是 JSON: %v", err)
			}
			if payload["jobId"] == nil {
				t.Fatalf("outbox 载荷必须含 jobId，实际 %+v", payload)
			}
			if _, hasUnits := payload["units"]; hasUnits {
				t.Fatal("outbox 载荷不得携带业务载荷（只放 ID，避免重投用旧载荷执行）")
			}
		}
	}
	if !found {
		t.Fatal("崩溃后 outbox 事件必须仍可被派发（否则任务永久丢失）")
	}

	// 作业仍在 pending，可被抢占。
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.Status != model.JobStatusPending {
		t.Fatalf("未派发前作业必须仍是 pending，实际 %s", reloaded.Status)
	}
}

// TestClaimJobIsExclusiveUnderConcurrency 覆盖验收场景 3 的一半：
// 「重复消息」「两个 worker 竞争」都不得让同一作业被执行两次。
func TestClaimJobIsExclusiveUnderConcurrency(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	const workers = 12
	type outcome struct {
		index int
		jobID int64
		claim bool
		err   error
	}
	results := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			// 每个 worker 用**不同的 owner 名**：模拟真实的多副本部署。
			claimed, ok, err := fixture.jobs.ClaimJobByID(ctx, job.ID,
				fmt.Sprintf("worker-%d", idx), model.DefaultLeaseDuration)
			results <- outcome{index: idx, jobID: claimed.ID, claim: ok, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("worker %d 抢占出错: %v", result.index, result.err)
		}
		if result.claim {
			claimed++
			if result.jobID != job.ID {
				t.Fatalf("抢到的必须是同一作业 %d，实际 %d", job.ID, result.jobID)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("12 个 worker 必须恰有一个抢到（否则同一作业被执行多次），实际 %d 个", claimed)
	}

	// attempt 恰好为 1：抢占即一次尝试，重复抢占不得虚增。
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.Attempt != 1 {
		t.Fatalf("抢占一次必须只留下 attempt=1，实际 %d", reloaded.Attempt)
	}
	if reloaded.FencingToken != 1 {
		t.Fatalf("首次抢占后 fencing_token 必须是 1，实际 %d", reloaded.FencingToken)
	}
}

// TestFencingRejectsLateSubmitFromExpiredWorker 是 T06 最关键的一条：
// **租约过期后旧 worker 迟到提交必须被拒绝**。
//
// 场景（真实发生过的形态）：worker A 抢到租约后进程被挂起（GC/IO 停顿/
// 容器被 freeze），租约过期被回收，worker B 抢占并完成。此时 A 醒过来提交结果。
//
// 仅靠 lease_until 判断是不够的：A 会认为自己「还在租约内」（它拿的是
// 抢占时读到的 lease_until，而那已经过期但它没重新读）。
// fencing_token 是全序的，B 的抢占让 token 递增，A 的 token 落后 → 被拒绝。
func TestFencingRejectsLateSubmitFromExpiredWorker(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// worker A 抢占，拿到短租约（1 秒，便于过期）。
	claimedA, ok, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-A", time.Second)
	if err != nil || !ok {
		t.Fatalf("worker A 抢占失败: ok=%v err=%v", ok, err)
	}

	// 让租约过期，并回收。
	time.Sleep(1100 * time.Millisecond)
	reclaimed, err := fixture.jobs.ReclaimExpiredJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ReclaimExpiredJobs: %v", err)
	}
	foundReclaimed := false
	for _, id := range reclaimed {
		if id == job.ID {
			foundReclaimed = true
		}
	}
	if !foundReclaimed {
		t.Fatal("过期租约必须被回收（否则作业永久卡在 running）")
	}

	// worker B 抢占同一作业。
	claimedB, ok, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-B", model.DefaultLeaseDuration)
	if err != nil || !ok {
		t.Fatalf("worker B 抢占失败: ok=%v err=%v", ok, err)
	}
	if claimedB.FencingToken <= claimedA.FencingToken {
		t.Fatalf("重新抢占必须递增 fencing token：A=%d B=%d",
			claimedA.FencingToken, claimedB.FencingToken)
	}

	// worker A 迟到提交 → 必须被拒绝。
	completed, err := fixture.jobs.CompleteJob(ctx, job.ID, "worker-A", claimedA.FencingToken,
		map[string]any{"who": "worker-A"})
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("迟到提交必须返回 ErrJobLeaseLost，实际: completed=%v err=%v", completed, err)
	}

	// 作业不得被 A 的结果污染。
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.Status != model.JobStatusLeased {
		t.Fatalf("A 的迟到提交不得改变状态（应仍是 B 持有的 leased），实际 %s", reloaded.Status)
	}
	if reloaded.LeaseOwner != "worker-B" {
		t.Fatalf("租约持有者必须仍是 worker-B，实际 %s", reloaded.LeaseOwner)
	}

	// worker B 正常提交 → 成功。
	completed, err = fixture.jobs.CompleteJob(ctx, job.ID, "worker-B", claimedB.FencingToken,
		map[string]any{"who": "worker-B"})
	if err != nil {
		t.Fatalf("worker B 提交必须成功: %v", err)
	}
	if !completed {
		t.Fatal("worker B 的提交必须返回 completed=true")
	}
	reloaded, err = fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.Status != model.JobStatusSucceeded {
		t.Fatalf("作业必须是 succeeded，实际 %s", reloaded.Status)
	}
	var result map[string]any
	if err := json.Unmarshal(reloaded.Payload, &result); err != nil {
		t.Fatalf("结果 payload 必须是 JSON: %v", err)
	}
	if result["who"] != "worker-B" {
		t.Fatalf("最终结果必须来自 worker-B（A 的结果被丢弃），实际 %+v", result)
	}

	// 取证：history 里必须有 A 的 fenced 记录。
	attempts, err := fixture.jobs.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts: %v", err)
	}
	sawExpired, sawFenced := false, false
	for _, attempt := range attempts {
		if attempt.Outcome == model.AttemptOutcomeLeaseExpired {
			sawExpired = true
		}
		if attempt.Outcome == model.AttemptOutcomeFenced {
			sawFenced = true
		}
	}
	if !sawExpired {
		t.Fatal("租约回收必须留下 lease_expired 记录（否则无法解释作业为何被重跑）")
	}
	if !sawFenced {
		t.Fatal("被拒绝的迟到提交必须留下 fenced 记录（否则无法解释「内容是谁写的」）")
	}
}

// TestFencingRejectsLateFailureFromExpiredWorker 覆盖同一拦截点的失败路径。
//
// 迟到的**失败**同样有害：它会把一个已经被别人成功完成的作业改成失败，
// 于是用户看到「明明有结果，却显示失败」。
func TestFencingRejectsLateFailureFromExpiredWorker(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	claimedA, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-A", time.Second)
	if err != nil {
		t.Fatalf("worker A 抢占: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := fixture.jobs.ReclaimExpiredJobs(ctx, 10); err != nil {
		t.Fatalf("ReclaimExpiredJobs: %v", err)
	}
	claimedB, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-B", model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("worker B 抢占: %v", err)
	}
	if _, err := fixture.jobs.CompleteJob(ctx, job.ID, "worker-B", claimedB.FencingToken, map[string]any{"ok": true}); err != nil {
		t.Fatalf("worker B 提交: %v", err)
	}

	// A 迟到上报失败 → 必须被拒绝，且不得覆盖成功状态。
	requeued, err := fixture.jobs.FailJob(ctx, job.ID, "worker-A", claimedA.FencingToken,
		model.ErrorClassTimeout, "迟到的超时上报")
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("迟到的失败上报必须返回 ErrJobLeaseLost，实际 requeued=%v err=%v", requeued, err)
	}
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.Status != model.JobStatusSucceeded {
		t.Fatalf("迟到的失败不得覆盖成功状态，实际 %s", reloaded.Status)
	}
}

// TestCompleteJobIsIdempotentOnReplay 覆盖验收场景 3 的另一半：
// 「重复消息」在成功提交后重放必须返回成功而不是错误。
//
// 语义理由：worker 提交成功后崩溃、消息被重投时会走到这里。
// 报错会让上层以为失败并触发重试（多余且可能花钱）。
func TestCompleteJobIsIdempotentOnReplay(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-A", model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("抢占: %v", err)
	}
	if _, err := fixture.jobs.CompleteJob(ctx, job.ID, "worker-A", claimed.FencingToken, map[string]any{"n": 2}); err != nil {
		t.Fatalf("首次提交: %v", err)
	}

	// 重放同一提交。
	completed, err := fixture.jobs.CompleteJob(ctx, job.ID, "worker-A", claimed.FencingToken, map[string]any{"n": 3})
	if err != nil {
		t.Fatalf("重放提交必须返回成功而不是错误: %v", err)
	}
	if !completed {
		t.Fatal("重放提交必须返回 completed=true（幂等）")
	}

	// 结果不得被重放改写。
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(reloaded.Payload, &result); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if result["n"] != float64(2) {
		t.Fatalf("重放不得改写结果，期望 n=2 实际 %v", result["n"])
	}
}

// TestHeartbeatUsesFencingToken 覆盖续约的 token 校验。
//
// owner 字符串相同不足以证明身份：同一个容器名重启后 owner 会重复，
// 但 token 不会。若只校验 owner，重启后的旧进程会「续上」新租约。
func TestHeartbeatUsesFencingToken(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "same-name", model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("抢占: %v", err)
	}

	// 正确 token + owner → 续约成功。
	ok, err := fixture.jobs.HeartbeatJob(ctx, job.ID, "same-name", claimed.FencingToken, model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("HeartbeatJob: %v", err)
	}
	if !ok {
		t.Fatal("正确 token 的续约必须成功")
	}

	// 同 owner 名但旧 token → 必须失败。
	stale, err := fixture.jobs.HeartbeatJob(ctx, job.ID, "same-name", claimed.FencingToken-1, model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("HeartbeatJob(stale): %v", err)
	}
	if stale {
		t.Fatal("旧 token 的续约必须失败（否则重启后的旧进程会延长租约）")
	}
}

// TestReclaimExpiredJobsRepublishesOutbox 覆盖验收项
// 「周期回收 pending/过期租约并重投」。
//
// 只回收不重投是不够的：作业回到 pending 但没有任何消息唤醒 worker，
// 于是它要等到下一次「启动时全量扫描」—— 那正是旧实现的形态。
func TestReclaimExpiredJobsRepublishesOutbox(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if _, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-A", time.Second); err != nil {
		t.Fatalf("抢占: %v", err)
	}

	// 清掉初始的派发事件，便于断言「回收产生了新事件」。
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM outbox WHERE event_id = $1`, "job:"+fmt.Sprint(job.ID)); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	reclaimed, err := fixture.jobs.ReclaimExpiredJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ReclaimExpiredJobs: %v", err)
	}
	if len(reclaimed) == 0 {
		t.Fatal("过期租约必须被回收")
	}

	// 回收必须产生**新的**派发事件（event_id 带 attempt，避免被唯一约束吞掉）。
	var republishCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM outbox WHERE event_id LIKE $1`,
		"job:"+fmt.Sprint(job.ID)+":reclaim:%").Scan(&republishCount); err != nil {
		t.Fatalf("count reclaim outbox: %v", err)
	}
	if republishCount != 1 {
		t.Fatalf("回收必须产生 1 条新的派发事件（否则作业不会被唤醒），实际 %d 条", republishCount)
	}

	// 作业回到 pending 且可被再次抢占。
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.Status != model.JobStatusPending {
		t.Fatalf("回收后必须是 pending，实际 %s", reloaded.Status)
	}
	if reloaded.Attempt != 1 {
		t.Fatalf("回收**不得**回退 attempt（否则 max_attempts 失效），实际 %d", reloaded.Attempt)
	}
	if reloaded.ErrorClass != model.ErrorClassTimeout {
		t.Fatalf("回收必须标记错误类别（供界面解释），实际 %q", reloaded.ErrorClass)
	}
}

// TestFailJobRetryPolicy 覆盖重试策略：可重试退避回 pending，
// 不可重试直接失败，重试耗尽转 dead。
func TestFailJobRetryPolicy(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		errorClass  string
		maxAttempts int
		// 抢几次（每次失败一次）
		claims          int
		wantFinal       string
		wantRequeueLast bool
	}{
		{
			name: "限流可重试且未耗尽", errorClass: model.ErrorClassRateLimited,
			maxAttempts: 3, claims: 1,
			wantFinal: model.JobStatusPending, wantRequeueLast: true,
		},
		{
			name: "schema 不可重试", errorClass: model.ErrorClassSchema,
			maxAttempts: 3, claims: 1,
			wantFinal: model.JobStatusFailed, wantRequeueLast: false,
		},
		{
			name: "限流但耗尽重试", errorClass: model.ErrorClassRateLimited,
			maxAttempts: 2, claims: 2,
			wantFinal: model.JobStatusDead, wantRequeueLast: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			projectID := fixture.projectID
			job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
				ProjectID: &projectID, Kind: model.JobKindBatchGenerate,
				Payload:     map[string]any{"case": testCase.name},
				MaxAttempts: testCase.maxAttempts,
			})
			if err != nil {
				t.Fatalf("EnqueueJob: %v", err)
			}

			var lastRequeue bool
			for i := 0; i < testCase.claims; i++ {
				claimed, ok, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker", model.DefaultLeaseDuration)
				if err != nil {
					t.Fatalf("抢占 %d: %v", i, err)
				}
				if !ok {
					t.Fatalf("第 %d 次抢占必须成功", i)
				}
				lastRequeue, err = fixture.jobs.FailJob(ctx, job.ID, "worker", claimed.FencingToken,
					testCase.errorClass, "测试失败")
				if err != nil {
					t.Fatalf("FailJob %d: %v", i, err)
				}
				if lastRequeue {
					// 退避后需要把 next_run_at 提前，否则下一次抢占会被时间条件挡住。
					if _, err := fixture.pool.Exec(ctx, `
            UPDATE jobs SET next_run_at = NOW() WHERE id = $1`, job.ID); err != nil {
						t.Fatalf("reset next_run_at: %v", err)
					}
				}
			}

			if lastRequeue != testCase.wantRequeueLast {
				t.Fatalf("最后一次 requeue 必须是 %v，实际 %v", testCase.wantRequeueLast, lastRequeue)
			}
			reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("GetJob: %v", err)
			}
			if reloaded.Status != testCase.wantFinal {
				t.Fatalf("最终状态必须是 %s，实际 %s", testCase.wantFinal, reloaded.Status)
			}
		})
	}
}

// TestJobBackoffGrowsAndIsCapped 覆盖退避计算。
//
// 限流场景立即重试只会再被限流，因此退避必须增长；但也不能无上限
// （用户以为点了恢复没反应）。
func TestJobBackoffGrowsAndIsCapped(t *testing.T) {
	first := model.JobBackoff(1)
	if first <= 0 {
		t.Fatalf("首次退避必须为正，实际 %v", first)
	}
	second := model.JobBackoff(2)
	if second <= first {
		t.Fatalf("退避必须增长：attempt1=%v attempt2=%v", first, second)
	}
	// 极大 attempt 必须被上限夹住，且不溢出为负数。
	huge := model.JobBackoff(1000)
	if huge != model.JobBackoffMax {
		t.Fatalf("退避必须被上限夹住（%v），实际 %v", model.JobBackoffMax, huge)
	}
	if model.JobBackoff(0) <= 0 || model.JobBackoff(-5) <= 0 {
		t.Fatal("非法 attempt 必须归一到正的退避，不得返回零或负数")
	}
}

// TestRetryJobResetsBudgetAndRepublishes 覆盖人工重试。
//
// attempt 必须重置：人工重试的语义是「我已经修好了原因，给我完整的重试预算」。
// 保留旧 attempt 会让一个耗尽预算的作业重试一次又立刻 dead，用户会困惑。
func TestRetryJobResetsBudgetAndRepublishes(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate,
		Payload: map[string]any{"n": 1}, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker", model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("抢占: %v", err)
	}
	if _, err := fixture.jobs.FailJob(ctx, job.ID, "worker", claimed.FencingToken,
		model.ErrorClassSchema, "schema 错"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	// 不可重试 → failed 终态。
	reloaded, _ := fixture.jobs.GetJob(ctx, job.ID)
	if reloaded.Status != model.JobStatusFailed {
		t.Fatalf("不可重试必须进入 failed，实际 %s", reloaded.Status)
	}

	// 清掉 outbox 便于断言重试产生了新事件。
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM outbox`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}

	retried, err := fixture.jobs.RetryJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if retried.Status != model.JobStatusPending {
		t.Fatalf("人工重试后必须是 pending，实际 %s", retried.Status)
	}
	if retried.Attempt != 0 {
		t.Fatalf("人工重试必须重置 attempt=0（给完整预算），实际 %d", retried.Attempt)
	}
	if retried.ErrorClass != "" || retried.ErrorMessage != "" {
		t.Fatalf("人工重试必须清掉旧错误（否则界面仍显示旧原因），实际 %q/%q",
			retried.ErrorClass, retried.ErrorMessage)
	}
	var republish int
	if err := fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE status = 'pending'`).Scan(&republish); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if republish != 1 {
		t.Fatalf("人工重试必须产生 1 条派发事件，实际 %d", republish)
	}

	// 对 running 的作业「重试」必须被拒绝：那不是重试而是并发重复执行。
	second, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 2},
	})
	if err != nil {
		t.Fatalf("EnqueueJob second: %v", err)
	}
	if _, _, err := fixture.jobs.ClaimJobByID(ctx, second.ID, "worker", model.DefaultLeaseDuration); err != nil {
		t.Fatalf("抢占 second: %v", err)
	}
	if _, err := fixture.jobs.RetryJob(ctx, second.ID); !errors.Is(err, ErrJobNotClaimable) {
		t.Fatalf("对运行中的作业重试必须被拒绝，实际: %v", err)
	}
}

// TestCancelJobBlocksLateSuccess 覆盖取消语义：
// 用户明确取消后，迟到的成功结果不得把它改回成功。
func TestCancelJobBlocksLateSuccess(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker", model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("抢占: %v", err)
	}

	cancelled, err := fixture.jobs.CancelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if cancelled.Status != model.JobStatusCancelled {
		t.Fatalf("必须是 cancelled，实际 %s", cancelled.Status)
	}

	// 迟到成功 → 必须被拒绝。
	completed, err := fixture.jobs.CompleteJob(ctx, job.ID, "worker", claimed.FencingToken, map[string]any{"n": 2})
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("取消后的迟到提交必须被拒绝，实际 completed=%v err=%v", completed, err)
	}
	reloaded, _ := fixture.jobs.GetJob(ctx, job.ID)
	if reloaded.Status != model.JobStatusCancelled {
		t.Fatalf("状态必须仍是 cancelled，实际 %s", reloaded.Status)
	}

	// 已取消的作业不可重试（避免「取消」变成可绕过的软状态）。
	if _, err := fixture.jobs.RetryJob(ctx, job.ID); !errors.Is(err, ErrJobNotClaimable) {
		t.Fatalf("已取消的作业不得重试，实际: %v", err)
	}
}

// TestJobEnvelopeRoundTripAndRejection 覆盖消息体契约。
//
// 三条断言对应 T06 与 T33 的要求：
//   - 消息自证 schema 版本（版本不兼容必须拒绝而不是尽力解析）；
//   - 新 worker 能识别旧格式消息（不得误吞）；
//   - 消息只含 ID，不含业务载荷。
func TestJobEnvelopeRoundTripAndRejection(t *testing.T) {
	raw, err := model.EncodeJobEnvelope(42, model.JobKindBatchGenerate)
	if err != nil {
		t.Fatalf("EncodeJobEnvelope: %v", err)
	}
	envelope, err := model.DecodeJobEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeJobEnvelope: %v", err)
	}
	if envelope.JobID != 42 || envelope.Kind != model.JobKindBatchGenerate {
		t.Fatalf("往返必须保持内容，实际 %+v", envelope)
	}
	if envelope.SchemaVersion != model.JobEnvelopeSchemaVersion {
		t.Fatalf("必须带 schema 版本，实际 %d", envelope.SchemaVersion)
	}

	// 消息体不得含业务载荷（只放 id 与 kind）。
	if !model.IsStudioJobKind(envelope.Kind) {
		t.Fatalf("新作业类型必须带 studio. 前缀，实际 %q", envelope.Kind)
	}

	// 未来版本必须被拒绝。
	future := []byte(`{"schemaVersion":99,"jobId":7,"kind":"studio.batch.generate"}`)
	if _, err := model.DecodeJobEnvelope(future); err == nil {
		t.Fatal("不兼容的 schema 版本必须被拒绝（尽力解析会产生错误业务动作）")
	}

	// 缺 jobId 的「新格式」必须被拒绝，而不是当成 jobId=0 的请求。
	if _, err := model.DecodeJobEnvelope([]byte(`{"schemaVersion":1}`)); err == nil {
		t.Fatal("缺少 jobId 的消息必须被拒绝（当成 0 号作业会写到别的对象）")
	}

	// 旧格式识别。
	legacy := []byte(`{"type":"sft.generate","datasetId":161}`)
	if !model.IsLegacyJobPayload(legacy) {
		t.Fatal("旧格式消息必须能被识别（新 worker 不得误吞旧消息）")
	}
	if model.IsLegacyJobPayload(raw) {
		t.Fatal("新格式消息不得被判为旧格式")
	}
	// 新格式消息也不能被旧 worker 误判成「有 type 无 jobId」的旧消息。
	newFormat := []byte(`{"schemaVersion":1,"jobId":9,"kind":"studio.batch.generate"}`)
	if model.IsLegacyJobPayload(newFormat) {
		t.Fatal("带 jobId 的新格式不得被判为旧格式")
	}
}

// TestOutboxFailureBackoffAndTerminalState 覆盖 outbox 派发失败处理。
//
// 无限重试会让一个永久坏事件永远占着 dispatcher 的轮次，
// 因此超过上限必须转 failed 终态并留错误。
func TestOutboxFailureBackoffAndTerminalState(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	if _, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	}); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	events, err := fixture.jobs.ClaimOutboxBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimOutboxBatch: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("必须有待派发事件")
	}
	target := events[0]
	if target.Attempts != 1 {
		t.Fatalf("取走即视为一次尝试，实际 attempts=%d", target.Attempts)
	}

	// 记录失败：未到上限 → 回到 pending 且带退避。
	if err := fixture.jobs.MarkOutboxFailed(ctx, target.ID, "redis 不可用"); err != nil {
		t.Fatalf("MarkOutboxFailed: %v", err)
	}
	var status string
	var nextAttempt time.Time
	if err := fixture.pool.QueryRow(ctx, `
    SELECT status, next_attempt_at FROM outbox WHERE id = $1`, target.ID).
		Scan(&status, &nextAttempt); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if status != model.OutboxStatusPending {
		t.Fatalf("未到上限必须回到 pending，实际 %s", status)
	}
	if !nextAttempt.After(time.Now()) {
		t.Fatal("派发失败必须退避（不能忙等，Redis 恢复需要时间）")
	}

	// 达到上限 → failed 终态。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE outbox SET attempts = $2 WHERE id = $1`, target.ID, model.OutboxMaxAttempts); err != nil {
		t.Fatalf("bump attempts: %v", err)
	}
	if err := fixture.jobs.MarkOutboxFailed(ctx, target.ID, "仍然不可用"); err != nil {
		t.Fatalf("MarkOutboxFailed(terminal): %v", err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM outbox WHERE id = $1`, target.ID).Scan(&status); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if status != model.OutboxStatusFailed {
		t.Fatalf("达到上限必须转 failed 终态（等待人工介入），实际 %s", status)
	}
}

// TestJobColumnsMatchScanOrder 是列清单与扫描目标一致性的守卫。
//
// 为什么需要它：为了让 SQL 完全静态（避免把列清单插值进语句）
// 而重复写出了列清单，代价是「加一列却忘了改某条语句」的风险。
// 这条测试直接对真实表做一次全列 SELECT 并与扫描结果比对，
// 让漂移在测试阶段就暴露，而不是在运行时以「错列」形式出现。
func TestJobColumnsMatchScanOrder(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate,
		Payload: map[string]any{"marker": "column-check"}, CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// 用与生产代码相同的静态语句读回，断言关键字段不串位。
	reloaded, err := fixture.jobs.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if reloaded.ID != job.ID || reloaded.Kind != job.Kind {
		t.Fatalf("ID/kind 串位：期望 %d/%s 实际 %d/%s",
			job.ID, job.Kind, reloaded.ID, reloaded.Kind)
	}
	if reloaded.ProjectID == nil || *reloaded.ProjectID != projectID {
		t.Fatalf("project_id 串位：%+v", reloaded.ProjectID)
	}
	if reloaded.MaxAttempts != model.DefaultMaxAttempts || reloaded.Status != model.JobStatusPending {
		t.Fatalf("max_attempts/status 串位：%+v", reloaded)
	}
	if reloaded.Retryable != true {
		// 新作业的 retryable 默认 true（尚未失败）。
		t.Fatalf("retryable 串位：%+v", reloaded.Retryable)
	}
	var payload map[string]any
	if err := json.Unmarshal(reloaded.Payload, &payload); err != nil {
		t.Fatalf("payload 串位（不是 JSON）: %v", err)
	}
	if payload["marker"] != "column-check" {
		t.Fatalf("payload 串位：%+v", payload)
	}

	// 表里的列数必须与静态清单一致（加列但忘了改语句时会在此失败）。
	var columnCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM information_schema.columns
    WHERE table_name = 'jobs' AND table_schema = current_schema()`).Scan(&columnCount); err != nil {
		t.Fatalf("count columns: %v", err)
	}
	// jobColumns 里声明的列数（21）。变更表结构时必须同步更新这个数字与语句。
	const declaredColumns = 21
	if columnCount != declaredColumns {
		t.Fatalf("jobs 表列数（%d）与 jobColumns 静态清单（%d）不一致："+
			"加/删列时请同步更新本文件所有静态 SELECT/RETURNING 语句与这个常量",
			columnCount, declaredColumns)
	}
}

// TestJobNotFoundAndLeaseErrors 覆盖哨兵错误的可区分性。
//
// 三种情形必须能被分别识别，因为它们对应完全不同的处理：
// 「作业不存在」（用户看错 ID）/「租约丢失」（丢弃结果）/
// 「不可执行」（已是终态，静默跳过）。
func TestJobNotFoundAndLeaseErrors(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	if _, err := fixture.jobs.GetJob(ctx, 1<<62); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("不存在的作业必须返回 pgx.ErrNoRows，实际: %v", err)
	}
	if _, err := fixture.jobs.CompleteJob(ctx, 1<<62, "w", 1, nil); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("不存在的作业提交必须返回 ErrJobNotFound，实际: %v", err)
	}
	if _, err := fixture.jobs.FailJob(ctx, 1<<62, "w", 1, model.ErrorClassInternal, "x"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("不存在的作业失败上报必须返回 ErrJobNotFound，实际: %v", err)
	}

	// 别人持有的作业不可被抢占。
	projectID := fixture.projectID
	job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
		ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if _, _, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-A", model.DefaultLeaseDuration); err != nil {
		t.Fatalf("抢占 A: %v", err)
	}
	_, ok, err := fixture.jobs.ClaimJobByID(ctx, job.ID, "worker-B", model.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("抢占 B: %v", err)
	}
	if ok {
		t.Fatal("已被别人持有的作业不得被抢占（否则同一作业被执行两次）")
	}
}

// TestRearmUndeliveredJobOutbox 覆盖验收场景 4 的另一半：「Redis 重启后作业不丢」。
//
// 丢消息的具体形态是「jobs 表说 pending，outbox 表说已经派发过了」。
// 因为 Redis List 是破坏性读取且没有 ack，「投递成功」无法证明「有人拿到」，
// 所以系统必须能按**作业事实**把派发意图重新武装起来。
//
// 本用例同时验证两条不能互相替代的性质：
//   - 已经 terminated（succeeded）的作业**不得**被重新武装（否则会把已完成
//     的作业又唤醒一次，白白花钱）；
//   - 仍是 pending 的作业**必须**被重新武装（否则它永远不会再被执行）。
func TestRearmUndeliveredJobOutbox(t *testing.T) {
	fixture := newJobFixture(t)
	ctx := context.Background()

	projectID := fixture.projectID
	newJob := func() model.Job {
		t.Helper()
		job, _, err := fixture.jobs.EnqueueJob(ctx, EnqueueJobInput{
			ProjectID: &projectID, Kind: model.JobKindBatchGenerate, Payload: map[string]any{"n": 1},
		})
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		return job
	}

	// 场景 A：消息投出去了、但没人拿到（Redis 重启/flushdb）。
	lost := newJob()
	events, err := fixture.jobs.ClaimOutboxBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimOutboxBatch: %v", err)
	}
	var lostEventID int64
	for _, event := range events {
		if event.EventID == "job:"+fmt.Sprint(lost.ID) {
			lostEventID = event.ID
		}
	}
	if lostEventID == 0 {
		t.Fatal("必须能取到该作业的派发事件")
	}
	if err := fixture.jobs.MarkOutboxDispatched(ctx, lostEventID); err != nil {
		t.Fatalf("MarkOutboxDispatched: %v", err)
	}
	// 把 dispatched_at 推回到阈值之前，模拟「投出去很久了但作业仍 pending」。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE outbox SET dispatched_at = NOW() - INTERVAL '10 minutes' WHERE id = $1`, lostEventID); err != nil {
		t.Fatalf("backdate dispatched_at: %v", err)
	}

	// 场景 B：投递本身失败到上限（Redis 长时间不可用），但作业从未执行。
	gaveUp := newJob()
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE outbox SET status = 'failed', attempts = $2, last_error = '连接被拒绝'
    WHERE event_id = $1`, "job:"+fmt.Sprint(gaveUp.ID), model.OutboxMaxAttempts); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	// 场景 C：已经成功的作业，其 dispatched 事件**不得**被重新武装。
	done := newJob()
	claimed, ok, err := fixture.jobs.ClaimJobByID(ctx, done.ID, "worker-A", model.DefaultLeaseDuration)
	if err != nil || !ok {
		t.Fatalf("抢占: ok=%v err=%v", ok, err)
	}
	if _, err := fixture.jobs.CompleteJob(ctx, done.ID, "worker-A", claimed.FencingToken, map[string]any{"ok": true}); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE outbox SET status = 'dispatched', dispatched_at = NOW() - INTERVAL '10 minutes'
    WHERE event_id = $1`, "job:"+fmt.Sprint(done.ID)); err != nil {
		t.Fatalf("backdate done: %v", err)
	}

	rearmed, err := fixture.jobs.RearmUndeliveredJobOutbox(ctx, time.Minute, 100)
	if err != nil {
		t.Fatalf("RearmUndeliveredJobOutbox: %v", err)
	}
	if rearmed != 2 {
		t.Fatalf("恰好两条事件应被重新武装（丢失的+放弃的），实际 %d", rearmed)
	}

	status := func(eventID string) (string, int) {
		t.Helper()
		var state string
		var attempts int
		if err := fixture.pool.QueryRow(ctx, `
      SELECT status, attempts FROM outbox WHERE event_id = $1`, eventID).Scan(&state, &attempts); err != nil {
			t.Fatalf("read outbox %s: %v", eventID, err)
		}
		return state, attempts
	}

	if state, _ := status("job:" + fmt.Sprint(lost.ID)); state != model.OutboxStatusPending {
		t.Fatalf("丢失的派发意图必须回到 pending，实际 %s", state)
	}
	if state, attempts := status("job:" + fmt.Sprint(gaveUp.ID)); state != model.OutboxStatusPending || attempts != 0 {
		t.Fatalf("放弃的派发意图必须回到 pending 且 attempts 归零（否则下一轮立刻再判死），实际 %s/%d", state, attempts)
	}
	if state, _ := status("job:" + fmt.Sprint(done.ID)); state != model.OutboxStatusDispatched {
		t.Fatalf("已成功作业的派发意图不得被重新武装，实际 %s", state)
	}

	// 重新武装后必须**真的**能被 dispatcher 取走（否则只是状态好看）。
	again, err := fixture.jobs.ClaimOutboxBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimOutboxBatch again: %v", err)
	}
	claimable := map[int64]bool{}
	for _, event := range again {
		claimable[event.ID] = true
	}
	if !claimable[lostEventID] {
		t.Fatal("重新武装后的事件必须能被 dispatcher 取走")
	}
}
