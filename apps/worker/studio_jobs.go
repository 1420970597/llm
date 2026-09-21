package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// 本文件是 Atelier 新作业（`studio.*`）的可靠执行侧（Issue #160 T06）。
//
// 契约：docs/plans/atelier-implementation.md §4.1/§6.3；
// docs/plans/atelier-api-contract.md §1.3、§5；迁移 0026 的文件头。
//
// 与旧 `consumeJobs` 的根本区别：
//
//	旧：Redis 消息是权威（`{type, datasetId}`），BRPOP 即消费，
//	    worker 崩溃 → 任务永久消失，而 datasets.status 还停在 *_queued。
//	新：**Postgres 是权威**。消息只带 `{schemaVersion, jobId, kind}`，
//	    worker 拿 jobId 去 DB 抢占租约（CAS + fencing），跑完再提交。
//	    因此丢消息可以从 DB 重投，重复消息会被 CAS 拒绝。
//
// 三个独立循环构成运行时：
//
//	dispatchLoop   outbox → Redis（业务事务与派发意图同生共死）
//	consumeLoop    Redis → 抢占 → 执行 → 提交（带心跳与 fencing）
//	maintainLoop   回收过期租约 + 重新武装未送达的派发意图

// studioQueueSuffix 是 Studio 队列的默认后缀（见 config.WorkerConfig.StudioQueueName）。
const studioQueueSuffix = "-studio"

// studioLeaseHeartbeatDivisor 决定心跳间隔：租约时长 / 该值。
//
// 为什么取 3：容忍**连续两次**心跳丢失（网络抖动、GC 停顿、DB 短暂不可用）后
// 才可能被判过期。取 2 或 1 会让一次抖动就触发「另一个 worker 抢走租约、
// 同一个作业被执行两遍」——那正是租约机制要避免的结果。
const studioLeaseHeartbeatDivisor = 3

// studioMaintenanceInterval 是维护循环的周期（回收过期租约 + 重新武装派发）。
//
// 30 秒的依据：它同时是「用户可接受的停滞」与「不会让 dispatcher 忙等」的折中。
// 比租约（5 分钟）小得多，因此一个心跳停止的 worker 留下的作业
// 最多在租约到期后 30 秒内被回收。
const studioMaintenanceInterval = 30 * time.Second

// studioStalledDispatchAfter 是「投递出去多久还没被执行」才重新派发的阈值。
//
// 必须**显著大于**一次正常投递到抢占的耗时（毫秒级），又要小于用户的耐心。
// 取 60 秒：Redis 重启/丢消息的场景下，作业最多晚 60 秒被重新唤醒，
// 而正常路径永远不会触发它。
const studioStalledDispatchAfter = 60 * time.Second

// StudioJobHandler 处理一个已抢占的作业。
//
// 返回 (result, err)：result 是写回 jobs.payload 的**结果摘要**
// （例如 batchId、产出样本数），供 API 读模型直接使用；
// 它必须是可 JSON 序列化的，且**不含密钥**（payload 会进日志与读模型）。
//
// 实现约束（T12/T14/T21 的 handler 必须遵守）：
//   - 必须尊重 ctx：心跳丢失时 ctx 会被取消，此时应尽快返回；
//   - 必须幂等：同一个 jobID 可能被抢占两次（租约过期后重投），
//     第二次的提交会被 fencing 拒绝，但**副作用**已经发生过了；
//   - 不允许自行改 jobs 状态：提交走 CompleteJob/FailJob 以带 fencing token。
type StudioJobHandler func(ctx context.Context, env *StudioJobEnv, job model.Job) (any, error)

// StudioJobEnv 是 Studio job handler 可用的依赖。
//
// 与 jobContext 分开：旧 handler 的依赖是 dataset 中心的（datasets/prompts/...），
// 新 handler 需要的是项目/批次/预算/评估等 store（T07 起逐步接入）。
// 混在一起会让「这个 handler 属于哪一轮实现」需要靠读代码判断。
type StudioJobEnv struct {
	Pool   *pgxpool.Pool
	Jobs   *store.JobStore
	Redis  *redis.Client
	Queue  string
	Owner  string
	extras map[string]any
}

// SetExtra 让后续任务（T12/T14/T21）挂载自己的 store，而无需改本文件签名。
func (env *StudioJobEnv) SetExtra(key string, value any) {
	if env.extras == nil {
		env.extras = map[string]any{}
	}
	env.extras[key] = value
}

// Extra 读取挂载的依赖。
func (env *StudioJobEnv) Extra(key string) (any, bool) {
	value, ok := env.extras[key]
	return value, ok
}

var (
	studioJobMu       sync.RWMutex
	studioJobHandlers = map[string]StudioJobHandler{}
)

// RegisterStudioJobHandler 注册一个 Studio 作业处理器。
//
// 重复注册同一 kind 会 panic：两个 handler 静默覆盖彼此会让
// 「这个作业到底由谁执行」变成不可知，而那种问题只在生产上以
// 「行为随机变化」的形态暴露。
func RegisterStudioJobHandler(kind string, handler StudioJobHandler) {
	if kind == "" || handler == nil {
		return
	}
	studioJobMu.Lock()
	defer studioJobMu.Unlock()
	if _, exists := studioJobHandlers[kind]; exists {
		panic("duplicate studio job handler: " + kind)
	}
	studioJobHandlers[kind] = handler
}

// lookupStudioJobHandler 查找已注册的处理器。
func lookupStudioJobHandler(kind string) (StudioJobHandler, bool) {
	studioJobMu.RLock()
	defer studioJobMu.RUnlock()
	handler, ok := studioJobHandlers[kind]
	return handler, ok
}

// studioQueueName 解析 Studio 队列名。
//
// 把「缺省后缀」放在函数里而不是只在 config 里：测试与其它入口
// （例如将来的一次性重投工具）需要与 worker 用同一个规则，
// 两处各写一遍迟早会漂移成「工具投递的队列没人消费」。
func studioQueueName(cfg config.WorkerConfig) string {
	name := strings.TrimSpace(cfg.StudioQueueName)
	if name != "" {
		return name
	}
	base := strings.TrimSpace(cfg.QueueName)
	if base == "" {
		base = "dataset-generation"
	}
	return base + studioQueueSuffix
}

// studioWorkerOwner 生成本进程的租约持有者标识。
//
// 必须**每个进程唯一**：两个进程用同一个 owner 名会让 fencing 失效
// （token 匹配 + owner 匹配就通过了），于是「过期 worker 迟到提交」会被放行。
// 用 hostname:pid:纳秒时间戳，既有可读性（日志里能定位到容器与进程）又唯一。
func studioWorkerOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d:%d", host, os.Getpid(), time.Now().UnixNano())
}

// studioRuntime 是 Studio 执行侧的运行时状态。
type studioRuntime struct {
	env         *StudioJobEnv
	legacyQueue string
	lease       time.Duration
	concurrency int
}

// routingStudioQueue 保存本进程的 Studio 队列名，供**旧消费者**回投
// 误落到旧队列的新格式消息（见 routeLegacyQueueMessage）。
//
// 为什么用包级变量而不是把它塞进 jobContext：jobContext 是冻结契约的一部分
// （docs/plans/eval-and-cleaning-plan.md 第 1 节），为一条只读的路由名改它的
// 签名会让所有 lane 的 handler 重新编译。它在 main 里、启动任何 goroutine
// **之前**赋值，因此不存在并发读写。
var routingStudioQueue string

// startStudioRuntime 启动 Studio 的三个循环并返回运行时（供测试与关停）。
//
// 传 cfg 而不是复用 jobContext 的字段：Studio 侧的队列名与并发度是
// 独立配置项（见 WorkerConfig 的说明），从 jc 里猜会让「这两条队列
// 是否相同」需要跨文件推断。
func startStudioRuntime(ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client, cfg config.WorkerConfig) *studioRuntime {
	concurrency := cfg.StudioConcurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	if concurrency > 8 {
		// 上限 8：再高也不会更快（真正的并行度在批次内部的 concurrency），
		// 却会让「同时在途几个批次」不可解释。
		concurrency = 8
	}

	runtime := &studioRuntime{
		env: &StudioJobEnv{
			Pool:  pool,
			Jobs:  store.NewJobStore(pool),
			Redis: redisClient,
			Queue: studioQueueName(cfg),
			Owner: studioWorkerOwner(),
		},
		legacyQueue: cfg.QueueName,
		lease:       model.DefaultLeaseDuration,
		concurrency: concurrency,
	}

	// 在启动 goroutine 之前赋值：旧消费者读它时已经是稳定值。
	routingStudioQueue = runtime.env.Queue

	go runtime.dispatchLoop(ctx)
	go runtime.consumeLoop(ctx)
	go runtime.maintainLoop(ctx)

	log.Printf("studio runtime started queue=%s legacy_queue=%s owner=%s concurrency=%d",
		runtime.env.Queue, runtime.legacyQueue, runtime.env.Owner, runtime.concurrency)
	return runtime
}

// ---------------------------------------------------------------------------
// 派发：outbox → Redis
// ---------------------------------------------------------------------------

// dispatchLoop 把 outbox 里待派发的事件投递到 Redis。
func (rt *studioRuntime) dispatchLoop(ctx context.Context) {
	runPeriodic(ctx, time.Second, rt.dispatchOnce)
}

// runPeriodic 周期执行 fn，并在 ctx 取消时立刻返回。
//
// 抽出来的原因：三个循环的「先跑一次、再按 ticker 跑、ctx 取消就退出」是
// 同一件事，写三遍只会让「其中一个忘了看 ctx」这种错误有发生的空间 ——
// 而忘了看 ctx 的循环在优雅关停时会继续持有租约（用户看到停滞）。
// 抽成纯函数后，这段行为本身可以在不连 Redis/DB 的情况下被测试。
func runPeriodic(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	// 启动时立刻跑一轮：重启后尽快处理积压，而不是等第一个 tick。
	if ctx.Err() == nil {
		fn(ctx)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// dispatchOnce 处理一批待派发事件。
func (rt *studioRuntime) dispatchOnce(ctx context.Context) {
	events, err := rt.env.Jobs.ClaimOutboxBatch(ctx, 50)
	if err != nil {
		log.Printf("studio.dispatch.claim_failed err=%v", err)
		return
	}
	for _, event := range events {
		if err := rt.dispatchEvent(ctx, event); err != nil {
			// 派发失败**不**丢事件：MarkOutboxFailed 会按 attempts 决定
			// 回到 pending（带退避）还是标记 failed（等重新武装）。
			if markErr := rt.env.Jobs.MarkOutboxFailed(ctx, event.ID, err.Error()); markErr != nil {
				log.Printf("studio.dispatch.mark_failed_failed event=%s err=%v", event.EventID, markErr)
			}
			log.Printf("studio.dispatch.failed event=%s attempts=%d err=%v", event.EventID, event.Attempts, err)
			continue
		}
		if err := rt.env.Jobs.MarkOutboxDispatched(ctx, event.ID); err != nil {
			log.Printf("studio.dispatch.mark_dispatched_failed event=%s err=%v", event.EventID, err)
		}
	}
}

// dispatchEvent 投递单条事件。
//
// 返回 nil 表示「这条事件已经不需要派发了」（作业已完成/已被别人持有），
// 调用方据此标记 dispatched —— 这不是丢失，而是把已经过期的派发意图收口。
func (rt *studioRuntime) dispatchEvent(ctx context.Context, event model.OutboxEvent) error {
	jobID, err := outboxJobID(event)
	if err != nil {
		return err
	}
	if jobID == 0 {
		// 不是作业派发事件（outbox 是通用日志，将来会有非作业事件）。
		return nil
	}

	job, err := rt.env.Jobs.GetJob(ctx, jobID)
	if err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			// 作业被删（例如项目归档级联）→ 派发意图作废。
			return nil
		}
		return fmt.Errorf("读取作业 %d 失败：%w", jobID, err)
	}
	if job.Status != model.JobStatusPending {
		// 已被抢占/已终态：消息已经在路上或已经不需要了。
		return nil
	}

	// 没有处理器就**不要**投递：投出去只会被抢占后立刻失败，
	// 而作业状态会从「等待兼容 worker」变成「failed」，掩盖真实原因。
	// 保持 pending + 记录原因，符合 T33「先部署读得懂新任务的 worker」。
	if _, ok := lookupStudioJobHandler(job.Kind); !ok {
		return fmt.Errorf("本 worker 未注册作业类型 %s 的处理器（等待兼容版本 worker 部署）", job.Kind)
	}

	envelope, err := model.EncodeJobEnvelope(job.ID, job.Kind)
	if err != nil {
		return err
	}
	if err := rt.env.Redis.LPush(ctx, rt.env.Queue, envelope).Err(); err != nil {
		return fmt.Errorf("投递到队列 %s 失败：%w", rt.env.Queue, err)
	}
	return nil
}

// outboxJobID 从事件载荷里取出作业 ID。
//
// 契约 §5：消息与事件载荷只放对象 ID，不放样本正文。
// 因此这里解析失败**不是**可忽略的小问题 —— 它意味着有一条派发意图
// 永远不会被兑现，必须报错（进而被记录为 last_error）而不是静默跳过。
func outboxJobID(event model.OutboxEvent) (int64, error) {
	if len(event.Payload) == 0 {
		return 0, nil
	}
	var payload struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return 0, fmt.Errorf("outbox 事件 %s 载荷无法解析：%w", event.EventID, err)
	}
	if payload.JobID < 0 {
		return 0, fmt.Errorf("outbox 事件 %s 的 jobId 非法：%d", event.EventID, payload.JobID)
	}
	return payload.JobID, nil
}

// ---------------------------------------------------------------------------
// 消费：Redis → 抢占 → 执行 → 提交
// ---------------------------------------------------------------------------

// consumeLoop 从 Studio 队列取消息并执行。
func (rt *studioRuntime) consumeLoop(ctx context.Context) {
	// 信号量限制同时执行的长作业数。取**执行**而不是「取出消息」为界：
	// 取出后立刻等待信号量会让 BRPOP 阻塞，消息留在本地内存里
	//（进程被杀即丢），那正是旧实现的形态。
	sem := make(chan struct{}, rt.concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		result, err := rt.env.Redis.BRPop(ctx, 5*time.Second, rt.env.Queue).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			log.Printf("studio.consume.brpop_failed err=%v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(result) != 2 {
			continue
		}

		raw := []byte(result[1])

		// 旧格式落到新队列（运维误投/旧工具）：回投旧队列而不是在这里解析。
		// 反过来，新格式落到旧队列时旧消费者也会回投（见 routeLegacyQueueMessage）。
		if model.IsLegacyJobPayload(raw) {
			log.Printf("studio.consume.legacy_payload_requeued queue=%s", rt.env.Queue)
			if err := rt.env.Redis.LPush(ctx, rt.legacyQueue, raw).Err(); err != nil {
				log.Printf("studio.consume.requeue_legacy_failed err=%v", err)
			}
			continue
		}

		envelope, err := model.DecodeJobEnvelope(raw)
		if err != nil {
			// schema 版本不兼容属于**部署问题**，必须显式告警而不是尽力解析。
			// 但不丢弃：BRPOP 是破坏性读取，丢掉等于让它永久消失；
			// 送入死信队列后它仍可被检查、修复版本后重投。
			log.Printf("studio.consume.envelope_rejected raw=%s err=%v", truncateForLog(string(raw), 200), err)
			rt.deadLetter(ctx, raw, err)
			continue
		}

		job, claimed, err := rt.env.Jobs.ClaimJobByID(ctx, envelope.JobID, rt.env.Owner, rt.lease)
		if err != nil {
			log.Printf("studio.consume.claim_failed job=%d err=%v", envelope.JobID, err)
			continue
		}
		if !claimed {
			// 重复消息，或已被别的 worker 抢占，或作业已终态/已达尝试上限。
			// 这是**正常**现象，不是错误：派发是至少一次的。
			log.Printf("studio.consume.skip job=%d reason=not_claimable", envelope.JobID)
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(job model.Job) {
			defer wg.Done()
			defer func() { <-sem }()
			rt.execute(ctx, job)
		}(job)
	}
}

// deadLetter 把无法处理的消息送入死信队列。
//
// 为什么不能只打日志：BRPOP 已经把它从队列里**破坏性**取走了，
// 丢掉之后运维无法检查「到底是什么消息让 worker 拒绝」——
// 而那正是排查「部署了不兼容版本」的唯一线索。
// 死信队列刻意不做自动重投：这类消息需要先修复版本/数据，
// 自动重投只会把它变成一轮又一轮同样的拒绝日志。
func (rt *studioRuntime) deadLetter(ctx context.Context, raw []byte, reason error) {
	queue := rt.env.Queue + "-rejected"
	if err := rt.env.Redis.LPush(ctx, queue, raw).Err(); err != nil {
		log.Printf("studio.consume.dead_letter_failed queue=%s err=%v", queue, err)
		return
	}
	log.Printf("studio.consume.dead_lettered queue=%s reason=%v", queue, reason)
}

// execute 执行一个已抢占的作业，并带心跳与 fencing 提交。
func (rt *studioRuntime) execute(ctx context.Context, job model.Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	heartbeatDone := make(chan struct{})
	go rt.heartbeat(jobCtx, cancel, job, heartbeatDone)
	defer close(heartbeatDone)

	handler, ok := lookupStudioJobHandler(job.Kind)
	if !ok {
		// 抢占后才发现没有处理器（例如两种 worker 版本并存、dispatcher 是新版
		// 而执行进程是旧版）。这是**配置问题**，重试无用，因此用不可重试的类别。
		rt.reportFailure(ctx, job, model.ErrorClassConfig,
			fmt.Sprintf("本 worker 未注册作业类型 %s 的处理器", job.Kind))
		return
	}

	started := time.Now()
	result, err := handler(jobCtx, rt.env, job)
	if err == nil {
		completed, completeErr := rt.env.Jobs.CompleteJob(ctx, job.ID, rt.env.Owner, job.FencingToken, result)
		if completeErr != nil {
			if errors.Is(completeErr, store.ErrJobLeaseLost) {
				// 租约已被接管（或作业已被取消）：我们的结果被**有意丢弃**。
				// 不能当成失败上报（那会覆盖接管者的状态），只能留日志。
				log.Printf("studio.job.fenced job=%d kind=%s owner=%s", job.ID, job.Kind, rt.env.Owner)
				return
			}
			log.Printf("studio.job.complete_failed job=%d kind=%s err=%v", job.ID, job.Kind, completeErr)
			return
		}
		if !completed {
			// 已是终态（重复提交）：幂等成功，不是错误。
			log.Printf("studio.job.complete_replayed job=%d kind=%s", job.ID, job.Kind)
			return
		}
		log.Printf("studio.job.succeeded job=%d kind=%s attempt=%d duration_ms=%d",
			job.ID, job.Kind, job.Attempt, time.Since(started).Milliseconds())
		return
	}

	// 心跳丢失导致的取消：结果不再有效，也不该上报失败（接管者负责善后）。
	if jobCtx.Err() != nil && ctx.Err() == nil {
		log.Printf("studio.job.cancelled_after_lease_loss job=%d kind=%s err=%v", job.ID, job.Kind, err)
		return
	}

	rt.reportFailure(ctx, job, classifyStudioJobError(err), err.Error())
}

// reportFailure 上报失败并记录「是否会再被执行」。
func (rt *studioRuntime) reportFailure(ctx context.Context, job model.Job, errorClass, message string) {
	requeued, err := rt.env.Jobs.FailJob(ctx, job.ID, rt.env.Owner, job.FencingToken, errorClass, message)
	if err != nil {
		if errors.Is(err, store.ErrJobLeaseLost) {
			log.Printf("studio.job.failure_fenced job=%d kind=%s", job.ID, job.Kind)
			return
		}
		log.Printf("studio.job.fail_report_failed job=%d kind=%s err=%v", job.ID, job.Kind, err)
		return
	}
	log.Printf("studio.job.failed job=%d kind=%s attempt=%d class=%s requeued=%v message=%s",
		job.ID, job.Kind, job.Attempt, errorClass, requeued, truncateForLog(message, 300))
}

// heartbeat 周期性续租，并在续租失败时取消作业上下文。
//
// 为什么必须在**失败时取消**而不是继续等：心跳失败意味着另一个 worker
// 已经（或即将）拿到租约。此时继续跑下去的成果一定会被 fencing 拒绝，
// 继续跑只是浪费预算（外部模型调用是要钱的）。
// 取消让 ctx 感知的 handler 尽快停下。
func (rt *studioRuntime) heartbeat(ctx context.Context, cancel context.CancelFunc, job model.Job, done <-chan struct{}) {
	interval := rt.lease / studioLeaseHeartbeatDivisor
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := rt.env.Jobs.HeartbeatJob(ctx, job.ID, rt.env.Owner, job.FencingToken, rt.lease)
			if err != nil {
				// 数据库暂时不可用不代表租约丢了：不取消，下一轮再试。
				// 取消需要**证据**（明确的 token 不匹配），否则一次网络抖动
				// 就会把正常作业腰斩。
				log.Printf("studio.job.heartbeat_failed job=%d err=%v", job.ID, err)
				continue
			}
			if !ok {
				log.Printf("studio.job.lease_lost job=%d kind=%s owner=%s", job.ID, job.Kind, rt.env.Owner)
				cancel()
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 维护：回收过期租约 + 重新武装未送达的派发
// ---------------------------------------------------------------------------

// maintainLoop 周期性地把「卡住的」作业重新拉回可执行状态。
func (rt *studioRuntime) maintainLoop(ctx context.Context) {
	runPeriodic(ctx, studioMaintenanceInterval, rt.maintainOnce)
}

// maintainOnce 执行一轮维护。
func (rt *studioRuntime) maintainOnce(ctx context.Context) {
	reclaimed, err := rt.env.Jobs.ReclaimExpiredJobs(ctx, 100)
	if err != nil {
		log.Printf("studio.maintain.reclaim_failed err=%v", err)
	} else if len(reclaimed) > 0 {
		// 回收本身会写入新的 outbox 事件（见 job_store.ReclaimExpiredJobs），
		// 因此这里只需记录数量，派发由 dispatchLoop 负责。
		log.Printf("studio.maintain.reclaimed count=%d jobs=%v", len(reclaimed), reclaimed)
	}

	rearmed, err := rt.env.Jobs.RearmUndeliveredJobOutbox(ctx, studioStalledDispatchAfter, 200)
	if err != nil {
		log.Printf("studio.maintain.rearm_failed err=%v", err)
	} else if rearmed > 0 {
		log.Printf("studio.maintain.rearmed count=%d reason=undelivered_dispatch", rearmed)
	}
}

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

// classifyStudioJobError 把错误映射到契约的错误类别。
//
// 与 model.IsRetryableJobError 配合决定是否自动重试，因此分类必须保守：
// 把「配置错误」误判成「限流」会让作业无限重试并持续产生费用；
// 把「限流」误判成「配置错误」只会让人多点一次恢复。
// 因此这里只在有**明确证据**时给出可重试类别，其余一律归入 internal（可重试但有人工介入路径）。
func classifyStudioJobError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.ErrorClassTimeout
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "429"), strings.Contains(message, "rate limit"),
		strings.Contains(message, "too many requests"):
		return model.ErrorClassRateLimited
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline exceeded"),
		strings.Contains(message, "timed out"):
		return model.ErrorClassTimeout
	case strings.Contains(message, "502"), strings.Contains(message, "503"),
		strings.Contains(message, "504"), strings.Contains(message, "connection reset"),
		strings.Contains(message, "unexpected eof"):
		return model.ErrorClassProvider
	default:
		return model.ErrorClassInternal
	}
}

// routeLegacyQueueMessage 判断旧队列里的一条消息应如何处理。
//
// 返回值 (forward, ok)：
//   - ok=false → 不是本函数认识的消息，调用方按原逻辑处理；
//   - forward=true → 这是**新格式**消息，必须回投 Studio 队列，旧消费者不得执行它。
//
// 为什么需要它（T06 验收项原文「旧 `{type,datasetId}` 消费者不得误吞新消息」）：
// 旧消费者按 `job.Type` switch，新格式的 `type` 为空 → 落进 `default:` 分支
// 打印一行「worker ignored job type=」然后**丢掉消息**。那不只是「误吞」，
// 而是让一个已经落库的作业永久停在 pending。因此旧消费者必须能识别
// 并回投它，而不是忽略。
func routeLegacyQueueMessage(raw []byte, studioQueue string) (forward bool, target string) {
	if len(raw) == 0 || model.IsLegacyJobPayload(raw) {
		return false, ""
	}
	// 只要带**正数 jobId**，它就属于 Studio 主线 —— 即使本进程读不懂它的
	// schema 版本。读不懂时也必须转发：留在旧队列只会落进 `default:` 分支
	// 被静默丢掉；转发后 Studio 消费者会明确拒绝（日志点名 schema 版本）
	// 并送入死信队列，让「该部署兼容 worker 了」这件事可见。
	jobID, ok := model.ProbeJobEnvelopeJobID(raw)
	if !ok || jobID <= 0 {
		return false, ""
	}
	return true, studioQueue
}

// truncateForLog 截断长文本，避免日志被单条消息淹没。
func truncateForLog(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
