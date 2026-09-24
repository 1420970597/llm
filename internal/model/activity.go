package model

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 本文件定义「今日工作」「动态」与「评论」的对象与判据（Issue #160 T27）。
//
// 契约：docs/plans/atelier-implementation.md §4.1（Activity / Comment）、
// sql/migrations/0033_studio_activity_comments.sql 的文件头（表结构取舍）。
//
// 三条与「不要让用户误判」直接相关的取舍：
//
//  1. **未读 != 未处理**。已读只是一个个人水位，它不影响任何业务状态
//     （待办由事实派生：待判断、失败恢复、候选阻塞……）。界面因此不能用
//     「未读为 0」表达「都处理完了」。
//  2. **待办一律带具体对象链接**。一条「3 个批次失败」而没有跳转链接的待办
//     等于没有信息 —— 用户还得自己去找是哪三个。
//  3. **评论不是 Decision**。它不改投影、不解除发布门槛。这条在类型层面
//     体现为：评论的类型与 Decision 完全分开，且注释里写明不可混用。

// 待办种类（T27 原文列出的四类 + 动态未读）。
const (
	// TodoPendingReview 待判断：投影里仍处于 pending 的内容版本。
	TodoPendingReview = "pending_review"
	// TodoPilotComparable 试制可比：同一项目已有两个完成的 pilot 且尚未采用方案。
	TodoPilotComparable = "pilot_comparable"
	// TodoFailedRecovery 失败恢复：partial_failed / failed 的批次。
	TodoFailedRecovery = "failed_recovery"
	// TodoReleaseBlocked 候选阻塞：被门槛挡住的发布候选。
	TodoReleaseBlocked = "release_blocked"
	// TodoUnreadActivity 动态未读：该工作区有比个人水位更新的事件。
	TodoUnreadActivity = "unread_activity"
)

// TodoItem 是一条待办。
//
// 每条都带 `ProjectID` 与 `Links`：没有链接的待办无法行动，而「让用户自己找」
// 在几十个项目下等于让待办不可用。
type TodoItem struct {
	Kind      string `json:"kind"`
	ProjectID int64  `json:"projectId"`
	// Count 是这一类的数量（为 0 时该条不应出现）。
	Count int64 `json:"count"`
	// Summary 是面向用户的短句（含具体数字）。
	Summary string `json:"summary"`
	// SampledIDs 是示例对象 ID（便于界面直接列出前几条）。
	SampledIDs []int64   `json:"sampledIds,omitempty"`
	Links      Links     `json:"links"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Links 是可跳转的页面/接口地址（前端不自行拼 URL）。
type Links map[string]string

// ActivitySource 是动态事件的来源。
//
// 用来源 + 事件 ID 组成游标的一部分：动态是**多张表的合并流**，
// 单独 (时间, ID) 不是全序（两张表的主键各自递增，会撞车）。
const (
	ActivitySourceBatch = "batch"
	ActivitySourceAudit = "audit"
)

// ActivityItem 是一条动态。
type ActivityItem struct {
	Source  string `json:"source"`
	EventID int64  `json:"eventId"`
	// GroupKey 是聚合动态的稳定身份。批次失败事件会不断追加新 event id，
	// 但同一批次的失败通知必须在轮询/分页合并时仍被视为同一条动态。
	GroupKey  string `json:"groupKey,omitempty"`
	ProjectID int64  `json:"projectId"`
	Kind      string `json:"kind"`
	ActorID   *int64 `json:"actorId,omitempty"`
	Summary   string `json:"summary"`
	Detail    string `json:"detail,omitempty"`
	// AggregateCount/AggregateTotal 仅用于聚合动态（例如批次失败 12/12）。
	AggregateCount int64     `json:"aggregateCount,omitempty"`
	AggregateTotal int64     `json:"aggregateTotal,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	Links          Links     `json:"links"`
	// Unread 是「相对当前用户水位」的判断结果，由服务端计算。
	// 它**不是**事件自身的属性（同一事件对不同用户可能一个已读一个未读）。
	Unread bool `json:"unread"`
}

// ReadWatermark 是个人阅读水位。
type ReadWatermark struct {
	UserID          int64     `json:"userId"`
	WorkspaceID     int64     `json:"workspaceId"`
	LastSeenAt      time.Time `json:"lastSeenAt"`
	LastSeenEventID int64     `json:"lastSeenEventId"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ---------------------------------------------------------------------------
// 评论
// ---------------------------------------------------------------------------

// 评论锚点类型。
const (
	CommentAnchorSampleVersion = "sample_version"
	CommentAnchorBatch         = "batch"
)

const (
	maxCommentBodyLength   = 4000
	maxCommentMentions     = 20
	maxCommentMentionLabel = 200
)

// Comment 是一条评论（当前有效版本或历史修订）。
type Comment struct {
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"projectId"`
	AnchorKind string `json:"anchorKind"`
	AnchorID   int64  `json:"anchorId"`
	Body       string `json:"body"`
	// Mentions 是被提及的用户 ID。
	Mentions []int64 `json:"mentions"`
	Revision int     `json:"revision"`
	// SupersedesID/SupersededBy 构成修订链（两个方向都记，见迁移文件头）。
	SupersedesID *int64    `json:"supersedesId,omitempty"`
	SupersededBy *int64    `json:"supersededBy,omitempty"`
	AuthorID     int64     `json:"authorId"`
	CreatedAt    time.Time `json:"createdAt"`
	// Current 表示这是该锚点上该作者的当前有效评论（由查询派生）。
	Current bool `json:"current"`
}

// ValidateCommentInput 校验评论正文与提及列表。
//
// 提及的**合法性**（是否为项目成员）由 store 校验：模型层拿不到成员关系，
// 在这里假装能校验会让「校验通过但实际无权」的失败发生在读取时。
func ValidateCommentInput(anchorKind string, anchorID int64, body string, mentions []int64) error {
	var errs FieldErrors
	switch anchorKind {
	case CommentAnchorSampleVersion, CommentAnchorBatch:
	default:
		errs = append(errs, FieldError{Field: "anchorKind",
			Message: fmt.Sprintf("只能是 %s 或 %s", CommentAnchorSampleVersion, CommentAnchorBatch)})
	}
	if anchorID <= 0 {
		errs = append(errs, FieldError{Field: "anchorId", Message: "必填"})
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		errs = append(errs, FieldError{Field: "body", Message: "评论内容不能为空"})
	}
	if len([]rune(trimmed)) > maxCommentBodyLength {
		errs = append(errs, FieldError{Field: "body",
			Message: fmt.Sprintf("不能超过 %d 个字符", maxCommentBodyLength)})
	}
	if len(mentions) > maxCommentMentions {
		errs = append(errs, FieldError{Field: "mentions",
			Message: fmt.Sprintf("最多提及 %d 人", maxCommentMentions)})
	}
	seen := map[int64]bool{}
	for index, userID := range mentions {
		if userID <= 0 {
			errs = append(errs, FieldError{Field: fmt.Sprintf("mentions[%d]", index), Message: "用户 ID 不合法"})
			continue
		}
		if seen[userID] {
			errs = append(errs, FieldError{Field: fmt.Sprintf("mentions[%d]", index), Message: "重复提及同一用户"})
		}
		seen[userID] = true
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// CommentCapabilities 派生评论能力位。
//
// 只有作者能更正自己的评论（与「最后一名 owner 不可移除」同类的确定性规则：
// 允许任何人改别人的话会让评论区失去作为证据的价值）。
func CommentCapabilities(role string, comment Comment, userID int64) Capabilities {
	isAuthor := comment.AuthorID == userID
	canReview := role == ProjectRoleOwner || role == ProjectRoleReviewer
	return Capabilities{
		CanEdit:     isAuthor && comment.Current,
		CanRun:      false,
		CanReview:   canReview,
		CanPublish:  false,
		CanDownload: true,
	}
}

// ---------------------------------------------------------------------------
// 动态流的游标与分页
// ---------------------------------------------------------------------------
//
// 为什么不放在 internal/studio：store 需要用它做 keyset 分页，而
// internal/studio 依赖 internal/store —— 把游标放在 studio 会形成导入环。
// 放在 model 是它唯一能同时被两端使用的位置。
//
// 为什么不复用通用的 Cursor{Time, ID}：动态是**多张表的合并流**
// （batch_events 与 audit_logs 各自有独立递增的主键），(时间, ID) 不是全序 ——
// 两次不同来源的事件可以拥有相同的时间与 ID，而那时「取游标之后的行」
// 会漏行或重复行。因此把**来源**也放进排序键：(Time, Source, ID)。

// ActivityCursor 是动态流的分页游标。
type ActivityCursor struct {
	Time   int64  `json:"t"`
	Source string `json:"s"`
	ID     int64  `json:"i"`
}

// EncodeActivityCursor 编码游标（不透明字符串，避免调用方手写它）。
func EncodeActivityCursor(cursor ActivityCursor) string {
	if cursor.Time == 0 && cursor.Source == "" && cursor.ID == 0 {
		return ""
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeActivityCursor 解析游标。
//
// 解析失败返回字段错误而不是「从头开始」：静默从头开始会让前端在一个坏游标上
// 无限拉第一页，而用户看到的是「列表刷不完」。
func DecodeActivityCursor(raw string) (ActivityCursor, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ActivityCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return ActivityCursor{}, FieldErrors{{Field: "cursor", Message: "游标格式不正确"}}
	}
	var cursor ActivityCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return ActivityCursor{}, FieldErrors{{Field: "cursor", Message: "游标内容不正确"}}
	}
	return cursor, nil
}

// ActivityCursorOf 从一条动态派生游标。
func ActivityCursorOf(item ActivityItem) ActivityCursor {
	return ActivityCursor{Time: item.CreatedAt.UnixMicro(), Source: item.Source, ID: item.EventID}
}

// ActivitySourceRank 给出跨来源的**稳定**排序权重（时间相同时用它打破平局）。
//
// 只要求稳定：写进数据库或依赖 map 遍历顺序都会让「同一份数据的两次分页」
// 得到不同顺序，从而在界面上表现为「翻页时少了一条」。
func ActivitySourceRank(source string) int {
	switch source {
	case ActivitySourceBatch:
		return 0
	case ActivitySourceAudit:
		return 1
	default:
		return 2
	}
}

// SortActivityItems 按 (时间, 来源, ID) 倒序排列（分页键即排序键）。
func SortActivityItems(items []ActivityItem) {
	for index := 1; index < len(items); index++ {
		current := items[index]
		position := index - 1
		for position >= 0 && activityLess(current, items[position]) {
			items[position+1] = items[position]
			position--
		}
		items[position+1] = current
	}
}

func activityLess(a, b ActivityItem) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if ActivitySourceRank(a.Source) != ActivitySourceRank(b.Source) {
		return ActivitySourceRank(a.Source) < ActivitySourceRank(b.Source)
	}
	return a.EventID > b.EventID
}

// ActivityAfterCursor 判断一条动态是否在游标之后（keyset 分页）。
func ActivityAfterCursor(item ActivityItem, cursor ActivityCursor) bool {
	if cursor.Time == 0 && cursor.Source == "" && cursor.ID == 0 {
		return true
	}
	itemTime := item.CreatedAt.UnixMicro()
	if itemTime != cursor.Time {
		return itemTime < cursor.Time
	}
	if ActivitySourceRank(item.Source) != ActivitySourceRank(cursor.Source) {
		return ActivitySourceRank(item.Source) > ActivitySourceRank(cursor.Source)
	}
	return item.EventID < cursor.ID
}

// ActivityUnread 判断一条动态相对水位是否未读。
//
// 「未读」只影响红点：T27 验收项明确「未读不等于业务已处理」，
// 因此这个判断不参与任何业务状态。
func ActivityUnread(item ActivityItem, watermark ReadWatermark) bool {
	if watermark.LastSeenAt.IsZero() {
		return true
	}
	if item.CreatedAt.After(watermark.LastSeenAt) {
		return true
	}
	if item.CreatedAt.Equal(watermark.LastSeenAt) {
		return item.EventID > watermark.LastSeenEventID
	}
	return false
}
