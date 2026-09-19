package main

// 用户可见错误文案的统一出口（issue #102 / #109）。
//
// 背景：本仓库的产品面向中文用户，`writeError` 的约定是「4xx 原样透出业务提示」。
// 但历史上这批 4xx 文案大量使用英文（`dataset not found`、`invalid limit`、
// `cannot enqueue questions: dataset 3 has no domains`），而前端 `unwrap()` 会把
// `error` 字段**直接渲染给用户**，于是界面上出现英文长句甚至驱动原文。
//
// 本文件解决两个具体问题：
//
//  1. **文案用中文，并给出恢复路径**（功能说明.txt 要求「系统设计必须符合人机交互习惯」）。
//     只说「失败了」不够，还要让用户知道下一步能做什么。
//
//  2. **保留错误身份**。Go 的 `fmt.Errorf("中文: %w", err)` 会把被包装错误的
//     Error() 文本也拼进去 —— 若 err 是 pgx.ErrNoRows，结果仍是
//     「中文: no rows in result set」，照旧泄漏。而直接丢掉 err 又会让日志失去第一手信息。
//     因此用 userFacingError：Error() 只返回中文，Unwrap() 仍能定位原始错误。

// userFacingError 携带一段面向用户的中文文案，同时保留底层错误以便
// errors.Is / errors.As 与日志排查继续工作。
type userFacingError struct {
	msg   string
	cause error
}

func (e userFacingError) Error() string { return e.msg }

// Unwrap 让 errors.Is(err, pgx.ErrNoRows) 之类的判定继续成立。
// 这很重要：writeError 正是靠它把「记录不存在」稳定映射成 404。
func (e userFacingError) Unwrap() error { return e.cause }

// newUserFacingError 构造一个「文案干净、身份保留」的错误。
func newUserFacingError(msg string, cause error) error {
	return userFacingError{msg: msg, cause: cause}
}

// 面向用户的通用文案。
//
// 集中定义而不是在各 handler 里重复字面量：同一语义（「这个资源不存在」）
// 出现十几种英文写法正是本次缺陷的形态，收敛到一处才能防止再次发散。
const (
	msgDatasetNotFound     = "未找到该任务，请返回任务列表确认它是否已被删除"
	msgEvalRunNotFound     = "未找到该评估运行，请返回评估列表刷新后重试"
	msgCleaningRunNotFound = "未找到该清洗运行，请返回清洗页面刷新后重试"
	msgArtifactNotFound    = "未找到该导出文件，请先在导出页面重新生成交付文件"
	msgAuthRequired        = "登录状态已失效，请重新登录"
	msgAdminRequired       = "该操作仅管理员可用，请用管理员账号登录"
	msgProviderUnavailable = "该任务没有可用的 AI 服务，请到「系统设置 → AI 服务」配置后重试"
	msgNoDomains           = "该任务还没有主题结构，请先在「整理主题」页生成并确认领域"
	msgNoDirections        = "该任务还没有方向，请先到「整理主题」页生成方向并确认结构"
	msgNoQuestions         = "该任务还没有题目，请先在「问题生成」页生成题目"
	msgNoReasoning         = "该任务还没有答案内容，请先在「答案内容」页生成答案"
	msgNoRewardRecords     = "该任务还没有质量评估记录，请先在「质量评估」页完成评分"
	msgRewardsIncomplete   = "质量评估尚未覆盖全部题目，请等评估跑完或先补齐后再导出"
	msgReasoningIncomplete = "答案生成尚未覆盖全部题目，请等生成跑完后再继续"
	msgNoChainStepTargets  = "该任务下没有可用于生成标准步骤的方向，请先在「整理主题」页生成方向"
	msgStepsEmpty          = "标准步骤不能为空，请至少填写一步"
	msgRewardLevelsTooFew  = "打分档次至少需要两档（例如 -1 与 1），请先在任务设置里补齐"
	msgRunNotResumable     = "该阶段当前不可续跑；只有失败或部分失败的运行才能续跑"
	msgNoResumableRun      = "没有可续跑的运行记录，请直接重新发起该阶段"
	msgDuplicateEnqueue    = "该任务刚刚已提交过，系统为避免重复计算本次没有重复执行；请稍后刷新查看进度。若确认需要重跑，请等当前任务结束后再试"
	msgEnqueueFailed       = "任务提交失败，请稍后重试；若持续失败请联系管理员检查队列服务"
)
