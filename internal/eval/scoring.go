package eval

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// 本文件由 L9 lane 独占：逐条 × 逐裁判 × 逐维度的打分引擎。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.9 节。
//
// 设计要点：本文件只负责「构造提示词 + 调裁判 + 归一化结果」，
// 落库与进度回写由 apps/worker/job_eval.go 负责。这样评分逻辑可以
// 用 fake judge 脱离 DB 与网络单测。

// 该模型是推理型，单次响应可达 120 秒，超时必须留足余量。
const DefaultJudgeTimeout = 300 * time.Second

// scoreJSONFunc 是 ScoreJSON 的可替换间接层。
//
// 生产路径始终是 eval.ScoreJSON（真实 LLM 调用）；单测把它替换成 fake，
// 从而能验证提示词构造、分数夹紧、失败隔离等逻辑，而不必真的打网络。
var scoreJSONFunc = ScoreJSON

// ScoreResponse 裁判必须返回的 JSON 结构。
//
// 字段名与提示词里约定的完全一致；unmarshalStructuredContent 会把
// 模型正文（可能包在 ```json 围栏里）解析进来。
type ScoreResponse struct {
	Score     float64 `json:"score"`
	Rationale string  `json:"rationale"`
}

// ScoreRequest 一次单维度打分所需的输入。
type ScoreRequest struct {
	Judge     JudgeRef
	Dimension model.EvalDimension
	Question  string
	Reasoning string
	Answer    string
	Timeout   time.Duration
}

// ScoreOutcome 一次打分的结果。
//
// 即便失败也返回 outcome（Status=failed + Err），而不是直接返回 error：
// 调用方需要把失败原因逐条落库，单条失败不能中断整轮评估。
type ScoreOutcome struct {
	Score     float64
	Rationale string
	Raw       string
	Status    string
	Err       error
	Clamped   bool
	RawScore  float64
}

// 打分状态。与 eval_item_scores.status 的取值对应。
const (
	ScoreStatusScored = "scored"
	ScoreStatusFailed = "failed"
)

// BuildScorePrompts 构造一次单维度打分的 system / user 提示词。
//
// 拆成独立函数是为了让单测能直接断言「rubric 与 scale 区间确实进了提示词」，
// 而不必先跑通一次 LLM 调用。
func BuildScorePrompts(dimension model.EvalDimension, question, reasoning, answer string) (string, string) {
	scaleMin := dimension.ScaleMin
	scaleMax := dimension.ScaleMax

	system := strings.Join([]string{
		"你是一名严格的数据质量评审专家，负责评估长链思考训练样本的质量。",
		"",
		"你只评估用户指定的那一个维度，不要扩展到其他维度。",
		"你必须只输出一个 JSON 对象，不要输出任何解释性文字、不要使用 Markdown 代码围栏。",
		"JSON 结构固定为：",
		`{"score": <数字>, "rationale": "<中文评分理由>"}`,
		"",
		fmt.Sprintf("score 必须是 %d 到 %d 之间的数字（含端点）。", scaleMin, scaleMax),
		"rationale 用中文说明给这个分数的具体依据，指出样本里支持该判断的原话或缺失之处。",
	}, "\n")

	scaleSection := fmt.Sprintf("评分区间：%d 分（最低）~ %d 分（最高）。请把分数落在该区间内。",
		scaleMin, scaleMax)
	if strings.TrimSpace(dimension.Rubric) != "" {
		scaleSection += "\n\n本维度的评分标准（rubric）：\n" + strings.TrimSpace(dimension.Rubric)
	}

	user := strings.Join([]string{
		"# 评估维度",
		fmt.Sprintf("名称：%s", dimension.Name),
		fmt.Sprintf("标识：%s", dimension.Key),
		fmt.Sprintf("分类：%s", dimension.Category),
		"描述：" + strings.TrimSpace(dimension.Description),
		"",
		"# 评分标准",
		scaleSection,
		"",
		"# 被评估的样本",
		"## 问题",
		emptyAsPlaceholder(question),
		"",
		"## 思维链",
		emptyAsPlaceholder(reasoning),
		"",
		"## 答案",
		emptyAsPlaceholder(answer),
		"",
		"# 输出要求",
		fmt.Sprintf("只输出 JSON：{\"score\": <数字>, \"rationale\": \"<中文理由>\"}，score 在 [%d, %d] 内。",
			scaleMin, scaleMax),
	}, "\n")

	return system, user
}

// emptyAsPlaceholder 让空字段在提示词里可辨认，而不是留下一片空白让模型猜。
func emptyAsPlaceholder(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "（该项为空）"
	}
	return trimmed
}

// ClampScore 把越界分数夹紧到维度的 scale 区间。
//
// 返回夹紧后的分数与「是否发生了夹紧」。
// 刻意不丢弃越界评分：模型给出 15 而区间是 0~10 时，这条评分仍然表达了
// 「非常好」的强信号，丢弃它等于让最优质的样本从统计里消失。
// 夹紧事实会写进 rationale，使该分数的来源可追溯。
func ClampScore(score float64, scaleMin, scaleMax int) (float64, bool) {
	// NaN 无法比较，必须单独处理：否则它会一路污染均值与标准差。
	if math.IsNaN(score) {
		return float64(scaleMin), true
	}
	low := float64(scaleMin)
	high := float64(scaleMax)
	if low > high {
		low, high = high, low
	}
	if score < low {
		return low, true
	}
	if score > high {
		return high, true
	}
	return score, false
}

// ScoreDimension 对单条数据的单个维度调一次裁判并归一化结果。
//
// 任何失败都以 outcome.Status = failed 返回，同时把原因放进 outcome.Err。
// 调用方（worker）负责记录失败并继续处理下一个维度/下一条数据 ——
// 单点失败不应让整轮评估前功尽弃。
func ScoreDimension(ctx context.Context, request ScoreRequest) ScoreOutcome {
	outcome := ScoreOutcome{Status: ScoreStatusFailed}

	if request.Judge.ProviderID <= 0 {
		outcome.Err = fmt.Errorf("judge provider is required")
		return outcome
	}
	if strings.TrimSpace(request.Dimension.Key) == "" {
		outcome.Err = fmt.Errorf("dimension key is required")
		return outcome
	}

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = DefaultJudgeTimeout
	}

	system, user := BuildScorePrompts(request.Dimension, request.Question, request.Reasoning, request.Answer)

	var response ScoreResponse
	if err := scoreJSONFunc(ctx, request.Judge, system, user, &response, timeout); err != nil {
		outcome.Err = fmt.Errorf("judge %d dimension %s: %w", request.Judge.ProviderID, request.Dimension.Key, err)
		return outcome
	}

	// raw_response 记录模型返回的分数原文，便于事后核对夹紧是否合理。
	outcome.Raw = fmt.Sprintf(`{"score": %v, "rationale": %q}`, response.Score, response.Rationale)
	outcome.RawScore = response.Score

	clamped, wasClamped := ClampScore(response.Score, request.Dimension.ScaleMin, request.Dimension.ScaleMax)
	outcome.Score = clamped
	outcome.Clamped = wasClamped
	outcome.Rationale = strings.TrimSpace(response.Rationale)

	if wasClamped {
		// 把夹紧事实写进 rationale：否则报告里只看到「10 分」，
		// 无法分辨这是模型给的满分还是被夹紧后的结果。
		outcome.Rationale = fmt.Sprintf("[模型返回 %v，超出维度区间 %d~%d，已夹紧] %s",
			response.Score, request.Dimension.ScaleMin, request.Dimension.ScaleMax, outcome.Rationale)
	}
	if outcome.Rationale == "" {
		// 模型没给理由时留一句可辨认的说明，不要留空串让人以为落库丢了字段。
		outcome.Rationale = "（模型未给出评分理由）"
	}

	outcome.Status = ScoreStatusScored
	outcome.Err = nil
	return outcome
}

// DimensionScore 一条数据在一个裁判下的一个维度打分结果。
//
// 键（裁判 ID + 维度 key）与 eval_item_scores 的 UNIQUE 约束一致，
// 调用方据此直接 upsert，无需再推导。
type DimensionScore struct {
	JudgeProviderID int64
	DimensionKey    string
	Outcome         ScoreOutcome
}

// ItemScoreRequest 一条数据的完整打分输入。
type ItemScoreRequest struct {
	Judges     []JudgeRef
	Dimensions []model.EvalDimension
	Question   string
	Reasoning  string
	Answer     string
	Timeout    time.Duration
}

// ScoreItemDimensions 对一条数据执行「全部裁判 × 全部维度」的打分。
//
// 失败隔离是本函数的核心语义：任何单次调用失败都只记为一条 failed 结果，
// 循环继续推进，绝不提前返回。理由：一次评估可能有 50+ 维度 × 多个裁判，
// 因某裁判一次超时就丢掉其余全部分数，会让用户白等一场并得到残缺报告。
//
// 返回顺序固定为「裁判顺序 × 维度顺序」，便于报告稳定复现。
func ScoreItemDimensions(ctx context.Context, request ItemScoreRequest) []DimensionScore {
	results := make([]DimensionScore, 0, len(request.Judges)*len(request.Dimensions))
	for _, judge := range request.Judges {
		for _, dimension := range request.Dimensions {
			outcome := ScoreDimension(ctx, ScoreRequest{
				Judge:     judge,
				Dimension: dimension,
				Question:  request.Question,
				Reasoning: request.Reasoning,
				Answer:    request.Answer,
				Timeout:   request.Timeout,
			})
			results = append(results, DimensionScore{
				JudgeProviderID: judge.ProviderID,
				DimensionKey:    dimension.Key,
				Outcome:         outcome,
			})
		}
	}
	return results
}
