package llm

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// questionGenTimeout 单次问题生成调用的超时。
// 当前配置的推理型模型单次响应可达 120 秒（思考 + 正文），留足余量。
const questionGenTimeout = 300 * time.Second

// questionGenMaxRounds 单个方向最多尝试几轮生成。
// 模型可能返回不足数量或返回重复，多轮补齐；轮次上限避免无限循环。
const questionGenMaxRounds = 3

// defaultDifficultyMix 用户未指定难度配比时的默认值。
var defaultDifficultyMix = map[string]float64{
	DifficultyEasy:   0.3,
	DifficultyMedium: 0.5,
	DifficultyHard:   0.2,
}

// DirectionContext 一个「方向」及其长链思维标准步骤。
// 方向是 level=2 的 domain，其长链标准步骤作为问题生成的思考框架。
type DirectionContext struct {
	DomainID   int64
	DomainName string
	ChainSteps []model.ChainStep
}

// QuestionGenInput 是 v2 问题生成的全部输入。
type QuestionGenInput struct {
	DatasetID             int64
	RootKeyword           string
	QuestionsPerDirection int
	DifficultyMix         map[string]float64
	Directions            []DirectionContext
	SystemPrompt          string
	UserPrompt            string
}

// questionDraft 是模型返回的单条问题草稿。
type questionDraft struct {
	Content    string `json:"content"`
	Difficulty string `json:"difficulty"`
}

// AllocateDifficultyMix 用最大余数法把 total 个问题按配比精确分配到各难度档。
//
// 保证：返回的各档数量之和恰好等于 total（当 total > 0 且配比有效时）。
// 例：total=10，mix={easy:0.3, medium:0.5, hard:0.2} → easy=3, medium=5, hard=2。
//
// mix 为空或无效时回退到 defaultDifficultyMix。
func AllocateDifficultyMix(total int, mix map[string]float64) map[string]int {
	allocation := map[string]int{DifficultyEasy: 0, DifficultyMedium: 0, DifficultyHard: 0}
	if total <= 0 {
		return allocation
	}

	normalized := NormalizeDifficultyMix(mix)
	if len(normalized) == 0 {
		normalized = NormalizeDifficultyMix(defaultDifficultyMix)
	}
	if len(normalized) == 0 {
		allocation[DifficultyMedium] = total
		return allocation
	}

	// 只对配比中出现的档位分配，其余档位保持 0。
	levels := make([]string, 0, len(normalized))
	for level := range normalized {
		levels = append(levels, level)
	}
	sort.Strings(levels)

	type remainder struct {
		level string
		frac  float64
	}
	remainders := make([]remainder, 0, len(levels))
	assigned := 0
	for _, level := range levels {
		exact := normalized[level] * float64(total)
		// 加极小量抵消浮点误差（0.3*10 在 float64 下为 2.9999999999999996）。
		floored := float64(int(exact + 1e-9))
		if floored < 0 {
			floored = 0
		}
		allocation[level] = int(floored)
		assigned += int(floored)
		remainders = append(remainders, remainder{level: level, frac: exact - floored})
	}

	// 余数按小数部分从大到小补 1，保证总数精确等于 total。
	sort.SliceStable(remainders, func(i, j int) bool {
		return remainders[i].frac > remainders[j].frac
	})
	for index := 0; assigned < total; index++ {
		remainders[index%len(remainders)].level = remainders[index%len(remainders)].level
		level := remainders[index%len(remainders)].level
		allocation[level]++
		assigned++
	}
	return allocation
}

// ReconcileDifficulty 把模型返回的难度标签归一化到 easy/medium/hard。
//
// 模型可能返回数字（"1"/"2"/"3"）、中文（"简单"/"中等"/"困难"）、大小写变体，
// 或完全无法识别的值。无法识别时归为 medium，绝不丢弃该问题。
func ReconcileDifficulty(raw string) (string, int) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case DifficultyEasy, "1", "低", "简单", "容易", "basic", "simple":
		return DifficultyEasy, DifficultyScoreOf(DifficultyEasy)
	case DifficultyMedium, "2", "中", "中等", "一般", "moderate", "normal":
		return DifficultyMedium, DifficultyScoreOf(DifficultyMedium)
	case DifficultyHard, "3", "高", "困难", "难", "advanced", "complex":
		return DifficultyHard, DifficultyScoreOf(DifficultyHard)
	default:
		return DifficultyMedium, DifficultyScoreOf(DifficultyMedium)
	}
}

// GenerateQuestionsV2 按方向、长链思维标准步骤与难度配比为每个方向生成 x 个问题。
//
// x 由 input.QuestionsPerDirection 控制（用户可控）；难度按 input.DifficultyMix
// 用最大余数法分配；同一批次内按 DedupeKey 去重。
func GenerateQuestionsV2(ctx context.Context, provider ProviderConfig, input QuestionGenInput) ([]model.Question, error) {
	if provider.ProviderType == "mock" {
		return nil, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return nil, fmt.Errorf("real provider configuration is incomplete")
	}
	if len(input.Directions) == 0 {
		return nil, fmt.Errorf("no directions provided for question generation")
	}

	perDirection := max(input.QuestionsPerDirection, 1)
	allocation := AllocateDifficultyMix(perDirection, input.DifficultyMix)

	questions := make([]model.Question, 0, perDirection*len(input.Directions))
	for _, direction := range input.Directions {
		log.Printf("questions.v2.direction.start dataset_id=%d direction_id=%d direction=%q target=%d",
			input.DatasetID, direction.DomainID, direction.DomainName, perDirection)
		generated, err := generateForDirection(ctx, provider, input, direction, perDirection, allocation)
		if err != nil {
			log.Printf("questions.v2.direction.error dataset_id=%d direction_id=%d err=%v",
				input.DatasetID, direction.DomainID, err)
			return nil, err
		}
		questions = append(questions, generated...)
		log.Printf("questions.v2.direction.done dataset_id=%d direction_id=%d generated=%d",
			input.DatasetID, direction.DomainID, len(generated))
	}
	return questions, nil
}

// difficultyPlan 把难度配额展开成有序序列，供逐条分配使用。
//
// 顺序固定为 easy → medium → hard，使同一配比下的分配结果可重现。
// 例：allocation={easy:3,medium:5,hard:2} → [easy,easy,easy,medium×5,hard,hard]。
func difficultyPlan(allocation map[string]int, total int) []string {
	plan := make([]string, 0, total)
	for _, level := range []string{DifficultyEasy, DifficultyMedium, DifficultyHard} {
		for count := allocation[level]; count > 0; count-- {
			plan = append(plan, level)
		}
	}
	// 配额不足 total 时（理论上不会发生，AllocateDifficultyMix 已保证相等）
	// 用 medium 补齐，避免下标越界。
	for len(plan) < total {
		plan = append(plan, DifficultyMedium)
	}
	return plan
}

// generateForDirection 为单个方向生成目标数量的问题，多轮补齐并去重。
//
// 难度以计划为准：`allocation` 是本方向按用户配比算出的各档数量，
// 最终每条问题的 difficulty 由 difficultyPlan 逐条分配，而不是照抄模型自报的
// 标签。原因：模型经常不遵守提示词里的难度要求（实测把 hard 配额 0 的请求
// 返回成一半 hard），若照抄则用户的 difficultyMix 形同虚设。模型自报值与计划
// 不一致时记日志，便于观察提示词遵循度。
func generateForDirection(ctx context.Context, provider ProviderConfig, input QuestionGenInput,
	direction DirectionContext, target int, allocation map[string]int) ([]model.Question, error) {

	collected := make([]questionDraft, 0, target)
	seen := map[string]struct{}{}
	produced := map[string]int{}
	plan := difficultyPlan(allocation, target)

	for round := 0; round < questionGenMaxRounds && len(collected) < target; round++ {
		needed := target - len(collected)
		quota := remainingQuota(allocation, produced, needed)

		raw, err := requestQuestionDrafts(ctx, provider, input, direction, needed, quota)
		if err != nil {
			return nil, err
		}
		drafts, err := parseQuestionDrafts(raw)
		if err != nil {
			return nil, err
		}
		if len(drafts) == 0 {
			log.Printf("questions.v2.round.empty dataset_id=%d direction_id=%d round=%d",
				input.DatasetID, direction.DomainID, round)
			continue
		}

		added := 0
		for _, draft := range drafts {
			content := strings.TrimSpace(draft.Content)
			if content == "" {
				continue
			}
			key := DedupeKey(content)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}

			// 难度以计划为准，模型自报值仅用于记录偏差。
			planned := plan[len(collected)]
			if reported, _ := ReconcileDifficulty(draft.Difficulty); reported != planned {
				log.Printf("questions.v2.difficulty.mismatch dataset_id=%d direction_id=%d planned=%s model=%s",
					input.DatasetID, direction.DomainID, planned, reported)
			}

			collected = append(collected, questionDraft{Content: content, Difficulty: planned})
			produced[planned]++
			added++
			if len(collected) >= target {
				break
			}
		}
		log.Printf("questions.v2.round.done dataset_id=%d direction_id=%d round=%d added=%d skipped=%d total=%d",
			input.DatasetID, direction.DomainID, round, added, len(drafts)-added, len(collected))
	}

	if len(collected) == 0 {
		return nil, fmt.Errorf("provider returned no questions for direction %q", direction.DomainName)
	}
	if len(collected) < target {
		// 不视为失败：已产出的问题全部保留，缺口记入日志由调用方决定是否重试。
		log.Printf("questions.v2.direction.shortfall dataset_id=%d direction_id=%d got=%d want=%d",
			input.DatasetID, direction.DomainID, len(collected), target)
	}

	questions := make([]model.Question, 0, len(collected))
	for _, draft := range collected {
		_, score := ReconcileDifficulty(draft.Difficulty)
		questions = append(questions, model.Question{
			DatasetID:         input.DatasetID,
			DomainID:          direction.DomainID,
			DomainName:        direction.DomainName,
			DirectionDomainID: direction.DomainID,
			Content:           draft.Content,
			CanonicalHash:     store.CanonicalHash(draft.Content),
			DedupeKey:         DedupeKey(draft.Content),
			Difficulty:        draft.Difficulty,
			DifficultyScore:   score,
			Source:            "ai",
			CleaningStatus:    "clean",
			Status:            "generated",
		})
	}
	return questions, nil
}

// remainingQuota 计算本轮各档还需要生成多少条，用于把剩余配额写进 prompt。
func remainingQuota(allocation, produced map[string]int, needed int) map[string]int {
	quota := map[string]int{}
	remaining := 0
	for level, planned := range allocation {
		gap := planned - produced[level]
		if gap <= 0 {
			continue
		}
		quota[level] = gap
		remaining += gap
	}
	if remaining == 0 {
		// 已产出的档位分布超出计划（模型自报难度与计划不符），本轮按总量补齐。
		quota[DifficultyMedium] = needed
	}
	return quota
}

// requestQuestionDrafts 调用模型生成问题，返回原始响应正文。
func requestQuestionDrafts(ctx context.Context, provider ProviderConfig, input QuestionGenInput,
	direction DirectionContext, count int, quota map[string]int) (string, error) {

	systemPrompt := "你是训练数据构造专家，负责产出具体、可解、互不重复的场景化问题。只返回 JSON，不要解释。"
	if strings.TrimSpace(input.SystemPrompt) != "" {
		systemPrompt = input.SystemPrompt
	}

	prompt := buildQuestionPrompt(input, direction, count, quota)
	if strings.TrimSpace(input.UserPrompt) != "" {
		prompt = renderQuestionTemplate(input.UserPrompt, input, direction, count)
	}

	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
	}
	applyReasoningEffort(payload, provider)

	decoded, err := requestChatCompletion(ctx, provider, payload, questionGenTimeout)
	if err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("provider returned no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

// buildQuestionPrompt 组装问题生成提示词，包含场景、长链框架与难度配额。
func buildQuestionPrompt(input QuestionGenInput, direction DirectionContext, count int, quota map[string]int) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("主题：%s\n", input.RootKeyword))
	builder.WriteString(fmt.Sprintf("方向：%s\n", direction.DomainName))

	if framework := renderChainSteps(direction.ChainSteps); framework != "" {
		builder.WriteString("\n该方向的长链思维标准步骤（生成的问题必须能触发这条完整思考链）：\n")
		builder.WriteString(framework)
		builder.WriteString("\n")
	}

	builder.WriteString(fmt.Sprintf("\n请生成 %d 个具体问题。每个问题都要是一个带具体场景的可解任务，", count))
	builder.WriteString("包含明确的背景设定与待决策点，避免空泛提问。\n")
	builder.WriteString("难度配额：")
	builder.WriteString(renderQuota(quota))
	builder.WriteString("\n\n要求：\n")
	builder.WriteString("1. 每个问题必须彼此不同，不得改写同一场景换词充数\n")
	builder.WriteString("2. 问题必须与上述长链思维标准步骤对应，能据此逐步推理\n")
	builder.WriteString("3. 只返回 JSON 数组，每个元素形如 {\"content\":\"问题正文\",\"difficulty\":\"easy|medium|hard\"}\n")
	builder.WriteString("4. 不要输出 Markdown 代码块，不要输出任何解释文字")
	return builder.String()
}

// renderQuestionTemplate 用管理端配置的模板渲染提示词。
func renderQuestionTemplate(template string, input QuestionGenInput, direction DirectionContext, count int) string {
	rendered := template
	rendered = strings.ReplaceAll(rendered, "{{rootKeyword}}", input.RootKeyword)
	rendered = strings.ReplaceAll(rendered, "{{domainName}}", direction.DomainName)
	rendered = strings.ReplaceAll(rendered, "{{count}}", fmt.Sprintf("%d", count))
	rendered = strings.ReplaceAll(rendered, "{{chainSteps}}", renderChainSteps(direction.ChainSteps))
	return rendered
}

// renderChainSteps 把长链思维标准步骤渲染成提示词里的编号列表。
func renderChainSteps(steps []model.ChainStep) string {
	if len(steps) == 0 {
		return ""
	}
	var builder strings.Builder
	for index, step := range steps {
		title := strings.TrimSpace(step.Title)
		if title == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("%d. %s", index+1, title))
		if description := strings.TrimSpace(step.Description); description != "" {
			builder.WriteString("：")
			builder.WriteString(description)
		}
		if checkpoint := strings.TrimSpace(step.Checkpoint); checkpoint != "" {
			builder.WriteString("（判断点：")
			builder.WriteString(checkpoint)
			builder.WriteString("）")
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// renderQuota 把难度配额渲染成人类可读文本。
func renderQuota(quota map[string]int) string {
	order := []string{DifficultyEasy, DifficultyMedium, DifficultyHard}
	labels := map[string]string{DifficultyEasy: "简单", DifficultyMedium: "中等", DifficultyHard: "困难"}
	parts := make([]string, 0, len(order))
	for _, level := range order {
		if count, ok := quota[level]; ok && count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 个", labels[level], count))
		}
	}
	if len(parts) == 0 {
		return "不限"
	}
	return strings.Join(parts, "、")
}

// parseQuestionDrafts 解析模型返回的问题草稿。
// 兼容对象数组（含 difficulty）与纯字符串数组两种形态。
func parseQuestionDrafts(raw string) ([]questionDraft, error) {
	var structured []questionDraft
	if err := unmarshalStructuredContent(raw, &structured); err == nil && len(structured) > 0 {
		drafts := make([]questionDraft, 0, len(structured))
		for _, item := range structured {
			if strings.TrimSpace(item.Content) == "" {
				continue
			}
			drafts = append(drafts, item)
		}
		if len(drafts) > 0 {
			return drafts, nil
		}
	}

	var plain []string
	if err := unmarshalStructuredContent(raw, &plain); err != nil {
		return nil, err
	}
	drafts := make([]questionDraft, 0, len(plain))
	for _, text := range plain {
		content := strings.TrimSpace(text)
		if content == "" {
			continue
		}
		drafts = append(drafts, questionDraft{Content: content})
	}
	if len(drafts) == 0 {
		return nil, fmt.Errorf("provider returned no usable questions")
	}
	return drafts, nil
}
