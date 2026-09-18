package eval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L7 lane 独占：数据集评估的裁判模型接入。
//
// 需求：多 LLM 互评。若数据集 A1 由模型 A 生成，评估 A1 必须使用 A 之外的模型，
// 否则模型会给自己生成的数据打高分，评估结论失去意义。
//
// 剔除判定集中在 ResolveJudges（纯函数，便于单测）；密钥解析与 DB 读取在 LoadJudgeRefs。

// JudgeRef 一名可用于评分的裁判模型。
//
// APIKey 只在 LoadJudgeRefs 之后才有值：ListProviders 不返回密钥，
// 需要按 provider 单独取密文解密（GetProviderWithSecret）。
type JudgeRef struct {
	ProviderID   int64
	ProviderName string
	Model        string
	BaseURL      string
	ProviderType string
	APIKey       string
}

// 剔除原因。写入 eval_run_judges.exclude_reason，前端据此向用户解释为何某个模型不可用。
const (
	ExcludeReasonGenerator  = "生成者模型，禁止自评"
	ExcludeReasonSameSource = "与生成者同源（相同 BaseURL + Model），禁止自评"
	ExcludeReasonInactive   = "provider 未启用"
	ExcludeReasonNoAPIKey   = "provider 未配置 API Key"
)

// sourceKey 归一化「模型来源」，用于识别同源 provider。
//
// 同一个模型可能在 model_providers 里注册成多行（不同名字、不同 id）。
// 只比对 provider id 会漏掉这种情况，自评依然会发生。
func sourceKey(provider model.ModelProvider) string {
	base := strings.ToLower(strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/"))
	modelName := strings.ToLower(strings.TrimSpace(provider.Model))
	if base == "" || modelName == "" {
		return ""
	}
	return base + "|" + modelName
}

// ResolveJudges 从候选 provider 中挑出裁判，剔除生成者自身与其同源模型。
//
// 纯函数：不查 DB、不发网络请求，因此可以脱离环境单测。
//
// 返回值：
//   - 第一个：可用的裁判列表（已剔除）
//   - 第二个：全部候选的落库记录，**包含被剔除项**（excluded=true + 原因），
//     供 eval_run_judges 写入，使「为什么这个模型没参与」可追溯
//
// generatorProviderID 为 0 表示生成者未知，此时不做剔除（也不做同源比对）。
// APIKey 不在本函数填充，由 LoadJudgeRefs 补齐。
func ResolveJudges(_ context.Context, providers []model.ModelProvider, generatorProviderID int64) ([]JudgeRef, []model.EvalRunJudge, error) {
	seen := make(map[int64]struct{}, len(providers))
	for _, provider := range providers {
		if provider.ID <= 0 {
			return nil, nil, fmt.Errorf("provider id must be positive, got %d", provider.ID)
		}
		if _, duplicated := seen[provider.ID]; duplicated {
			// eval_run_judges 上有 UNIQUE(eval_run_id, provider_id)，
			// 重复项会在写库时才报错，提前拦下并说明原因。
			return nil, nil, fmt.Errorf("duplicate provider id %d in candidate list", provider.ID)
		}
		seen[provider.ID] = struct{}{}
	}

	generatorSource := ""
	if generatorProviderID != 0 {
		for _, provider := range providers {
			if provider.ID == generatorProviderID {
				generatorSource = sourceKey(provider)
				break
			}
		}
	}

	judges := make([]JudgeRef, 0, len(providers))
	records := make([]model.EvalRunJudge, 0, len(providers))
	for _, provider := range providers {
		reason := ""
		switch {
		case generatorProviderID != 0 && provider.ID == generatorProviderID:
			reason = ExcludeReasonGenerator
		case generatorSource != "" && sourceKey(provider) == generatorSource:
			reason = ExcludeReasonSameSource
		case !provider.IsActive:
			reason = ExcludeReasonInactive
		}

		excluded := reason != ""
		records = append(records, model.EvalRunJudge{
			ProviderID:    provider.ID,
			ProviderName:  provider.Name,
			Model:         provider.Model,
			Excluded:      excluded,
			ExcludeReason: reason,
			Status:        "pending",
		})
		if excluded {
			continue
		}
		judges = append(judges, JudgeRef{
			ProviderID:   provider.ID,
			ProviderName: provider.Name,
			Model:        provider.Model,
			BaseURL:      provider.BaseURL,
			ProviderType: provider.ProviderType,
		})
	}
	return judges, records, nil
}

// LoadJudgeRefs 读取全部 provider，解析裁判并补齐解密后的 API Key。
//
// 与 ResolveJudges 的区别：本函数做 I/O。缺 API Key 的候选会被追加剔除
// （原因 ExcludeReasonNoAPIKey）而不是留到评分时才静默失败——
// 本地环境若只配了生成者 provider，全部候选都会被剔除，这是正确行为而非缺陷。
func LoadJudgeRefs(ctx context.Context, adminStore *store.AdminStore, generatorProviderID int64) ([]JudgeRef, []model.EvalRunJudge, error) {
	if adminStore == nil {
		return nil, nil, fmt.Errorf("admin store is required")
	}

	providers, err := adminStore.ListProviders(ctx)
	if err != nil {
		return nil, nil, err
	}

	judges, records, err := ResolveJudges(ctx, providers, generatorProviderID)
	if err != nil {
		return nil, nil, err
	}

	usable := make([]JudgeRef, 0, len(judges))
	for _, judge := range judges {
		full, err := adminStore.GetProviderWithSecret(ctx, judge.ProviderID)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(full.APIKey) == "" {
			markExcluded(records, judge.ProviderID, ExcludeReasonNoAPIKey)
			continue
		}
		judge.APIKey = full.APIKey
		judge.BaseURL = full.BaseURL
		judge.Model = full.Model
		judge.ProviderType = full.ProviderType
		usable = append(usable, judge)
	}
	return usable, records, nil
}

// markExcluded 把某条落库记录标记为剔除，并同步移出可用裁判集合的判定依据。
func markExcluded(records []model.EvalRunJudge, providerID int64, reason string) {
	for index := range records {
		if records[index].ProviderID != providerID {
			continue
		}
		records[index].Excluded = true
		records[index].ExcludeReason = reason
		return
	}
}

// ProviderConfig 把裁判引用转成 llm 调用配置。
func (j JudgeRef) ProviderConfig() llm.ProviderConfig {
	return llm.ProviderConfig{
		BaseURL:      j.BaseURL,
		Model:        j.Model,
		ProviderType: j.ProviderType,
		APIKey:       j.APIKey,
	}
}

// ScoreJSON 调用裁判模型并把其 JSON 正文解析进 target。
//
// 这是评分任务唯一的裁判调用入口：SSE 流式解析、重试、reasoning_content 与正文分离
// 全部封装在 internal/llm（judge_client.go），本包不重复实现 HTTP 细节。
func ScoreJSON(ctx context.Context, judge JudgeRef, systemPrompt, userPrompt string, target any, timeout time.Duration) error {
	if judge.ProviderID <= 0 {
		return fmt.Errorf("judge provider is required")
	}
	return llm.CompleteStructured(ctx, judge.ProviderConfig(), systemPrompt, userPrompt, target, timeout)
}
