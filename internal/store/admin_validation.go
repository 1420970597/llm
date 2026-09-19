package store

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/1420970597/llm/internal/model"
)

// ValidationError 表示配置项的字段校验失败。
//
// 为什么需要独立类型：handler 必须把「用户填错字段」（400）与「服务端故障」（500）
// 分开。若只用文本错误，handler 只能靠字符串匹配，非常脆弱；这里用类型 +
// errors.As 让 apps/api 稳定判定，同时 err.Error() 本身就是可直接展示给用户的中文提示。
type ValidationError struct {
	Field   string // 字段的中文标签，与表单文案一致，直接面向用户
	JSONKey string // 请求体里的字段名，便于脚本与前端定位
	Reason  string // 具体原因；为空表示「必填」
}

func (e *ValidationError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("%s（%s）必填", e.Field, e.JSONKey)
	}
	return fmt.Sprintf("%s（%s）%s", e.Field, e.JSONKey, e.Reason)
}

func requiredField(field, jsonKey string) error {
	return &ValidationError{Field: field, JSONKey: jsonKey}
}

func invalidField(field, jsonKey, reason string) error {
	return &ValidationError{Field: field, JSONKey: jsonKey, Reason: reason}
}

// 校验发生在 Upsert* 的第一条语句 —— 早于加密与任何写库动作，
// 因此既不依赖数据库约束兜底，也不会留下半成品行。
//
// 为什么不把校验放在 apps/api 的 handler 里（issue #63 的根因）：
//  1. 4 类配置 x POST/PUT 共 8 个入口，逐处复制校验必然漂移；
//  2. Upsert* 还有非 HTTP 调用方（启动时的 EnsureProvider 引导、GRPO 夹具等），
//     handler 层校验管不到它们。issue 的验收标准是「不再可能写入脏行」，
//     而不是「从这一个端点不再可能写入脏行」。

// maxStrategyDomainCount 是生成策略里领域数的上限。
//
// 取 1000 不是新发明的阈值：internal/store/dataset_store.go 的 Estimate 已经用
// min(max(10, targetSize/questionsPerDomain), 1000) 把领域数夹在 1000 以内。
// 若配置允许更大的值，界面会显示一个永远不会被执行的估算，属于「假承诺」。
const maxStrategyDomainCount = 1000

// promptTemplateStages 是 prompt_templates.stage 的允许取值。
//
// 这个集合不是凭模板猜的，而是「代码里真实会按 stage 查库」的全部取值：
//
//	apps/api/datasets.go             domain-generation
//	apps/worker/main.go              question-generation
//	apps/worker/job_questions_v2.go  question-generation
//	apps/worker/main.go              reasoning-generation
//	apps/worker/main.go              reward-generation
//	apps/worker/job_sft.go           sft-generation
//
// 存在的理由：stage 写错的模板会被成功保存且 is_active=TRUE，但永远不会被任何阶段
// 读到 —— 用户以为「指令已启用」，实际生成阶段仍走内置提示词。这与 issue #63 报的
// 「启用中的空模板导致生成阶段拿到空 prompt」是同一类静默失效。
// 新增生成阶段时必须同步这里，否则新阶段无法保存模板（这是刻意的：宁可报错，不要静默无效）。
var promptTemplateStages = []string{
	"domain-generation",
	"question-generation",
	"reasoning-generation",
	"reward-generation",
	"sft-generation",
}

// ValidateProviderInput 校验 AI 服务配置。
//
// name/baseUrl/model 是运行期真正被用到的三个字段：internal/llm/openai_client.go
// 用 BaseURL 拼 /chat/completions，provider.Model 直接进请求体。任一为空都会在生成
// 阶段才失败，而那时用户已经建好了任务。maxConcurrency/timeoutSeconds 是计数与秒数，
// 0 表示「不限/立即超时」，都不是可用配置。
func ValidateProviderInput(input model.ModelProvider) error {
	if strings.TrimSpace(input.Name) == "" {
		return requiredField("服务名称", "name")
	}
	if strings.TrimSpace(input.BaseURL) == "" {
		return requiredField("基础 URL", "baseUrl")
	}
	if err := validateHTTPURL("基础 URL", "baseUrl", input.BaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(input.Model) == "" {
		return requiredField("模型名称", "model")
	}
	if input.MaxConcurrency < 1 {
		return invalidField("最大并发数", "maxConcurrency", "必须大于 0")
	}
	if input.TimeoutSeconds < 1 {
		return invalidField("超时秒数", "timeoutSeconds", "必须大于 0")
	}
	return nil
}

// ValidateStorageProfileInput 校验结果存储配置。
//
// endpoint 必须是完整 http(s) 地址：internal/storage/object_store.go 的 ParseEndpoint
// 用 url.Parse 取 Host，非法地址会得到空 Host，直到导出落盘时才报错。
//
// accessKeyId 刻意不设为必填：S3 兼容端点允许匿名访问，把它强制为非空会挡住合法用法。
// 真正的「空表单」仍然会被 name/provider/endpoint/bucket 四项拦下。
func ValidateStorageProfileInput(input model.StorageProfile) error {
	if strings.TrimSpace(input.Name) == "" {
		return requiredField("配置名称", "name")
	}
	if strings.TrimSpace(input.Provider) == "" {
		return requiredField("存储提供方", "provider")
	}
	if strings.TrimSpace(input.Endpoint) == "" {
		return requiredField("端点", "endpoint")
	}
	if err := validateHTTPURL("端点", "endpoint", input.Endpoint); err != nil {
		return err
	}
	if strings.TrimSpace(input.Bucket) == "" {
		return requiredField("存储桶", "bucket")
	}
	return nil
}

// ValidateStrategyInput 校验生成策略。
//
// issue #63 的关键证据是「空表单写出 domainCount=1000 且 isDefault=true 的默认策略」。
// 空表单之所以能落库，是因为 name 完全没校验；四个数量字段则为 0 —— 0 个领域 /
// 0 个问题的策略同样不可用。因此这里要求 name 非空、四个数量字段均为正数，
// 并把领域数限制在 dataset_store 既有的 1000 上限内。
func ValidateStrategyInput(input model.GenerationStrategy) error {
	if strings.TrimSpace(input.Name) == "" {
		return requiredField("策略名称", "name")
	}
	if input.DomainCount < 1 || input.DomainCount > maxStrategyDomainCount {
		return invalidField("领域数", "domainCount", fmt.Sprintf("必须为 1~%d 的整数", maxStrategyDomainCount))
	}
	if input.QuestionsPerDomain < 1 {
		return invalidField("每领域问题数", "questionsPerDomain", "必须大于 0")
	}
	if input.AnswerVariants < 1 {
		return invalidField("答案变体数", "answerVariants", "必须大于 0")
	}
	if input.RewardVariants < 1 {
		return invalidField("奖励变体数", "rewardVariants", "必须大于 0")
	}
	return nil
}

// ValidatePromptInput 校验生成指令模板。
//
// systemPrompt 与 userPrompt 允许其一为空（调用方可能只用系统指令），
// 但两者同时为空等于没有模板 —— 而模板默认 is_active=TRUE，会被
// GetActivePromptByStage 选中并把空指令交给模型。
func ValidatePromptInput(input model.PromptTemplate) error {
	if strings.TrimSpace(input.Name) == "" {
		return requiredField("模板名称", "name")
	}
	if strings.TrimSpace(input.Stage) == "" {
		return requiredField("阶段", "stage")
	}
	if !slicesContains(promptTemplateStages, strings.TrimSpace(input.Stage)) {
		return invalidField("阶段", "stage", fmt.Sprintf("必须是以下之一：%s", strings.Join(promptTemplateStages, "、")))
	}
	if strings.TrimSpace(input.SystemPrompt) == "" && strings.TrimSpace(input.UserPrompt) == "" {
		return invalidField("系统指令或用户指令", "systemPrompt/userPrompt", "至少填写一项")
	}
	return nil
}

// validateHTTPURL 校验「必须是完整 http(s) 地址」这一共同约定。
func validateHTTPURL(field, jsonKey, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return invalidField(field, jsonKey, "必须是 http:// 或 https:// 开头的完整地址")
	}
	return nil
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
