package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 本文件定义 Atelier 可靠作业对象（Issue #160 T06）。
//
// 契约：docs/plans/atelier-implementation.md §4.1；#160 §1 的 worker 行；
// docs/plans/atelier-api-contract.md §1.3、§5。
//
// 模型的核心主张：**Postgres 是权威状态，Redis 只是通知**。
//   * 作业的存在、尝试、租约、终态都在 jobs 表；
//   * Redis 消息只携带 schemaVersion + jobId（不携带业务载荷），
//     因此丢消息可以从 DB 重投，而不会因载荷与 DB 状态不一致而做错事；
//   * 「需要派发」由 outbox 表保证，与业务事务同生共死。

// 作业状态。
const (
	JobStatusPending   = "pending"
	JobStatusLeased    = "leased"
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"
	// JobStatusDead 表示「重试次数耗尽」。
	// 与 failed 区分：failed 仍可被人工重试，dead 表示自动路径已经放弃，
	// 界面据此给出「需要人工介入」而不是「点一下恢复」。
	JobStatusDead = "dead"
)

// 作业尝试结果。
const (
	AttemptOutcomeRunning      = "running"
	AttemptOutcomeSucceeded    = "succeeded"
	AttemptOutcomeFailed       = "failed"
	AttemptOutcomeLeaseExpired = "lease_expired"
	// AttemptOutcomeFenced 表示「这次尝试的提交被拒绝，因为租约已被别人拿走」。
	// 它是 fencing 生效的取证记录，必须留痕：否则「内容是谁写的」无从判断。
	AttemptOutcomeFenced = "fenced"
)

// outbox 状态。
const (
	OutboxStatusPending    = "pending"
	OutboxStatusDispatched = "dispatched"
	OutboxStatusFailed     = "failed"
)

// JobEnvelopeSchemaVersion 是 Redis 消息体的 schema 版本。
//
// 契约 T33 要求「API/Worker job schema 兼容版本在部署前检查」，
// 因此消息必须自证版本，让旧 worker 能明确拒绝而不是误解析。
const JobEnvelopeSchemaVersion = 1

// JobEnvelope 是写入 Redis 的消息体。
//
// 刻意**不含**业务载荷：载荷在 jobs.payload 里，worker 用 jobId 从 DB 读。
// 这样「重投」永远不会用一份过期的载荷去执行 —— 那是旧实现
// （`{type, datasetId}`）的隐患：datasetId 背后的业务状态可能已经变了。
type JobEnvelope struct {
	SchemaVersion int   `json:"schemaVersion"`
	JobID         int64 `json:"jobId"`
	// Kind 冗余一份便于排障与「旧 worker 不得误吞新消息」的判断。
	Kind string `json:"kind"`
}

// EncodeJobEnvelope 序列化消息体。
func EncodeJobEnvelope(jobID int64, kind string) ([]byte, error) {
	return json.Marshal(JobEnvelope{
		SchemaVersion: JobEnvelopeSchemaVersion,
		JobID:         jobID,
		Kind:          kind,
	})
}

// DecodeJobEnvelope 解析消息体，并校验 schema 版本。
//
// 校验失败必须**拒绝**而不是尽力解析：一个未来版本的载荷可能语义不同，
// 误解析会产生错误的业务动作，而「拒绝并留日志」最多是延迟处理。
func DecodeJobEnvelope(raw []byte) (JobEnvelope, error) {
	var envelope JobEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return JobEnvelope{}, fmt.Errorf("作业消息格式不正确")
	}
	if envelope.SchemaVersion != JobEnvelopeSchemaVersion {
		return JobEnvelope{}, fmt.Errorf(
			"作业消息 schema 版本不兼容（收到 %d，期望 %d）：本 worker 拒绝处理，请先升级 worker",
			envelope.SchemaVersion, JobEnvelopeSchemaVersion)
	}
	if envelope.JobID <= 0 {
		return JobEnvelope{}, fmt.Errorf("作业消息缺少 jobId")
	}
	return envelope, nil
}

// IsLegacyJobPayload 判断一条 Redis 消息是否是**旧格式**（`{type, datasetId}`）。
//
// 为什么必须有这个判断（#160 T06 验收项原文：「旧 `{type,datasetId}` 消费者
// 不得误吞新消息」，反向也成立）：
//
//	新 worker 会同时消费一个队列（过渡期不能要求运维同时切两条队列），
//	因此它必须能区分「新格式（有 jobId + schemaVersion）」与
//	「旧格式（有 type + datasetId）」并分别路由。若新 worker 把旧消息
//	当成新格式解析，会得到一个 jobId=0 的请求；若旧 worker 拿到新消息，
//	它会看到 type 为空而忽略 —— 后者是安全的，前者必须显式拦下。
func IsLegacyJobPayload(raw []byte) bool {
	var probe struct {
		Type      string `json:"type"`
		DatasetID int64  `json:"datasetId"`
		JobID     int64  `json:"jobId"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	// 有 jobId 就是新格式；否则若有 type 或 datasetId 就是旧格式。
	if probe.JobID > 0 {
		return false
	}
	return probe.Type != "" || probe.DatasetID != 0
}

// ProbeJobEnvelopeJobID 宽松地探测一条消息是否是 Studio 作业消息，并取出 jobId。
//
// 与 DecodeJobEnvelope 的区别：**不校验** schema 版本，也不校验 jobId 是否为正。
// 存在的理由是路由：一条未来版本的消息在**旧 worker** 眼里是「不认识的东西」，
// 而旧消费者的 switch 会因为 `type` 为空把它当垃圾丢掉。路由需要一个
// 比「能不能解析」更宽松的判据（「是不是新格式的形态」），才能把它送到
// 能给出明确结论的地方（Studio 消费者会拒绝并送入死信队列）。
func ProbeJobEnvelopeJobID(raw []byte) (int64, bool) {
	var probe struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, false
	}
	return probe.JobID, true
}

// JobIdempotencyKey 组合作业幂等键：actor + project + command + 请求摘要。
//
// 契约 §1.3 要求「同键同请求返回原结果，同键不同请求 409」，而 T06 的
// 验收项把它具体化为「幂等键绑定 actor/project/command 和请求摘要」。
// 四段都不可省，每一段都对应一个真实的串键场景：
//
//   - 缺 actor：两个用户各自的「启动第 1 批」会互相命中，后者拿到前者的批次，
//     而那个批次属于**另一个人的项目**（越权 + 结果错）。
//   - 缺 project：同一用户在两个项目上的同名命令会串。
//   - 缺 command：「创建候选」与「发布候选」共用一键时会互相当成重放。
//   - 缺请求摘要：无法区分「同键同请求」（应回放）与「同键不同请求」（应 409）。
//
// 参数非法时**返回错误**而不是静默降级为「不去重」：去重失败会让双击
// 直接产生两个批次（每一个都会花钱调模型），而错误至少能被调用方变成 422。
func JobIdempotencyKey(actorID, projectID int64, command, requestDigest string) (string, error) {
	if actorID <= 0 {
		return "", errors.New("幂等键需要 actor")
	}
	if projectID <= 0 {
		return "", errors.New("幂等键需要 project")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("幂等键需要命令名")
	}
	requestDigest = strings.TrimSpace(requestDigest)
	if requestDigest == "" {
		return "", errors.New("幂等键需要请求摘要")
	}
	return fmt.Sprintf("a%d:p%d:%s:%s", actorID, projectID, command, requestDigest), nil
}

// Job 是一个权威作业记录。
type Job struct {
	ID        int64           `json:"id"`
	ProjectID *int64          `json:"projectId,omitempty"`
	BatchID   *int64          `json:"batchId,omitempty"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Status    string          `json:"status"`

	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"maxAttempts"`
	NextRunAt   time.Time `json:"nextRunAt"`

	LeaseOwner   string     `json:"leaseOwner,omitempty"`
	LeaseUntil   *time.Time `json:"leaseUntil,omitempty"`
	FencingToken int64      `json:"fencingToken"`

	ErrorClass   string `json:"errorClass"`
	ErrorMessage string `json:"errorMessage"`
	Retryable    bool   `json:"retryable"`

	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	CreatedBy  *int64     `json:"createdBy,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// JobAttempt 是一次尝试的记录。
type JobAttempt struct {
	ID           int64      `json:"id"`
	JobID        int64      `json:"jobId"`
	Attempt      int        `json:"attempt"`
	LeaseOwner   string     `json:"leaseOwner"`
	FencingToken int64      `json:"fencingToken"`
	Outcome      string     `json:"outcome"`
	ErrorClass   string     `json:"errorClass"`
	ErrorMessage string     `json:"errorMessage"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
}

// OutboxEvent 是一条待派发事件。
type OutboxEvent struct {
	ID            int64           `json:"id"`
	EventID       string          `json:"eventId"`
	Topic         string          `json:"topic"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"nextAttemptAt"`
	LastError     string          `json:"lastError"`
	CreatedAt     time.Time       `json:"createdAt"`
	DispatchedAt  *time.Time      `json:"dispatchedAt,omitempty"`
}

// 作业类型（新 Studio 路径）。与旧 job 类型分命名空间：
// 旧类型是 `sft.generate` 这类带点的名字，新类型统一 `studio.` 前缀，
// 让「这条消息属于哪一轮实现」在日志与队列里一眼可辨。
const (
	JobKindBatchGenerate  = "studio.batch.generate"
	JobKindExperimentRun  = "studio.experiment.run"
	JobKindReleaseBuild   = "studio.release.build"
	JobKindOutboxDispatch = "studio.outbox.dispatch"
)

// IsStudioJobKind 判断作业类型是否属于 Atelier 主线。
func IsStudioJobKind(kind string) bool {
	const prefix = "studio."
	return len(kind) > len(prefix) && kind[:len(prefix)] == prefix
}

// 默认租约与重试参数。
//
// 租约时长必须**大于**最长单次外部调用（含重试），否则一个正常运行的 worker
// 会被另一个 worker 判定为过期并抢占，导致同一作业被执行两次。
// 这里取 5 分钟：与 internal/llm 的超时上限同量级，且远小于「用户可接受的停滞」。
const (
	DefaultLeaseDuration = 5 * time.Minute
	DefaultMaxAttempts   = 3
	// JobBackoffBase 是指数退避的基数。限流场景下立即重试只会再被限流，
	// 因此重试必须退避。
	JobBackoffBase = 5 * time.Second
	// JobBackoffMax 是退避上限：不设上限会让「第 5 次重试」等到几小时后，
	// 而用户以为点了恢复没反应。
	JobBackoffMax = 5 * time.Minute
	// OutboxMaxAttempts 是派发失败的重试上限。超过后事件标记 failed 并留错误，
	// 由人工介入 —— 无限重试会让一个永久坏事件（例如 payload 无法序列化）
	// 永远占着 dispatcher 的轮次。
	OutboxMaxAttempts = 10
)

// JobBackoff 计算第 attempt 次尝试失败后的退避时长（指数 + 上限）。
//
// attempt 从 1 开始（第一次失败）。返回值不会是负数或零。
func JobBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := JobBackoffBase
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= JobBackoffMax {
			return JobBackoffMax
		}
	}
	if backoff > JobBackoffMax {
		return JobBackoffMax
	}
	return backoff
}

// IsRetryableJobError 判断错误类别是否值得自动重试。
//
// 区分依据是「同样的输入再来一次是否可能成功」：
//   - 限流/超时/供应商错误 → 是（外部状态可能已变）；
//   - schema 不合法/配置错误 → 否（同样的输入会得到同样的结果，
//     重试只是浪费预算并延长用户等待）。
func IsRetryableJobError(errorClass string) bool {
	switch errorClass {
	case ErrorClassRateLimited, ErrorClassTimeout, ErrorClassProvider, ErrorClassInternal:
		return true
	default:
		return false
	}
}

// JobCapabilities 按作业状态派生能力位（契约 §4）。
//
// 只有 owner 能重试/取消；终态（succeeded/cancelled/dead）不可再操作。
// dead 允许**人工重试**：它表示自动路径放弃，而不是「永远不能做」——
// 用户修好配置之后应当能重新触发。
func JobCapabilities(role, status string) Capabilities {
	canOperate := role == ProjectRoleOwner
	switch status {
	case JobStatusSucceeded, JobStatusCancelled:
		canOperate = false
	}
	return Capabilities{
		CanEdit:     false,
		CanRun:      canOperate,
		CanReview:   false,
		CanPublish:  false,
		CanDownload: false,
	}
}
