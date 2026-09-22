// Package studio 是 Atelier 主线（Issue #160）的契约与命令层。
//
// 为什么需要这一层（T08 的落点之一）：
//
//	`apps/api` 里的 handler 只做「解析请求 → 调用服务 → 写响应」，
//	而「响应长什么样」「错误码怎么映射」「游标怎么编解码」「幂等怎么判定」
//	是**契约**，必须只有一份定义。放在 handler 里会让 T10–T29 的每个页面
//	各自演化出一套，而那正是 #159 批评的「漂移的路由映射表」的同类问题。
//
// 本包不引用 net/http：HTTP 状态码的映射属于 handler（一个契约错误可能
// 在不同资源下对应不同状态码，例如「不是成员」要返回资源隐藏型 404）。
// 这样这层可以被 worker 与测试直接使用。
package studio

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// ---------------------------------------------------------------------------
// 响应信封（契约 §1.1）
// ---------------------------------------------------------------------------

// Links 是可跳转链接。前端**不自行拼 URL**（契约 §1.1）：
// 让前端拼 URL 意味着路由规则有两份实现，改一处必然漏一处。
type Links map[string]string

// Envelope 是所有单对象响应的稳定外壳（契约 §1.1）。
//
// Capabilities 用 `any` 而不是统一的 Capabilities 结构：契约 §4 为每类对象
// 定义了**不同**的能力键（Batch 是 canPause/canResume/canRetryFailed，
// Release 是 canPublish/canDownload/canCreateNext…）。把它们塞进一个结构里
// 会让每类对象都返回一堆与自己无关的键，而前端无法据此判断「这个键存在
// 意味着这个操作可用」。
type Envelope struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	Revision     int64    `json:"revision"`
	UpdatedAt    string   `json:"updatedAt"`
	Capabilities any      `json:"capabilities"`
	Links        Links    `json:"links"`
	Warnings     []string `json:"warnings"`
	// Data 是对象本身。刻意不把对象字段提升到顶层：不同资源的同名字段
	// （例如都有 `status`）语义不同，提升会让「batch.status」与「release.status」
	// 在前端类型里互相覆盖。
	Data any `json:"data"`
}

// NewEnvelope 组装一个响应信封。
//
// Warnings 与 Links 保证非 nil：前端对 null 与 [] 的处理不同，
// 而「没有警告」与「警告为空数组」在语义上应当是同一件事。
func NewEnvelope(id, status string, revision int64, updatedAt time.Time, capabilities any, links Links, warnings []string) Envelope {
	if links == nil {
		links = Links{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return Envelope{
		ID:           id,
		Status:       status,
		Revision:     revision,
		UpdatedAt:    FormatTime(updatedAt),
		Capabilities: capabilities,
		Links:        links,
		Warnings:     warnings,
	}
}

// FormatTime 统一时间序列化形态（RFC3339，UTC）。
//
// 统一而不是让各处用 time.Time 默认格式：默认格式会带本机时区偏移，
// 于是同一个时刻在不同部署上序列化出不同字符串，前端缓存与比较会失效。
func FormatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// ---------------------------------------------------------------------------
// 错误契约（契约 §1.2）
// ---------------------------------------------------------------------------

// 错误码。前端按 code 分支，**不解析中文文案**（文案会改，code 不会）。
const (
	CodeValidation    = "VALIDATION_FAILED"
	CodeUnauthorized  = "UNAUTHORIZED"
	CodeForbidden     = "FORBIDDEN"
	CodeNotFound      = "NOT_FOUND"
	CodeConflict      = "CONFLICT"
	CodeRevisionStale = "REVISION_CONFLICT"
	CodeIdempotency   = "IDEMPOTENCY_KEY_REUSED"
	CodeBudget        = "BUDGET_EXHAUSTED"
	CodeUnavailable   = "DEPENDENCY_UNAVAILABLE"
	CodeRateLimited   = "RATE_LIMITED"
)

// Blocker 是可跳转的阻塞项（契约 §2.8）。
//
// 为什么必须带 Link：T22 的验收项要求「每条 blocker 链到具体样本/证据/节点」。
// 只给一句「还有 3 条待审阅」会让用户自己去列表里找那 3 条。
type Blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Link    string `json:"link,omitempty"`
}

// ErrorBody 是错误响应的实体（契约 §1.2）。
type ErrorBody struct {
	Code        string             `json:"code"`
	Message     string             `json:"message"`
	FieldErrors []model.FieldError `json:"fieldErrors,omitempty"`
	Blockers    []Blocker          `json:"blockers,omitempty"`
	RequestID   string             `json:"requestId"`
	Retryable   bool               `json:"retryable"`
}

// Error 是携带契约错误码的领域错误。
//
// HTTP 状态码**不在**这里：同一个 code 在不同资源下可能对应不同状态
// （例如「不存在」对公开对象是 404、对无权得知的对象是资源隐藏型 404 但
// 对已登录的非成员也应该 404 —— 而 FORBIDDEN 必须是 403）。
// handler 用 StatusFor 做一次集中映射。
type Error struct {
	Code        string
	Message     string
	FieldErrors []model.FieldError
	Blockers    []Blocker
}

func (e *Error) Error() string { return e.Message }

// NewError 构造一个契约错误。
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// NewValidationError 构造字段级校验错误（422）。
func NewValidationError(message string, fieldErrors []model.FieldError) *Error {
	return &Error{Code: CodeValidation, Message: message, FieldErrors: fieldErrors}
}

// NewBlockerError 构造带阻塞项的错误（用于发布候选等门槛失败的场景）。
func NewBlockerError(code, message string, blockers []Blocker) *Error {
	return &Error{Code: code, Message: message, Blockers: blockers}
}

// StatusFor 把契约错误码集中映射成 HTTP 状态码。
//
// 集中在一处而不是每个 handler 自己选：错误码与状态码的对应关系是契约
// （契约 §1.2 的表），散落在几十个 handler 里必然漂移，而漂移的直接后果是
// 前端按 409 写的冲突处理拿不到冲突。
func StatusFor(err error) int {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return 500
	}
	switch apiErr.Code {
	case CodeUnauthorized:
		return 401
	case CodeForbidden:
		return 403
	case CodeNotFound:
		return 404
	case CodeConflict, CodeRevisionStale, CodeIdempotency:
		return 409
	case CodeValidation:
		return 422
	case CodeBudget, CodeRateLimited:
		return 429
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}

// IsRetryable 判断某个错误码是否值得重试（契约 §1.2 的 retryable 字段）。
//
// 只把「外部状态可能已变」的类别标为可重试：把校验失败标成可重试会让
// 前端自动重发同样的非法请求，而用户看到的是「重试了但还是失败」。
func IsRetryable(code string) bool {
	switch code {
	case CodeBudget, CodeRateLimited, CodeUnavailable:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 分页（契约 §1.5）
// ---------------------------------------------------------------------------

// Cursor 是稳定游标。
//
// 用 (时间, ID) 二元组而不是单纯的时间：同一毫秒内创建的多个对象用时间无法
// 区分，翻页会重复或漏行。ID 作为末位排序键保证全序。
type Cursor struct {
	Time time.Time `json:"t"`
	ID   int64     `json:"i"`
}

// EncodeCursor 把游标编码成不可读的字符串。
//
// 为什么编码而不是明文 JSON：明文会让调用方以为它是个稳定的查询参数
// 而手写它（例如拼一个 {"t":"2026-01-01","i":1}），把那部分变成事实上的 API，
// 从而无法在不破坏兼容的情况下改变排序键。
func EncodeCursor(cursor Cursor) string {
	if cursor.Time.IsZero() && cursor.ID == 0 {
		return ""
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor 解析游标。
//
// 解析失败返回校验错误而不是「从头开始」：静默从头开始会让前端在一个坏游标上
// 无限循环拉第一页（用户看到列表永远刷不完），而错误至少能暴露问题。
func DecodeCursor(raw string) (Cursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Cursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, NewValidationError("分页游标无法解析，请刷新列表后重试", []model.FieldError{{
			Field: "cursor", Message: "游标格式不正确",
		}})
	}
	var cursor Cursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return Cursor{}, NewValidationError("分页游标无法解析，请刷新列表后重试", []model.FieldError{{
			Field: "cursor", Message: "游标内容不正确",
		}})
	}
	return cursor, nil
}

// Page 是列表响应（契约 §1.5）。
//
// SortKey 必须与游标比较所用的键一致，否则「翻页无重复无遗漏」无法验证：
// 前端的测试与运维的排查都要靠它判断「这两页是不是同一个排序」。
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor"`
	SortKey    string `json:"sortKey"`
}

// NewPage 组装一页结果。
//
// limit+1 的取法：调用方多取一条，用于判断「还有下一页」。
// 用 COUNT(*) 判断总数在 keyset 分页下每次都要全表扫一遍，
// 而「有没有下一页」只需要一条额外记录。多取的那条**不返回**。
func NewPage[T any](items []T, limit int, sortKey string, cursorOf func(T) Cursor) Page[T] {
	page := Page[T]{Items: items, SortKey: sortKey}
	if limit > 0 && len(items) > limit {
		page.Items = items[:limit]
		last := cursorOf(page.Items[len(page.Items)-1])
		page.NextCursor = EncodeCursor(last)
	}
	if page.Items == nil {
		page.Items = []T{}
	}
	return page
}

// ---------------------------------------------------------------------------
// 能力（契约 §4）
// ---------------------------------------------------------------------------

// BatchCapabilities 是批次能力位（契约 §4）。
type BatchCapabilities struct {
	CanPause       bool `json:"canPause"`
	CanResume      bool `json:"canResume"`
	CanRetryFailed bool `json:"canRetryFailed"`
}

// SampleCapabilities 是样本能力位。
type SampleCapabilities struct {
	CanReview      bool `json:"canReview"`
	CanViewHistory bool `json:"canViewHistory"`
}

// DocumentCapabilities 是版本化文档能力位。
type DocumentCapabilities struct {
	CanEdit    bool `json:"canEdit"`
	CanCopy    bool `json:"canCopy"`
	CanCompare bool `json:"canCompare"`
}

// ---------------------------------------------------------------------------
// 查询参数
// ---------------------------------------------------------------------------

// ListQuery 是列表命令的通用参数（契约 §1.5）。
type ListQuery struct {
	Limit  int
	Cursor Cursor
	Search string
	Status string
	Batch  string
	Risk   string
	// Purpose 用于批次列表（pilot/scale）。
	Purpose string
}

// ParseListQuery 从 URL 查询串解析列表参数。
//
// 集中解析而不是每个 handler 自己读 query：limit 的上界与非法值的处置
// 是契约的一部分（超过 100 会拖垮服务端序列化，静默截断会让用户以为
// 「一共就这么多」）。这里超界即校正到上界并**在响应里保留真实 limit**，
// 而不是报错 —— 分页参数非法时返回错误会让「翻到最后一页」这种正常操作失败。
func ParseListQuery(values url.Values, defaultLimit int) (ListQuery, error) {
	query := ListQuery{Search: strings.TrimSpace(values.Get("q")), Status: strings.TrimSpace(values.Get("status")),
		Batch: strings.TrimSpace(values.Get("batch")), Risk: strings.TrimSpace(values.Get("risk")),
		Purpose: strings.TrimSpace(values.Get("purpose"))}

	limit := defaultLimit
	if limit <= 0 {
		limit = 20
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := parsePositiveInt(raw)
		if err != nil {
			return ListQuery{}, NewValidationError("每页数量必须是正整数", []model.FieldError{{
				Field: "limit", Message: "必须是 1–100 之间的整数",
			}})
		}
		limit = parsed
	}
	if limit > 100 {
		limit = 100
	}
	if limit < 1 {
		limit = 1
	}
	query.Limit = limit

	cursor, err := DecodeCursor(values.Get("cursor"))
	if err != nil {
		return ListQuery{}, err
	}
	query.Cursor = cursor
	return query, nil
}

// parsePositiveInt 解析正整数，拒绝 `12abc`、`+1`、负数与空串。
func parsePositiveInt(raw string) (int, error) {
	value := 0
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("不是正整数：%s", raw)
		}
		value = value*10 + int(char-'0')
		if value > 1_000_000 {
			return 0, fmt.Errorf("数值过大：%s", raw)
		}
	}
	if value == 0 {
		return 0, fmt.Errorf("必须是正整数：%s", raw)
	}
	return value, nil
}
