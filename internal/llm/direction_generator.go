package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// DirectionGenerateInput 是方向生成（需求第 1 步的 m 层）的输入。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.1 节。
type DirectionGenerateInput struct {
	// Domain 是本次要展开的领域（level=1），其 ID 作为方向的 parent_id。
	Domain model.Domain
	// Count 是每个领域下要产出的方向数 m，由用户控制。
	Count int
	// Existing 是该领域下已存在的方向名，用于要求模型不要重复。
	Existing []string
}

// GenerateDirections 为一个领域生成 m 个下属方向（level=2）。
//
// 与 GenerateDomains 的差异：方向必须挂在具体领域下，因此 prompt 需要带上
// 领域名与已生成方向列表，且返回结果统一填充 ParentID 与 Level=2。
func GenerateDirections(ctx context.Context, provider ProviderConfig, input DirectionGenerateInput) ([]model.Domain, error) {
	if provider.ProviderType == "mock" {
		return nil, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return nil, fmt.Errorf("real provider configuration is incomplete")
	}
	if input.Domain.ID <= 0 {
		return nil, fmt.Errorf("direction generation requires a persisted parent domain")
	}
	if input.Count <= 0 {
		return nil, fmt.Errorf("direction count must be positive, got %d", input.Count)
	}

	prompt := buildDirectionPrompt(input.Domain.Name, input.Count, input.Existing)
	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}
	payload := map[string]any{
		"model":    provider.Model,
		"messages": messages,
		"stream":   true,
	}
	applyReasoningEffort(payload, provider)

	decoded, err := requestChatCompletion(ctx, provider, payload, 300*time.Second)
	if err != nil {
		return nil, fmt.Errorf("生成领域 %q 的方向失败: %w", input.Domain.Name, err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices for domain %q", input.Domain.Name)
	}

	var names []string
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &names); err != nil {
		return nil, fmt.Errorf("解析领域 %q 的方向失败: %w", input.Domain.Name, err)
	}

	parentID := input.Domain.ID
	seen := make(map[string]struct{}, len(input.Existing))
	for _, name := range input.Existing {
		seen[canonicalize(name)] = struct{}{}
	}

	directions := make([]model.Domain, 0, input.Count)
	for _, name := range names {
		cleaned := cleanDomainLabel(name)
		canonical := canonicalize(cleaned)
		if canonical == "" || looksLikeDNSName(canonical) {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		directions = append(directions, model.Domain{
			DatasetID:    input.Domain.DatasetID,
			Name:         cleaned,
			Canonical:    canonical,
			Level:        2,
			ParentID:     &parentID,
			Source:       "ai",
			ReviewStatus: "draft",
		})
		if len(directions) >= input.Count {
			break
		}
	}

	if len(directions) == 0 {
		return nil, fmt.Errorf("领域 %q 未产出任何可用方向", input.Domain.Name)
	}
	sort.Slice(directions, func(i, j int) bool { return directions[i].Name < directions[j].Name })
	return directions, nil
}

func buildDirectionPrompt(domainName string, count int, existing []string) string {
	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("围绕领域‘%s’生成 %d 个下属方向，用于后续为每个方向生成具体问题。返回 JSON 字符串数组。", domainName, count))
	builder.WriteString(" 每个方向必须是该领域下可直接展开成场景问题的具体子方向，例如领域‘海军’下的‘海上巡逻’、‘海上打击’、‘反潜作战’。")
	builder.WriteString(" 方向要简短、可复核、彼此有区分，粒度一致，不要编号，不要解释，不要输出对象。")
	builder.WriteString(" 严禁输出域名、网址、品牌名拼接词、拼音站点名、带 .com/.cn 等后缀的字符串。")
	if len(existing) > 0 {
		builder.WriteString(" 已生成方向，禁止重复：")
		limit := min(len(existing), 120)
		for index := 0; index < limit; index++ {
			if index > 0 {
				builder.WriteString("、")
			}
			builder.WriteString(existing[index])
		}
		builder.WriteString("。")
	}
	return builder.String()
}
