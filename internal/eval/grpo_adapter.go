package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

// 本文件是 GRPO 的质量适配器（Issue #160 T24）。
//
// 契约：docs/plans/atelier-implementation.md §2.2（GRPO payload 字段）、
// §2.3（分母与覆盖）、§2.6（缺分语义）；#160 T24 的原文要求。
//
// 三条与「不能复用 SFT 打分逻辑」直接相关的实现决定：
//
//  1. **不向裁判要 reasoning/answer**：GRPO 样本里根本没有这两个字段。
//     沿用 SFT 的提示词会让模型对着空内容打分，产出「看起来正常、
//     其实语义错误」的结论 —— 那正是 T24 要防的。
//
//  2. **缺分与 0 分区分**：无法判定的维度返回 State=missing、Score=nil；
//     0 是一个真实分数（「质量最差」），把它当成「没判定」会让报告显示
//     「这道题判得很差」，而事实是「没有依据可判」。
//
//  3. **边界稳定性基于冻结参考集逐条判定**：裁判对参考集里每一条给出
//     accept/reject/undecidable，适配器再与冻结标注比对。这样：
//       - 同一参考集重复跑 → 差异能被复算（分数 + 逐条理由都在报告里）；
//       - 不同裁判的差异可追溯到**具体哪一条**边界样例判得不一样；
//       - 没有参考集时**不编造**分数，直接记缺分（T24 原文）。

// GRPOVerdict 是适配器给出的一个维度判定（中立于 studio 的 JudgeVerdict）。
//
// 不直接返回 studio.JudgeVerdict：internal/eval 不依赖 internal/studio
// （studio 已经依赖 store/model，再双向依赖会让「评估逻辑」与「执行编排」
// 绑死，而它们需要能各自单测）。worker 负责这一层映射。
type GRPOVerdict struct {
	Dimension  string
	RawScore   *float64
	State      string
	Rationale  string
	ErrorClass string
}

// GRPOJudgeRequest 是一次 GRPO 裁判调用的输入。
//
// 全部来自实验的**冻结快照**与样本的不可变内容：裁判只读，不查当前配置。
type GRPOJudgeRequest struct {
	Sample     model.GRPOSample
	Config     model.GRPOTargetConfig
	Dimensions []string
	JudgeLabel string
}

// GRPO 边界判定的三种结果。
const (
	grpoBoundaryAccept      = "accept"
	grpoBoundaryReject      = "reject"
	grpoBoundaryUndecidable = "undecidable"
)

// grpoJudgePayload 是裁判返回的 JSON 形状。
type grpoJudgePayload struct {
	// Boundary 是逐条边界参考样例的判定结果。
	Boundary []struct {
		Index     int    `json:"index"`
		Verdict   string `json:"verdict"`
		Rationale string `json:"rationale"`
	} `json:"boundary"`
	// ExplanationConsistency 是「评分解释一致性」的 1–5 分判定。
	ExplanationConsistency struct {
		// Score 用指针：缺分时省略该键，而不是填 0。
		Score     *float64 `json:"score"`
		Rationale string   `json:"rationale"`
	} `json:"explanationConsistency"`
}

// GRPOJudgeSystemPrompt 是 GRPO 裁判的系统提示词。
func GRPOJudgeSystemPrompt() string {
	return "你是独立的数据质量评审，负责评估 GRPO 训练样本的判分标准是否可用。" +
		"你只评价判分标准本身，不生成、不修改样本内容。" +
		"你必须只返回 JSON 对象；无法判定的维度必须**省略该键**（表示缺分），不要填 0 —— 0 是一个真实分数。"
}

// GRPOJudgeUserPrompt 构造 GRPO 裁判的用户提示词。
//
// 返回的提示词显式列出「裁判需要回答的维度」（JudgedDimensions），
// 因为档位覆盖是确定性维度，不应由模型再打一次分（两个来源的分会让报告冲突）。
func GRPOJudgeUserPrompt(request GRPOJudgeRequest) (string, error) {
	if len(request.Sample.Levels) < 2 {
		return "", fmt.Errorf("GRPO 样本档位不足两档，无法评估")
	}
	dimensions := make(map[string]bool, len(request.Dimensions))
	for _, dimension := range request.Dimensions {
		dimensions[dimension] = true
	}

	var builder strings.Builder
	builder.WriteString("以下是 GRPO 样本的判分标准。请只按下列要求回答，不要复述内容。\n\n")
	builder.WriteString("【问题】\n")
	builder.WriteString(strings.TrimSpace(request.Sample.Question))
	builder.WriteString("\n\n【裁判提示词（judge_prompt）】\n")
	builder.WriteString(strings.TrimSpace(request.Sample.JudgePrompt))
	builder.WriteString("\n\n【档位与判据】\n")
	for index, level := range request.Sample.Levels {
		builder.WriteString(fmt.Sprintf("%d. %s\n", index+1, level))
		for _, rubric := range request.Sample.LevelRubrics {
			if strings.TrimSpace(rubric.Level) != strings.TrimSpace(level) {
				continue
			}
			if strings.TrimSpace(rubric.Criteria) != "" {
				builder.WriteString("   判据：" + strings.TrimSpace(rubric.Criteria) + "\n")
			}
			if strings.TrimSpace(rubric.AcceptCase) != "" {
				builder.WriteString("   接受示例：" + strings.TrimSpace(rubric.AcceptCase) + "\n")
			}
			if strings.TrimSpace(rubric.RejectCase) != "" {
				builder.WriteString("   拒绝示例：" + strings.TrimSpace(rubric.RejectCase) + "\n")
			}
		}
	}

	asksBoundary := dimensions[model.GRPODimBoundaryStability]
	asksExplanation := dimensions[model.GRPODimExplanationConsistency]
	if !asksBoundary && !asksExplanation {
		return "", nil
	}

	if asksBoundary {
		reference := request.Config.BoundaryReference
		if len(reference.Items) > 0 {
			builder.WriteString("\n【边界参考样例（冻结标注，不得修改）】\n")
			for index, item := range reference.Items {
				builder.WriteString(fmt.Sprintf("[%d] 档位=%s 期望=%s\n", index, strings.TrimSpace(item.Level), strings.TrimSpace(item.Expected)))
				builder.WriteString("    内容：" + strings.TrimSpace(item.Input) + "\n")
				if strings.TrimSpace(item.Note) != "" {
					builder.WriteString("    标注说明：" + strings.TrimSpace(item.Note) + "\n")
				}
			}
			builder.WriteString("\n请逐条判断：**仅凭上面的判据与裁判提示词**，每一条参考样例会被判为 accept 还是 reject；" +
				"若判据无法确定，回答 \"undecidable\"。\n")
		}
		// 没有参考集时**不**在这里返回空提示词：
		// explanation_consistency 仍然需要评估，而提前返回会让整次调用被跳过，
		// 表现为「评分解释一致性无故缺分」。边界稳定性的缺分由 JudgeGRPOItem
		// 在调用模型之前单独给出（并且不花钱）。
	}

	builder.WriteString("\n请只返回如下 JSON 对象：\n{\n")
	if asksBoundary {
		builder.WriteString("  \"boundary\": [{\"index\": 0, \"verdict\": \"accept|reject|undecidable\", \"rationale\": \"简短理由\"}],\n")
	}
	if asksExplanation {
		builder.WriteString("  \"explanationConsistency\": {\"score\": 1到5的整数, \"rationale\": \"简短理由\"}\n")
	} else {
		// 去掉上一行末尾的逗号（JSON 不允许尾随逗号）。
		trimmed := strings.TrimSuffix(builder.String(), ",\n")
		builder.Reset()
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	builder.WriteString("}\n")
	builder.WriteString("评分口径：explanationConsistency 评估 judge_prompt 与各档判据是否讲清同一套判分逻辑（档位之间不矛盾、边界可区分）；" +
		"1 分=彼此矛盾或无法据此判分，3 分=基本可用但有含糊处，5 分=每一档都能据此稳定判分。\n")
	builder.WriteString("无法判定的维度请**省略对应键**，不要填 0。\n")
	return builder.String(), nil
}

// JudgeGRPOItem 调用一名裁判评估 GRPO 样本。
//
// 三条与「不生成假统计」相关的行为：
//
//  1. 请求里没有任何需要判定的维度 → 返回空（调用方不会写入任何格）；
//  2. boundary_stability 需要参考集，而参考集为空 → **不调用模型**，
//     直接返回 State=missing（T24：「缺参考样例显示缺证据」）；
//  3. explanation_consistency 的分数缺失 → missing，不补 0。
func JudgeGRPOItem(ctx context.Context, provider llm.ProviderConfig, request GRPOJudgeRequest, timeout time.Duration) ([]GRPOVerdict, error) {
	wanted := map[string]bool{}
	for _, dimension := range request.Dimensions {
		wanted[dimension] = true
	}

	verdicts := make([]GRPOVerdict, 0, len(request.Dimensions))
	callsModel := false

	if wanted[model.GRPODimBoundaryStability] {
		if !request.Config.HasBoundaryReference() {
			verdicts = append(verdicts, GRPOVerdict{
				Dimension: model.GRPODimBoundaryStability,
				State:     model.ScoreStateMissing,
				Rationale: "缺少冻结的边界参考集，无法判定边界稳定性（缺证据，不计为 0 分）",
			})
		} else {
			callsModel = true
		}
	}
	if wanted[model.GRPODimExplanationConsistency] {
		callsModel = true
	}

	if !callsModel {
		return verdicts, nil
	}

	userPrompt, err := GRPOJudgeUserPrompt(request)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userPrompt) == "" {
		return verdicts, nil
	}

	var payload grpoJudgePayload
	if err := llm.CompleteStructured(ctx, provider, GRPOJudgeSystemPrompt(), userPrompt, &payload, timeout); err != nil {
		return nil, err
	}

	if wanted[model.GRPODimBoundaryStability] && request.Config.HasBoundaryReference() {
		verdicts = append(verdicts, boundaryStabilityVerdict(request, payload))
	}
	if wanted[model.GRPODimExplanationConsistency] {
		verdicts = append(verdicts, explanationConsistencyVerdict(payload))
	}

	// 稳定输出顺序：报告按维度键排序，顺序不稳定会让「同一实验两次读取」
	// 在 diff 里看起来不同（而它们其实是同一次执行）。
	sort.SliceStable(verdicts, func(i, j int) bool { return verdicts[i].Dimension < verdicts[j].Dimension })
	return verdicts, nil
}

// boundaryStabilityVerdict 把裁判的逐条判定与冻结标注比对成 1–5 分。
//
// 分数的构造：matched/total 的比例映射到 1–5（0% → 1 分，100% → 5 分）。
// 用 1 作为下界而不是 0，是因为量表本身是 1–5 分档；「全部判错」是 1 分
// （最差）而不是「缺分」—— 缺分只用于「没有依据可判」。
//
// 逐条理由写进 Rationale：T24 要求「相同边界样例的重复/不同裁判差异可追溯」，
// 而只有总分时无法知道是哪一条判得不一样。
func boundaryStabilityVerdict(request GRPOJudgeRequest, payload grpoJudgePayload) GRPOVerdict {
	total := len(request.Config.BoundaryReference.Items)
	if total == 0 {
		return GRPOVerdict{Dimension: model.GRPODimBoundaryStability,
			State: model.ScoreStateMissing, Rationale: "边界参考集为空"}
	}

	byIndex := map[int]string{}
	for _, item := range payload.Boundary {
		byIndex[item.Index] = strings.ToLower(strings.TrimSpace(item.Verdict))
	}

	matched := 0
	details := make([]string, 0, total)
	for index, item := range request.Config.BoundaryReference.Items {
		expected := strings.ToLower(strings.TrimSpace(item.Expected))
		actual := byIndex[index]
		switch {
		case actual == "":
			details = append(details, fmt.Sprintf("#%d 裁判未回答（期望 %s）", index, expected))
		case actual == grpoBoundaryUndecidable:
			details = append(details, fmt.Sprintf("#%d 判据无法确定（期望 %s）：档位边界不清晰", index, expected))
		case actual == expected:
			matched++
			details = append(details, fmt.Sprintf("#%d 一致（%s）", index, expected))
		default:
			details = append(details, fmt.Sprintf("#%d 不符：期望 %s，实际 %s", index, expected, actual))
		}
	}

	score := 1 + 4*(float64(matched)/float64(total))
	rationale := fmt.Sprintf("边界参考样例一致 %d/%d；%s", matched, total, strings.Join(details, "；"))
	if request.Config.BoundaryReference.Sampled {
		// 抽样结论必须标示范围（§2.3）。
		rationale += "（参考集为抽样，非全量）"
	}
	return GRPOVerdict{
		Dimension: model.GRPODimBoundaryStability,
		RawScore:  &score,
		State:     model.ScoreStateScored,
		Rationale: rationale,
	}
}

// explanationConsistencyVerdict 归一化裁判对评分解释一致性的判定。
func explanationConsistencyVerdict(payload grpoJudgePayload) GRPOVerdict {
	verdict := GRPOVerdict{Dimension: model.GRPODimExplanationConsistency}
	if payload.ExplanationConsistency.Score == nil {
		// 缺分：模型没给出这一维度的分数。**不补 0**。
		verdict.State = model.ScoreStateMissing
		verdict.Rationale = "裁判未返回评分解释一致性的分数（缺分，不计为 0）"
		return verdict
	}
	score := *payload.ExplanationConsistency.Score
	// 量表是 1–5；越界显式记缺分而不是夹紧 —— 夹紧会把
	// 「裁判没按量表回答」这个事实藏起来（与 model.NormalizedScore 同一原则）。
	if score < 1 || score > 5 {
		verdict.State = model.ScoreStateMissing
		verdict.Rationale = fmt.Sprintf("裁判返回的分数 %.2f 超出 1–5 量表，记缺分（不夹紧）", score)
		return verdict
	}
	verdict.RawScore = &score
	verdict.State = model.ScoreStateScored
	verdict.Rationale = strings.TrimSpace(payload.ExplanationConsistency.Rationale)
	return verdict
}
