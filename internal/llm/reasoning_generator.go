package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

type reasoningPayload struct {
	Answer    string `json:"answer"`
	Reasoning string `json:"reasoning"`
}

// reasoningTimeout 是本阶段单次 LLM 调用的超时。
//
// 为什么从 90s 提到 300s（由 R11 lane 的取证发现，见 PR #82 的遗留风险）：
// 直连 provider 实测一道带具体场景的题目（prompt 517 tokens）需要 **84.5s**
// （completion_tokens=16634，其中 reasoning_tokens=14807）—— 紧贴 90s 上限，
// 实际运行中连续两次在 90s 被切断并写入 status=failed 占位行。
//
// 而同一代码库的其他生成阶段早就用 300s：
//   - question_generator_v2.go:16  questionGenTimeout = 300 * time.Second
//   - sft_generator.go:33          sftTimeout        = 300 * time.Second
//
// 本阶段产出的是**长链思考**，输出长度与问答/评分同级甚至更长，
// 没有理由给它一个只有别人三分之一的上限。对齐到 300s 让四者一致。
//
// 注：model_providers.timeout_seconds 已配置为 300，本常量与之同值；
// 这里不改成「读 provider 配置」是因为本阶段的调用链
// （ResolveProvider → ProviderConfig）当前不携带该字段，
// 为此扩展六个调用点的签名属于超出本次修复范围的改动。
const reasoningTimeout = 300 * time.Second

func GenerateReasoning(ctx context.Context, provider ProviderConfig, dataset model.Dataset, questions []model.Question, promptTemplate *model.PromptTemplate) ([]model.ReasoningRecord, map[int64]reasoningPayload, error) {
	records := make([]model.ReasoningRecord, 0, len(questions))
	payloads := map[int64]reasoningPayload{}
	for _, question := range questions {
		log.Printf("reasoning.generate.question.start dataset_id=%d question_id=%d", dataset.ID, question.ID)
		generated, err := generateReasoningForQuestion(ctx, provider, dataset, question, promptTemplate)
		status := "generated"
		if err != nil {
			if errors.Is(err, ErrInvalidContent) {
				// 模型答上来了，但内容是占位/无效的。保留原始输出（供人工核查
				// 模型到底返回了什么），只把状态标成 invalid。
				status = ContentStatusInvalid
				log.Printf("reasoning.generate.question.invalid dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
			} else {
				log.Printf("reasoning.generate.question.error dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
				generated = reasoningPayload{
					Answer:    fmt.Sprintf("生成失败（question_id=%d）: %v", question.ID, err),
					Reasoning: "",
				}
				status = "failed"
			}
		}
		payloads[question.ID] = generated
		records = append(records, model.ReasoningRecord{
			DatasetID:     dataset.ID,
			QuestionID:    question.ID,
			QuestionText:  question.Content,
			AnswerSummary: generated.Answer,
			Reasoning:     generated.Reasoning,
			Status:        status,
		})
		log.Printf("reasoning.generate.question.done dataset_id=%d question_id=%d status=%s", dataset.ID, question.ID, status)
	}
	return records, payloads, nil
}

func generateReasoningForQuestion(ctx context.Context, provider ProviderConfig, dataset model.Dataset, question model.Question, promptTemplate *model.PromptTemplate) (reasoningPayload, error) {
	if provider.ProviderType == "mock" {
		return reasoningPayload{}, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return reasoningPayload{}, fmt.Errorf("real provider configuration is incomplete")
	}

	systemPrompt := "You generate long-form reasoning data. Return JSON with answer and reasoning fields only."
	userPrompt := fmt.Sprintf("Question: %s", question.Content)
	if promptTemplate != nil {
		if strings.TrimSpace(promptTemplate.SystemPrompt) != "" {
			systemPrompt = promptTemplate.SystemPrompt
		}
		if strings.TrimSpace(promptTemplate.UserPrompt) != "" {
			userPrompt = strings.ReplaceAll(promptTemplate.UserPrompt, "{{question}}", question.Content)
			userPrompt = strings.ReplaceAll(userPrompt, "{{rootKeyword}}", dataset.RootKeyword)
		}
	}
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	applyReasoningEffort(payload, provider)
	decoded, err := requestChatCompletion(ctx, provider, payload, reasoningTimeout)
	if err != nil {
		return reasoningPayload{}, err
	}
	if len(decoded.Choices) == 0 {
		return reasoningPayload{}, fmt.Errorf("provider returned no choices")
	}

	if len(decoded.Choices) == 0 {
		return reasoningPayload{}, fmt.Errorf("provider returned no choices")
	}
	var generated reasoningPayload
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &generated); err != nil {
		// JSON 解析失败属于「模型没按约定格式作答」。此前这里把原始文本当作
		// answer 并返回 nil 错误，导致非 JSON 输出被判为 generated；现在如实
		// 返回解析错误，由调用方标记为 failed。
		//
		// 残留的兜底：模型返回了可读文本但完全不是 JSON 时，把文本作为 answer
		// 保留下来供人工核查，错误仍需透出。
		fallback := strings.TrimSpace(decoded.Choices[0].Message.Content)
		if fallback != "" {
			payloadsOfLastResort := reasoningPayload{Answer: fallback, Reasoning: ""}
			return payloadsOfLastResort, err
		}
		return reasoningPayload{}, err
	}

	// 结构层通过不代表内容可用：模型会返回合法 JSON 但字段是占位符
	// （实测 {"answer":"...","reasoning":"..."}）。
	if assessment := AssessReasoningContent(generated.Answer, generated.Reasoning); !assessment.Valid {
		return generated, newInvalidContentError(assessment)
	}
	return generated, nil
}
