package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件是 Atelier 命令层的服务（Issue #160 T08）。
//
// 契约：docs/plans/atelier-api-contract.md §1（通用约定）、§2（命令）、§3（读模型）。
//
// 为什么把授权与幂等收在这里而不是散在 handler 里：
//
//   - **授权**：契约 §1.6 的拒绝有**两种**语义（资源隐藏型 404 与 403），
//     而「服务端当前状态判定」这条要求意味着每个 handler 都必须查库。
//     写 20 遍就会有 20 个地方忘记刷新角色。
//   - **幂等**：契约 §1.3 的幂等键必须绑定 actor + project + command + 摘要。
//     少任何一段都会产生一个可复现的串键场景（见 model.JobIdempotencyKey 的说明）。
//
// 本包只依赖 internal/store 与 internal/model，不引用 net/http。

// Service 是 Atelier 指挥层的依赖聚合。
type Service struct {
	Pool      *pgxpool.Pool
	Projects  *store.ProjectStore
	Authz     *store.AuthzStore
	Documents *store.DocumentStore
	Batches   *store.BatchStore
	Jobs      *store.JobStore
	Usage     *store.UsageStore
	Reviews   *store.ReviewStore
	// Rules 提供质量策略规则的纯读预览与不可变命中证据。
	// 预览端点走这里而不是直接在 handler 里查库，确保项目作用域校验
	// 与其它 Atelier 命令使用同一组 store 依赖。
	Rules            *store.RuleStore
	Experiments      *store.ExperimentStore
	Selections       *store.SelectionStore
	Comparisons      *store.ComparisonStore
	Releases         *store.ReleaseStore
	ReleaseArtifacts *store.ReleaseArtifactStore
	Idempotency      *store.IdempotencyStore
	// Rollout 是特性开关与项目级回退名单（T33）。
	// 零值表示全部启用（见 rollout.go 的说明）。
	Rollout Rollout
}

// New 构造服务。
func New(pool *pgxpool.Pool) *Service {
	return &Service{
		Pool:             pool,
		Projects:         store.NewProjectStore(pool),
		Authz:            store.NewAuthzStore(pool),
		Documents:        store.NewDocumentStore(pool),
		Batches:          store.NewBatchStore(pool),
		Jobs:             store.NewJobStore(pool),
		Usage:            store.NewUsageStore(pool),
		Reviews:          store.NewReviewStore(pool),
		Rules:            store.NewRuleStore(pool),
		Experiments:      store.NewExperimentStore(pool),
		Selections:       store.NewSelectionStore(pool),
		Comparisons:      store.NewComparisonStore(pool),
		Releases:         store.NewReleaseStore(pool),
		ReleaseArtifacts: store.NewReleaseArtifactStore(pool),
		Idempotency:      store.NewIdempotencyStore(pool),
	}
}

// NewWithRollout 构造服务并带上特性开关（T33）。
//
// 与 New 分开而不是改 New 的签名：New 在测试与其它构造点里大量使用，
// 而「不传 rollout」的语义（全部启用）是安全的 —— 改签名只会制造
// 一堆与特性开关无关的改动。
func NewWithRollout(pool *pgxpool.Pool, rollout Rollout) *Service {
	service := New(pool)
	service.Rollout = rollout
	return service
}

// ---------------------------------------------------------------------------
// 授权
// ---------------------------------------------------------------------------

// Authorize 做一次项目级授权，并把拒绝翻译成契约错误。
//
// 两种拒绝的处置不同，且**必须**不同：
//   - 不是成员 → 资源隐藏型 404（对非成员返回 403 等于确认「这个项目存在」）；
//   - 是成员但角色不够 → 403。
//
// 返回 AuthzDecision 而不是只返回 bool：调用方需要 Project（判定时读到的项目）
// 来做后续校验，再查一次会引入 TOCTOU 窗口（判定用旧状态、写入用新状态）。
func (s *Service) Authorize(ctx context.Context, projectID, userID int64, action store.AuthzAction) (store.AuthzDecision, error) {
	decision, denial, reason, err := s.Authz.RequireProjectAccess(ctx, projectID, userID, action)
	if err != nil {
		return store.AuthzDecision{}, NewError(CodeUnavailable, "授权校验暂时不可用，请稍后重试")
	}
	switch denial {
	case store.DenialAllowed:
		// 回退开关的判定放在**授权之后**（T33）：
		// 先授权可以避免把一个运维状态泄露给非成员（非成员应该只看到 404），
		// 同时保证“被暂停”这个信息只对有权访问该项目的人可见。
		if blocked, reason := s.Rollout.BlockedFor(projectID); blocked && s.Rollout.BlocksAction(action) {
			return store.AuthzDecision{}, NewError(CodeUnavailable, reason)
		}
		return decision, nil
	case store.DenialHidden:
		return store.AuthzDecision{}, NewError(CodeNotFound, reason)
	default:
		return store.AuthzDecision{}, NewError(CodeForbidden, reason)
	}
}

// ---------------------------------------------------------------------------
// 幂等（契约 §1.3）
// ---------------------------------------------------------------------------

// IdempotencyOutcome 是一次幂等判定的结果。
type IdempotencyOutcome int

const (
	// IdempotencyExecute 表示没有记录，应当执行命令。
	IdempotencyExecute IdempotencyOutcome = iota
	// IdempotencyReplay 表示命中同键同请求，应当回放原结果。
	IdempotencyReplay
	// IdempotencyConflict 表示命中同键但请求不同，必须返回 409。
	IdempotencyConflict
)

// IdempotencyDecision 是幂等判定的完整结果。
type IdempotencyDecision struct {
	Outcome    IdempotencyOutcome
	ResourceID int64
	Status     int
}

// CommandScope 组合幂等作用域：命令名 + 项目。
//
// 为什么把项目放进 scope 而不是只放进键里：idempotency_records 的唯一键是
// (scope, actor_id, idempotency_key)。若 scope 只有命令名，那么同一个用户的
// 「启动第 1 批」在项目 A 与项目 B 上会互相命中 —— 而键是客户端给的，
// 客户端很可能在多个项目里用同一个 uuid。
func CommandScope(command string, projectID int64) string {
	return fmt.Sprintf("%s:p%d", strings.TrimSpace(command), projectID)
}

// GuardIdempotency 判定「是否应当执行」。
//
// key 为空时直接执行（幂等是可选头）。这一点很重要：把缺失的键当成
// 「同键」会让所有不带该头的请求互相冲突。
func (s *Service) GuardIdempotency(ctx context.Context, scope string, actorID int64, key, digest string) (IdempotencyDecision, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return IdempotencyDecision{Outcome: IdempotencyExecute}, nil
	}
	record, found, err := s.Idempotency.Lookup(ctx, scope, actorID, key)
	if err != nil {
		return IdempotencyDecision{}, NewError(CodeUnavailable, "命令幂等检查暂时不可用，请稍后重试")
	}
	if !found {
		return IdempotencyDecision{Outcome: IdempotencyExecute}, nil
	}
	if record.RequestDigest != digest {
		// 同键不同请求：这是**用户错误**（复用了同一个键），必须显式告知，
		// 否则他会以为「重试拿到了旧结果」，而实际上是两个不同的命令在争一个键。
		return IdempotencyDecision{Outcome: IdempotencyConflict}, nil
	}
	return IdempotencyDecision{Outcome: IdempotencyReplay, ResourceID: record.ResourceID, Status: record.ResponseStatus}, nil
}

// RecordIdempotency 记录一次**已成功**的命令。
//
// 只在成功后写：先写记录再执行会让「执行失败」也占住幂等键，
// 于是用户重试永远拿到一个不存在的资源（T02 已说明同一取舍）。
func (s *Service) RecordIdempotency(ctx context.Context, scope string, actorID int64, key, digest string, resourceID int64, status int) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	return s.Idempotency.Save(ctx, scope, actorID, key, digest, resourceID, status)
}

// ---------------------------------------------------------------------------
// 请求摘要
// ---------------------------------------------------------------------------

// Digest 计算一个请求的语义摘要。
//
// 用「归一化后的结构」而不是原始 body 字节：JSON 字段顺序、空白与
// 未提供字段的显式 null 都会改变字节而语义不变。用原始字节会让同一个
// 语义请求被判成「不同请求」进而返回 409 —— 而用户的动作只做了一次。
//
// 失败时返回带前缀的标记而不是空串：空串会让 RecordIdempotency 写入一条
// 摘要为空的记录，于是**任何**后续请求都会被判成「同键不同请求」。
func Digest(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "undigestible"
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// 读模型：项目概览（契约 §3）
// ---------------------------------------------------------------------------

// ProjectOverview 是项目概览（T10 的 P01 页面）。
//
// 契约 §3 要求「真实版本/批次/待决定；主动作定位当前阻塞」。
// 因此这里返回的是**事实计数**，而不是「完成度百分比」这类合成指标 ——
// 合成指标会把「已生成 231 条但一条都没审」显示成 60% 完成。
type ProjectOverview struct {
	ProjectID  int64  `json:"projectId"`
	TargetKind string `json:"targetKind"`
	Status     string `json:"status"`
	Goal       string `json:"goal"`
	// Versions 是各类最新版本摘要（没有则为 null，不伪造一个空版本）。
	Versions OverviewVersions `json:"versions"`
	// Batches 是批次事实计数（不按最大 ID 猜「当前运行」）。
	Batches OverviewBatches `json:"batches"`
	// Budget 是预算台账（四态）。
	Budget model.BudgetSnapshot `json:"budget"`
	// NextAction 是「下一决定」：按事实指出该去哪个工作区。
	NextAction NextAction `json:"nextAction"`
}

// OverviewVersions 是概览里的版本摘要。
type OverviewVersions struct {
	Blueprint *VersionSummary `json:"blueprint"`
	Coverage  *VersionSummary `json:"coverage"`
	Standard  *VersionSummary `json:"standard"`
	Quality   *VersionSummary `json:"qualityPolicy"`
	Mapping   *VersionSummary `json:"mapping"`
}

// VersionSummary 是一条版本摘要（契约 §1.1 的稳定字段子集）。
type VersionSummary struct {
	VersionID    int64  `json:"versionId"`
	Version      int    `json:"version"`
	LogicalID    string `json:"logicalId"`
	ContentHash  string `json:"contentHash"`
	ChangeReason string `json:"changeReason"`
	CreatedAt    string `json:"createdAt"`
	CreatedBy    *int64 `json:"createdBy,omitempty"`
}

// OverviewBatches 是批次事实计数。
type OverviewBatches struct {
	Total     int `json:"total"`
	Pilot     int `json:"pilot"`
	Scale     int `json:"scale"`
	Running   int `json:"running"`
	Paused    int `json:"paused"`
	Failed    int `json:"failed"`
	Completed int `json:"completed"`
}

// NextAction 是「下一决定」。
//
// 为什么由服务端算而不是前端按计数猜：不同角色看到的「下一决定」不同
// （viewer 没有写权限，不该被指向「去启动试制」），而权限判定在服务端。
type NextAction struct {
	// Kind 是动作类型：design / pilot / review / quality / release / done。
	Kind string `json:"kind"`
	// Message 是面向用户的中文说明。
	Message string `json:"message"`
	// Href 是可直达的路由（前端不自行拼 URL）。
	Href string `json:"href"`
}

// LoadProjectOverview 组装项目概览。
func (s *Service) LoadProjectOverview(ctx context.Context, projectID int64) (ProjectOverview, error) {
	project, err := s.Projects.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectOverview{}, NewError(CodeNotFound, "未找到该项目，请返回项目列表确认它是否已被删除")
		}
		return ProjectOverview{}, err
	}

	overview := ProjectOverview{
		ProjectID:  project.ID,
		TargetKind: project.TargetKind,
		Status:     project.Status,
		Goal:       project.Goal,
	}

	documents, err := s.Documents.ListDocuments(ctx, projectID)
	if err != nil {
		return ProjectOverview{}, err
	}
	for _, document := range documents {
		summary := &VersionSummary{
			VersionID: document.ID,
			Version:   document.CurrentVersion,
			LogicalID: document.LogicalID,
		}
		// Current 是「当前采用版本」；没有版本时保持 nil 摘要字段为空，
		// 而不是伪造一个 hash 为空串的版本 —— 那会让界面显示一个不可用的版本。
		if document.Current != nil {
			summary.VersionID = document.Current.ID
			summary.ContentHash = document.Current.ContentHash
			summary.ChangeReason = document.Current.ChangeReason
			summary.CreatedBy = document.Current.CreatedBy
			summary.CreatedAt = FormatTime(document.Current.CreatedAt)
		} else {
			summary.CreatedAt = FormatTime(document.UpdatedAt)
		}
		switch document.Kind {
		case model.KindBlueprint:
			overview.Versions.Blueprint = summary
		case model.KindCoverage:
			overview.Versions.Coverage = summary
		case model.KindStandard:
			overview.Versions.Standard = summary
		case model.KindQualityPolicy:
			overview.Versions.Quality = summary
		case model.KindMapping:
			overview.Versions.Mapping = summary
		}
	}

	batches, err := s.Batches.ListBatches(ctx, store.BatchListQuery{ProjectID: projectID, Limit: 100})
	if err != nil {
		return ProjectOverview{}, err
	}
	for _, batch := range batches {
		overview.Batches.Total++
		switch batch.Purpose {
		case model.BatchPurposePilot:
			overview.Batches.Pilot++
		case model.BatchPurposeScale:
			overview.Batches.Scale++
		}
		switch batch.Status {
		case model.BatchStatusRunning, model.BatchStatusQueued, model.BatchStatusPauseRequested:
			overview.Batches.Running++
		case model.BatchStatusPaused:
			overview.Batches.Paused++
		case model.BatchStatusFailed, model.BatchStatusPartialFailed:
			overview.Batches.Failed++
		case model.BatchStatusCompleted:
			overview.Batches.Completed++
		}
	}

	budget, err := s.Usage.ProjectBudget(ctx, projectID, project.Budget.Currency)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 项目刚被删除（并发）：按不存在处理，而不是返回一个可疑的空预算。
			return ProjectOverview{}, NewError(CodeNotFound, "未找到该项目，请返回项目列表确认它是否已被删除")
		}
		return ProjectOverview{}, err
	}
	overview.Budget = budget

	overview.NextAction = nextActionFor(project, overview)
	return overview, nil
}

// nextActionFor 按**事实**决定「下一决定」。
//
// 顺序刻意是「设计 → 试制 → 生产 → 质量 → 发布」：
// 每一档的前置条件由真实数据判定，因此一个只有蓝图的项目不会被指向
// 「去发布」；而一个已经有完成批次的项目不会被一直指向「去设计」。
func nextActionFor(project model.Project, overview ProjectOverview) NextAction {
	base := fmt.Sprintf("/p/%d", project.ID)
	hasBlueprint := overview.Versions.Blueprint != nil
	if !hasBlueprint {
		return NextAction{Kind: "design", Message: "还没有保存蓝图，先完成设计再试制", Href: base + "/blueprint"}
	}
	if overview.Versions.Coverage == nil {
		return NextAction{Kind: "design", Message: "还没有覆盖方案，先定义领域与方向配额", Href: base + "/coverage"}
	}
	if overview.Batches.Pilot == 0 {
		return NextAction{Kind: "pilot", Message: "还没有试制批次，先跑一次小批试制验证方案", Href: base + "/pilot"}
	}
	if overview.Batches.Running > 0 {
		return NextAction{Kind: "pilot", Message: "有批次正在运行，查看进度与失败项", Href: base + "/runs"}
	}
	if overview.Batches.Failed > 0 {
		return NextAction{Kind: "pilot", Message: "有批次失败或部分失败，处理失败项后继续", Href: base + "/runs"}
	}
	if overview.Batches.Scale == 0 {
		return NextAction{Kind: "pilot", Message: "试制已完成，比较结果后规划扩量批次", Href: base + "/compare"}
	}
	return NextAction{Kind: "quality", Message: "查看质量结论并准备发布", Href: base + "/quality"}
}

// ---------------------------------------------------------------------------
// 读模型：批次
// ---------------------------------------------------------------------------

// BatchSummary 是批次列表项。
type BatchSummary struct {
	BatchID          int64  `json:"batchId"`
	ResourceID       string `json:"resourceId"`
	Purpose          string `json:"purpose"`
	Status           string `json:"status"`
	ControlState     string `json:"controlState"`
	TargetKind       string `json:"targetKind"`
	PlannedUnits     int    `json:"plannedUnits"`
	CompletedUnits   int    `json:"completedUnits"`
	FailedUnits      int    `json:"failedUnits"`
	InFlightUnits    int    `json:"inFlightUnits"`
	BudgetCurrency   string `json:"budgetCurrency"`
	BudgetLimitMinor int64  `json:"budgetLimitMinor"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
	// Capabilities are included on list rows as a fail-closed UI hint. The
	// authoritative command endpoints still recalculate authorization.
	Capabilities BatchCapabilities `json:"capabilities"`
}

// ToBatchSummary 把批次模型转成列表项。
func ToBatchSummary(batch model.Batch) BatchSummary {
	return BatchSummary{
		BatchID:          batch.ID,
		ResourceID:       BatchResourceID(batch.ID),
		Purpose:          batch.Purpose,
		Status:           batch.Status,
		ControlState:     batch.ControlState,
		TargetKind:       batch.TargetKind,
		PlannedUnits:     batch.PlannedUnits,
		CompletedUnits:   batch.CompletedUnits,
		FailedUnits:      batch.FailedUnits,
		InFlightUnits:    batch.InFlightUnits,
		BudgetCurrency:   batch.Budget.Currency,
		BudgetLimitMinor: batch.Budget.LimitMinor,
		CreatedAt:        FormatTime(batch.CreatedAt),
		UpdatedAt:        FormatTime(batch.UpdatedAt),
	}
}

// ToBatchSummaryWithCapabilities is the role-aware form used by HTTP read
// models. Keeping the base converter role-free avoids accidentally trusting a
// caller-supplied role in worker/service code, while list/detail handlers can
// attach the current user's UI affordances explicitly.
func ToBatchSummaryWithCapabilities(batch model.Batch, capabilities BatchCapabilities) BatchSummary {
	summary := ToBatchSummary(batch)
	summary.Capabilities = capabilities
	return summary
}

// BatchBudgetView 是批次级预算台账视图（T07 的三个计数器 + 上限）。
//
// 与 model.BudgetSnapshot 分开：那个是**项目级**快照（带 ProjectID 与 version），
// 而批次台账存在 batches 行上、没有独立版本号。用同一个类型会让
// 「批次的 version」看起来存在而实际总是 0。
type BatchBudgetView struct {
	Currency       string `json:"currency"`
	LimitMinor     int64  `json:"limitMinor"`
	ReservedMinor  int64  `json:"reservedMinor"`
	SettledMinor   int64  `json:"settledMinor"`
	UncertainMinor int64  `json:"uncertainMinor"`
}

// BatchResourceID 是批次的稳定 ID 形态（与项目的 p_ 前缀同构）。
//
// 前缀的存在让「拿批次 ID 拼项目 URL」这类错误在 URL 上立刻可见，
// 而不是变成一个指向另一个项目的合法请求。
func BatchResourceID(id int64) string { return fmt.Sprintf("b_%d", id) }

// ParseBatchID 解析批次 ID，同时接受 `b_12` 与 `12`。
func ParseBatchID(raw string) (int64, error) {
	return parsePrefixedID(raw, "b_", "批次")
}

// SampleResourceID 是样本的稳定 ID 形态。
func SampleResourceID(id int64) string { return fmt.Sprintf("s_%d", id) }

// ParseSampleID 解析样本 ID。
func ParseSampleID(raw string) (int64, error) {
	return parsePrefixedID(raw, "s_", "样本")
}

func parsePrefixedID(raw, prefix, label string) (int64, error) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), prefix)
	if value == "" {
		return 0, NewValidationError("地址不正确，请从列表重新进入", []model.FieldError{{
			Field: "id", Message: label + " ID 不能为空",
		}})
	}
	var id int64
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, NewValidationError("地址不正确，请从列表重新进入", []model.FieldError{{
				Field: "id", Message: label + " ID 必须是数字",
			}})
		}
		id = id*10 + int64(char-'0')
		if id > (1 << 62) {
			return 0, NewValidationError("地址不正确，请从列表重新进入", []model.FieldError{{
				Field: "id", Message: label + " ID 超出范围",
			}})
		}
	}
	if id <= 0 {
		return 0, NewValidationError("地址不正确，请从列表重新进入", []model.FieldError{{
			Field: "id", Message: label + " ID 必须为正数",
		}})
	}
	return id, nil
}

// BatchCapabilitiesFor 按批次状态与能力推导批次能力位（契约 §4）。
//
// 与 model.JobCapabilities 同一原则：终态不可再控制，而「暂停请求中」
// 不该再给「暂停」按钮（重复点会得到 409，不如直接禁用）。
func BatchCapabilitiesFor(role, status string) BatchCapabilities {
	canOperate := role == model.ProjectRoleOwner
	switch status {
	case model.BatchStatusCompleted, model.BatchStatusFailed:
		// 终态不可再控制：给按钮只会在点击时返回 409，不如直接禁用。
		canOperate = false
	}
	// 运行中/排队中才可暂停；暂停或部分失败后可恢复。
	pausable := status == model.BatchStatusQueued || status == model.BatchStatusRunning
	resumable := status == model.BatchStatusPaused || status == model.BatchStatusPauseRequested ||
		status == model.BatchStatusPartialFailed
	return BatchCapabilities{
		CanPause:       canOperate && pausable,
		CanResume:      canOperate && resumable,
		CanRetryFailed: canOperate && (status == model.BatchStatusPartialFailed || status == model.BatchStatusPaused),
	}
}

// ---------------------------------------------------------------------------
// 读模型：分列统计（契约 §3.1）
// ---------------------------------------------------------------------------

// SampleStats 是分列统计。
//
// 契约 §3.1 明确要求这些数字**不得相互冒充**：`plannedQuestions` 是
// n × m × x 的计划量，不是「已产出」；接纳率的分母是**纳入检查**的样本版本数，
// 而不是计划量。旧实现用 answerVariants/rewardVariants 偷乘目标量，
// 于是「完成度」在一件都没做时就已经是 20%。
type SampleStats struct {
	PlannedQuestions int `json:"plannedQuestions"`
	Generated        int `json:"generated"`
	StructureValid   int `json:"structureValid"`
	Inspected        int `json:"inspected"`
	Scored           int `json:"scored"`
	Accepted         int `json:"accepted"`
	Quarantined      int `json:"quarantined"`
	PendingReview    int `json:"pendingReview"`
	// AcceptanceRate 为 nil 表示**无结论**（分母为 0），不是 100%。
	AcceptanceRate *float64 `json:"acceptanceRate"`
	// AcceptanceRateDisplay 在无结论时是「无结论」而不是「100.0%」。
	AcceptanceRateDisplay string `json:"acceptanceRateDisplay"`
}

// AcceptanceRateOf 是 model.AcceptanceRateOf 的薄包装。
//
// 保留这个名字是为了让既有 API 调用点（apps/api/routes_studio_read.go）
// 无需改动；而**规则本身**只有一份（在 model 里）——
// 项目统计与实验统计显示不同的接纳率是致命的，那正是两处各写一遍的必然结果。
func AcceptanceRateOf(accepted, inspected int) (*float64, string) {
	return model.AcceptanceRateOf(accepted, inspected)
}

// PlannedQuestions 计算计划问题数（n × m × x）。
//
// 单独一个函数是为了让「这是计划量」这件事在调用点显式可见：
// 把它内联进统计结构会让下一个人很容易把它当成「已产出」。
func PlannedQuestions(domains, directionsPerDomain, questionsPerDirection int) int {
	if domains <= 0 || directionsPerDomain <= 0 || questionsPerDirection <= 0 {
		return 0
	}
	return domains * directionsPerDomain * questionsPerDirection
}
