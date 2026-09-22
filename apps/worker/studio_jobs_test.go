package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 Issue #160 T06 执行侧中**不需要 Redis** 的那部分逻辑。
//
// 刻意不在这里起 Redis：抢占/租约/fencing 的语义全部在 internal/store 的
// 真实 Postgres 测试里（job_store_test.go），重复测一遍只会得到「两个地方
// 都要改」的维护成本。这里只覆盖 worker 自己的判定逻辑：
// 消息路由、队列命名、错误分类。这三处出错都不会被 store 测试发现，
// 而它们的后果都很重（丢消息 / 两条队列撞车 / 把限流误判成配置错误）。

// TestRouteLegacyQueueMessageRoutesOnlyNewFormat 覆盖 T06 验收项
// 「旧 `{type,datasetId}` 消费者不得误吞新消息」。
//
// 为什么这条测试重要：旧消费者按 `job.Type` switch，新格式消息的 type 为空
// 会落进 default 分支打印一行日志然后**丢掉消息**。而那个作业已经落在
// jobs 表里、outbox 也标了 dispatched，于是它会永久停在 pending ——
// 用户看到「排队中」但永远不会有人处理它。
func TestRouteLegacyQueueMessageRoutesOnlyNewFormat(t *testing.T) {
	const studioQueue = "dataset-generation-studio"

	cases := []struct {
		name        string
		raw         string
		wantForward bool
	}{
		{
			name:        "旧格式必须由旧消费者处理",
			raw:         `{"type":"sft.generate","datasetId":42}`,
			wantForward: false,
		},
		{
			name:        "旧格式（带重试计数）同样不转发",
			raw:         `{"type":"export.generate","datasetId":7,"retry":1}`,
			wantForward: false,
		},
		{
			name:        "新格式必须转发到 Studio 队列",
			raw:         `{"schemaVersion":1,"jobId":9,"kind":"studio.batch.generate"}`,
			wantForward: true,
		},
		{
			name:        "缺 schemaVersion 但带 jobId 的消息也必须转发（本 worker 会拒绝并送死信）",
			raw:         `{"jobId":10}`,
			wantForward: true,
		},
		{
			name:        "未来 schema 版本也必须转发（不能让旧消费者静默丢弃）",
			raw:         `{"schemaVersion":99,"jobId":12,"kind":"studio.batch.generate"}`,
			wantForward: true,
		},
		{
			name:        "两种字段都有的消息按 Studio 侧处理（带 jobId 就不再是旧格式）",
			raw:         `{"jobId":11,"type":"legacy","datasetId":3}`,
			wantForward: true,
		},
		{
			name:        "完全无法解析的内容不转发（留给原逻辑记录错误）",
			raw:         `not json at all`,
			wantForward: false,
		},
		{
			name:        "空消息不转发",
			raw:         ``,
			wantForward: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			forward, target := routeLegacyQueueMessage([]byte(testCase.raw), studioQueue)
			if forward != testCase.wantForward {
				t.Fatalf("forward 应为 %v，实际 %v", testCase.wantForward, forward)
			}
			if forward && target != studioQueue {
				t.Fatalf("转发目标必须是 Studio 队列 %q，实际 %q", studioQueue, target)
			}
		})
	}
}

// TestRoutingCoversEveryStudioShapedMessage 锁定「新旧两个消费者都不会丢消息」
// 的真实边界。
//
// 两个判定函数**不是**严格互补的，这一点必须测清楚：
//   - 带正数 jobId 的消息一律属于 Studio 主线（包括未来 schema 版本 ——
//     本 worker 读不懂它，但也不能让旧消费者当垃圾丢弃）；
//   - 既无 type/datasetId、又无 jobId 的内容（真正的垃圾）两边都不处理，
//     由原逻辑记错误日志，这是正确的：它不代表任何已落库的作业。
func TestRoutingCoversEveryStudioShapedMessage(t *testing.T) {
	const studioQueue = "q-studio"

	studioShaped := []string{
		`{"schemaVersion":1,"jobId":2,"kind":"studio.release.build"}`,
		// 未来版本：本 worker 拒绝执行，但必须路由到 Studio 队列，
		// 否则旧消费者会把它归入 default 分支静默丢弃。
		`{"schemaVersion":99,"jobId":4,"kind":"studio.batch.generate"}`,
		// 缺 schemaVersion 的畸形消息同样属于 Studio 侧（它带 jobId）。
		`{"jobId":6,"kind":"studio.batch.generate"}`,
	}
	for _, raw := range studioShaped {
		if model.IsLegacyJobPayload([]byte(raw)) {
			t.Fatalf("带 jobId 的消息不得被归为旧格式：%s", raw)
		}
		forward, target := routeLegacyQueueMessage([]byte(raw), studioQueue)
		if !forward || target != studioQueue {
			t.Fatalf("带 jobId 的消息必须转发到 Studio 队列：%s（forward=%v target=%q）", raw, forward, target)
		}
	}

	legacyShaped := []string{
		`{"type":"sft.generate","datasetId":1}`,
		`{"type":"export.generate","datasetId":7,"retry":2}`,
	}
	for _, raw := range legacyShaped {
		if !model.IsLegacyJobPayload([]byte(raw)) {
			t.Fatalf("旧格式消息必须被识别：%s", raw)
		}
		if forward, _ := routeLegacyQueueMessage([]byte(raw), studioQueue); forward {
			t.Fatalf("旧格式消息不得被转发到 Studio 队列：%s", raw)
		}
	}

	// 真正的垃圾：两边都不接，由原逻辑记错误日志。
	for _, raw := range []string{`not json`, `{"unrelated":true}`, `{}`} {
		if forward, _ := routeLegacyQueueMessage([]byte(raw), studioQueue); forward {
			t.Fatalf("无法识别的消息不得被转发：%s", raw)
		}
		if model.IsLegacyJobPayload([]byte(raw)) {
			t.Fatalf("无法识别的消息不得被归为旧格式：%s", raw)
		}
	}
}

// TestDecodeJobEnvelopeRejectsIncompatibleSchema 覆盖 T33 要求的
// 「API/Worker job schema 兼容版本在部署前检查」。
//
// 拒绝而不是尽力解析：一个未来版本的载荷可能语义不同，误解析会产生
// 错误的业务动作；而「拒绝并留日志」最多是延迟处理。
func TestDecodeJobEnvelopeRejectsIncompatibleSchema(t *testing.T) {
	future, err := json.Marshal(map[string]any{
		"schemaVersion": model.JobEnvelopeSchemaVersion + 1,
		"jobId":         5,
		"kind":          "studio.batch.generate",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := model.DecodeJobEnvelope(future); err == nil {
		t.Fatal("更高 schema 版本的消息必须被拒绝，而不是尽力解析")
	}

	valid, err := model.EncodeJobEnvelope(5, "studio.batch.generate")
	if err != nil {
		t.Fatalf("EncodeJobEnvelope: %v", err)
	}
	envelope, err := model.DecodeJobEnvelope(valid)
	if err != nil {
		t.Fatalf("当前版本的消息必须能解析：%v", err)
	}
	if envelope.JobID != 5 || envelope.Kind != "studio.batch.generate" {
		t.Fatalf("解析结果错位：%+v", envelope)
	}
}

// TestStudioQueueNameIsDistinctFromLegacyQueue 覆盖 T06 的结构性要求：
// 新旧队列**必须**不同名。
//
// 如果两者同名，新消息会被旧消费者按 `{type,datasetId}` 解析失败后丢弃
// （见 routeLegacyQueueMessage 的说明），而这是配置一处笔误就能造成的、
// 且只在生产上以「任务一直排队」的形态暴露的故障。因此这里显式断言。
func TestStudioQueueNameIsDistinctFromLegacyQueue(t *testing.T) {
	cfg := config.WorkerConfig{
		QueueName:         "dataset-generation",
		StudioQueueName:   "dataset-generation-studio",
		StudioConcurrency: 2,
	}
	if studioQueueName(cfg) == cfg.QueueName {
		t.Fatal("Studio 队列不得与旧队列同名")
	}
	if got := studioQueueName(cfg); got != "dataset-generation-studio" {
		t.Fatalf("Studio 队列名错误：%q", got)
	}

	// 未显式配置时按后缀派生（一个只配 WORKER_QUEUE_NAME 的部署也要正确）。
	derived := studioQueueName(config.WorkerConfig{QueueName: "queues-main"})
	if derived != "queues-main"+studioQueueSuffix {
		t.Fatalf("派生队列名错误：%q", derived)
	}
}

// TestStudioWorkerOwnerIsAtLeastUniquePerCall 覆盖 fencing 的前提。
//
// fencing 靠 (owner, fencing_token) 两段判定。若两个进程拿到相同的 owner，
// 那么「过期 worker 迟到提交」会被放行（owner 匹配 + token 匹配），
// 于是同一作业的两次执行结果都可能写进权威状态。因此 owner 必须唯一。
func TestStudioWorkerOwnerIsAtLeastUniquePerCall(t *testing.T) {
	first := studioWorkerOwner()
	second := studioWorkerOwner()
	if first == second {
		t.Fatalf("两次生成必须不同：%q", first)
	}
	if first == "" {
		t.Fatal("owner 不得为空（空 owner 会被 JobStore 拒绝）")
	}
}

// TestClassifyStudioJobError 锁定错误分类的保守性。
//
// 分类直接决定 model.IsRetryableJobError，也就是「要不要自动再花钱跑一次」。
// 因此有明确证据（429/超时/供应商错误）才给可重试类别，其余归 internal。
func TestClassifyStudioJobError(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		want  string
		retry bool
	}{
		{"限流", errors.New("HTTP 429 Too Many Requests"), model.ErrorClassRateLimited, true},
		{"限流（长文案）", errors.New("provider said: rate limit exceeded"), model.ErrorClassRateLimited, true},
		{"上下文超时", context.DeadlineExceeded, model.ErrorClassTimeout, true},
		{"文本超时", errors.New("request timed out after 300s"), model.ErrorClassTimeout, true},
		{"供应商 503", errors.New("upstream returned 503"), model.ErrorClassProvider, true},
		{"连接重置", errors.New("connection reset by peer"), model.ErrorClassProvider, true},
		{"未知错误归内部", errors.New("something odd"), model.ErrorClassInternal, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifyStudioJobError(testCase.err)
			if got != testCase.want {
				t.Fatalf("分类应为 %s，实际 %s", testCase.want, got)
			}
			if retry := model.IsRetryableJobError(got); retry != testCase.retry {
				t.Fatalf("重试判定应为 %v，实际 %v", testCase.retry, retry)
			}
		})
	}
	if class := classifyStudioJobError(nil); class != "" {
		t.Fatalf("nil 错误必须分类为空，实际 %q", class)
	}
}

// TestRegisterStudioJobHandlerRejectsDuplicate 覆盖「两个 handler 静默覆盖彼此」
// 的防护。重复注册会 panic，这是**有意**的：静默覆盖只在生产上以
// 「行为随机变化」的形态暴露，而在启动时崩溃是可以立刻发现的。
func TestRegisterStudioJobHandlerRejectsDuplicate(t *testing.T) {
	const kind = "studio.test.duplicate"
	RegisterStudioJobHandler(kind, func(context.Context, *StudioJobEnv, model.Job) (any, error) {
		return nil, nil
	})
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("重复注册必须 panic")
		}
		studioJobMu.Lock()
		delete(studioJobHandlers, kind)
		studioJobMu.Unlock()
	}()
	RegisterStudioJobHandler(kind, func(context.Context, *StudioJobEnv, model.Job) (any, error) {
		return nil, nil
	})
}

// TestOutboxJobIDRejectsMalformedPayload 覆盖「解析失败必须报错」。
//
// outbox 是通用派发日志，非作业事件的载荷里没有 jobId（返回 0 是正确的）。
// 但**有 jobId 字段却无法解析**意味着一条派发意图永远不会被兑现，
// 必须报错让它进入 last_error 可见，而不是静默当成功。
func TestOutboxJobIDRejectsMalformedPayload(t *testing.T) {
	if id, err := outboxJobID(model.OutboxEvent{EventID: "e1", Payload: nil}); err != nil || id != 0 {
		t.Fatalf("无载荷事件应返回 0 且无错，实际 id=%d err=%v", id, err)
	}
	if id, err := outboxJobID(model.OutboxEvent{EventID: "e2", Payload: json.RawMessage(`{"other":1}`)}); err != nil || id != 0 {
		t.Fatalf("非作业事件应返回 0 且无错，实际 id=%d err=%v", id, err)
	}
	if id, err := outboxJobID(model.OutboxEvent{EventID: "e3", Payload: json.RawMessage(`{"jobId":12}`)}); err != nil || id != 12 {
		t.Fatalf("作业事件应解析出 12，实际 id=%d err=%v", id, err)
	}
	if _, err := outboxJobID(model.OutboxEvent{EventID: "e4", Payload: json.RawMessage(`{`)}); err == nil {
		t.Fatal("坏载荷必须报错（否则这条派发意图永远不会被兑现也不可见）")
	}
	if _, err := outboxJobID(model.OutboxEvent{EventID: "e5", Payload: json.RawMessage(`{"jobId":-3}`)}); err == nil {
		t.Fatal("负数 jobId 必须报错")
	}
}

// TestRunPeriodicStopsOnContextCancel 验证三个循环共用的周期性驱动器
// 能随 ctx 取消而退出。
//
// 为什么要在**不连 Redis/DB** 的情况下测这一条：worker 优雅关停时，
// 忘了看 ctx 的循环会继续持有租约（用户看到的是停滞 —— 要等租约过期
// 才被回收）。把驱动逻辑抽成纯函数正是为了让这条行为可以直接被测。
func TestRunPeriodicStopsOnContextCancel(t *testing.T) {
	t.Run("启动前已取消则立即返回且不执行", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		runs := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			runPeriodic(ctx, time.Millisecond, func(context.Context) { runs++ })
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("runPeriodic 必须随 ctx 取消而退出")
		}
		if runs != 0 {
			t.Fatalf("ctx 已取消时不得执行 fn，实际执行 %d 次", runs)
		}
	})

	t.Run("运行中取消则停止周期执行", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ticks := make(chan struct{}, 32)
		done := make(chan struct{})
		go func() {
			defer close(done)
			runPeriodic(ctx, time.Millisecond, func(context.Context) {
				select {
				case ticks <- struct{}{}:
				default:
				}
			})
		}()

		select {
		case <-ticks:
		case <-time.After(2 * time.Second):
			t.Fatal("runPeriodic 必须先立刻跑一轮")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("取消后 runPeriodic 必须退出")
		}
	})
}
