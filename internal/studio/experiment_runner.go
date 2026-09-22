package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件是实验的**执行侧**（Issue #160 T14）。
//
// 契约：docs/plans/atelier-implementation.md §2.3（分母）、§2.6（缺分语义）、
// §4.2（实验状态机）。
//
// 两条与「冻结」直接相关的实现约束：
//
//  1. **只加载实验自己**：runner 按 experiment/item ID 取数，不查任何「当前采用」
//     的配置（不读当前维度表、不按 provider 的当前设置决定怎么做）。
//     这是「入队后修改维度/provider 不改变实验」在执行侧的落点。
//  2. **内容按冻结的版本 ID 读**：item 指向具体的 sample_version，
//     而 sample_versions 不可变（T05），因此重跑读到的是同一份内容。
//
// 关于缺分：裁判没给分时必须记成 missing 而**不是 0**。0 会被聚合当成最差分，
// 把「没评」显示成「评得很差」—— 那是会让用户错误淘汰好数据的方向。

// JudgeVerdict 是一名裁判对一个维度的判定。
type JudgeVerdict struct {
	Dimension string
	// RawScore 为 nil 表示**缺分**（不是 0）。
	RawScore *float64
	// State 是 scored / missing / error / not_applicable；留空时按 RawScore 推断。
	State      string
	Rationale  string
	ErrorClass string
}

// JudgeRequest 是一次裁判调用的输入（全部来自冻结快照与不可变内容）。
type JudgeRequest struct {
	ExperimentID int64
	ItemID       int64
	TargetKind   string

	SampleID        int64
	SampleVersionID int64
	// Payload 是该样本版本的**不可变内容**（原样透出，不加工）。
	Payload json.RawMessage
	// GeneratorFingerprint 是该内容生成来源的指纹。
	GeneratorFingerprint string

	Rubric        model.RubricSpec
	Judge         model.JudgeSpec
	IsIndependent bool

	// JudgedDimensions 是**该裁判需要回答**的维度键（T24）。
	//
	// 为什么需要它：GRPO 量表里的 `level_coverage` 是确定性维度
	//（由 LocalJudge 直接计算），模型裁判只负责其余维度。若不给这份清单，
	// 裁判会顺手给确定性维度也打一个分，而那个分会与确定性判定冲突 ——
	// 报告里同一个维度出现两个来源的分，用户无法判断该信哪个。
	JudgedDimensions []string

	// TargetConfig 是该实验冻结的目标类型专属配置（T24 的 GRPO
	// 边界参考集与教师/基准版本）。SFT 为空。
	TargetConfig json.RawMessage
}

// LocalJudge 计算**不需要模型**的确定性判据（T24 的档位覆盖）。
//
// 与 ExperimentJudge 分开的理由：确定性判据的结果与裁判无关，
// 只应记一行（judge_connection_id = 0），不能按裁判数复制 ——
// 复制 N 份会让报告显示「N 名裁判完全一致」，而那个一致是假的。
type LocalJudge interface {
	LocalVerdicts(ctx context.Context, request JudgeRequest) ([]JudgeVerdict, error)
}

// ExperimentJudge 是裁判调用接口。
//
// 抽成接口的理由与 T12 的 UnitGenerator 相同：真实裁判要花钱、要网络，
// 而验收项要求「缺分 / error / 不适用与真实 0 分区分」—— 这三条必须能被
// **按需驱动**。测试因此可以脚本化每个维度的返回，而不必真的调模型。
type ExperimentJudge interface {
	JudgeItem(ctx context.Context, request JudgeRequest) ([]JudgeVerdict, error)
}

// ExperimentRunner 执行一个质量实验。
type ExperimentRunner struct {
	Experiments *store.ExperimentStore
	// Batches 用于按**冻结的 sample_version_id** 读取内容：sample_versions
	// 不可变，因此不需要在实验表里再存一份正文（那会带来两份内容不一致的风险）。
	Batches *store.BatchStore
	Judge   ExperimentJudge
	// Local 计算确定性维度（T24 的档位覆盖）。为 nil 且量表含确定性维度时
	// **显式失败**，不静默跳过：跳过会让那些维度永远缺分，而报告看起来
	// 只是「覆盖不足」—— 一个看起来正常的错误。
	Local LocalJudge
}

// ExperimentRunResult 是一次执行的摘要（写回 jobs.payload）。
type ExperimentRunResult struct {
	ExperimentID int64  `json:"experimentId"`
	Inspected    int    `json:"inspected"`
	Scored       int    `json:"scored"`
	Missing      int    `json:"missing"`
	Error        int    `json:"error"`
	Status       string `json:"status"`
}

// RunExperiment 执行实验的全部未完成项。
//
// 只处理 pending/error 的项（续跑语义）：已 scored 的项不会再评一次，
// 因此续跑既不覆盖成功证据，也不重复花钱。
func (runner *ExperimentRunner) RunExperiment(ctx context.Context, experimentID int64) (ExperimentRunResult, error) {
	if runner.Judge == nil {
		return ExperimentRunResult{}, NewError(CodeUnavailable, "裁判客户端未配置，无法执行实验")
	}
	if runner.Batches == nil {
		return ExperimentRunResult{}, NewError(CodeUnavailable, "runner 未注入批次 store，无法读取样本内容")
	}
	experiment, err := runner.Experiments.GetExperiment(ctx, experimentID)
	if err != nil {
		return ExperimentRunResult{}, err
	}
	if runnable, reason := model.ExperimentRunnable(experiment.TargetKind, "T14"); !runnable {
		return ExperimentRunResult{}, NewError(CodeValidation, reason)
	}
	// 确定性维度（T24）需要 LocalJudge，而它属于**接线**而不是逐项事实：
	// 缺失时每个项都会同样失败。在这里提前拒绝，避免白跑一遍模型裁判
	//（每个项都先花掉裁判调用的钱）并在项上留下一批误导性的 error。
	if len(model.LocalDimensionKeys(experiment.TargetKind)) > 0 && runner.Local == nil {
		return ExperimentRunResult{}, NewError(CodeUnavailable,
			"runner 未注入确定性判据实现，无法执行该目标类型的实验")
	}

	items, err := runner.Experiments.ListPendingExperimentItems(ctx, experiment.ID, 500)
	if err != nil {
		return ExperimentRunResult{}, err
	}
	for _, item := range items {
		// 单项失败已在 runItem 内落库（带 error_class），这里继续处理其余项：
		// 一个项失败不该让整份报告作废。
		_ = runner.runItem(ctx, experiment, item)
	}

	refreshed, err := runner.Experiments.RefreshExperimentCounts(ctx, experiment.ID)
	if err != nil {
		return ExperimentRunResult{}, err
	}
	return ExperimentRunResult{
		ExperimentID: refreshed.ID,
		Inspected:    refreshed.InspectedCount,
		Scored:       refreshed.ScoredCount,
		Missing:      refreshed.MissingCount,
		Error:        refreshed.ErrorCount,
		Status:       refreshed.Status,
	}, nil
}

// runItem 评估一个待评项。
func (runner *ExperimentRunner) runItem(ctx context.Context, experiment model.Experiment, item model.ExperimentItem) error {
	// 读该版本的**不可变内容**。读不到内容不是「缺分」而是数据问题：
	// 记成缺分会让报告显示「这部分内容无法评价」，而真实原因是数据被删/被改。
	version, err := runner.Batches.GetSampleVersionByID(ctx, experiment.ProjectID, item.SampleVersionID)
	if err != nil {
		// 标成 error（需要人工介入）而不是 missing。
		if _, markErr := runner.Experiments.MarkExperimentItemStatus(ctx, item.ID,
			model.ExperimentItemError, model.ErrorClassConfig,
			truncate(fmt.Sprintf("读取样本版本 %d 失败：%v", item.SampleVersionID, err), 500)); markErr != nil {
			return markErr
		}
		return err
	}
	payload := version.Payload

	scoredDimensions := 0
	judgeFailed := false
	anyMissing := false

	// 确定性维度（T24）：与裁判无关，只记一行（judge_connection_id = 0）。
	// 先算它们再算裁判，使「档位覆盖为 0」这种结构性失败在报告里立刻可见，
	// 而不是被一个「平均分看起来还行」的模型分掩盖。
	judgeDimensions := judgeFacingDimensions(experiment.Rubric, model.LocalDimensionKeys(experiment.TargetKind))
	if len(judgeDimensions) != len(experiment.Rubric.Dimensions) {
		if runner.Local == nil {
			// 量表含确定性维度但没有实现：显式失败，不静默跳过。
			// 跳过会让这些维度永远缺分，而报告看起来只是「覆盖不足」——
			// 一个看起来正常的错误。重试也不会变好，这是接线/部署错误。
			if _, markErr := runner.Experiments.MarkExperimentItemStatus(ctx, item.ID,
				model.ExperimentItemError, model.ErrorClassConfig,
				"该量表含确定性维度，但 runner 未注入 LocalJudge"); markErr != nil {
				return markErr
			}
			return NewError(CodeUnavailable, "runner 未注入确定性判据实现，无法执行该目标类型的实验")
		}
		localVerdicts, localErr := runner.Local.LocalVerdicts(ctx, JudgeRequest{
			ExperimentID: experiment.ID, ItemID: item.ID, TargetKind: experiment.TargetKind,
			SampleID: item.SampleID, SampleVersionID: item.SampleVersionID,
			Payload: payload, GeneratorFingerprint: item.GeneratorFingerprint,
			Rubric: experiment.Rubric, TargetConfig: experiment.TargetConfig,
		})
		if localErr != nil {
			if _, markErr := runner.Experiments.MarkExperimentItemStatus(ctx, item.ID,
				model.ExperimentItemError, classifyUnitError(localErr),
				truncate(localErr.Error(), 500)); markErr != nil {
				return markErr
			}
			return localErr
		}
		byLocalKey := map[string]JudgeVerdict{}
		for _, verdict := range localVerdicts {
			byLocalKey[verdict.Dimension] = verdict
		}
		for _, dimension := range experiment.Rubric.Dimensions {
			if !model.IsLocalDimension(experiment.TargetKind, dimension.Key) {
				continue
			}
			verdict, found := byLocalKey[dimension.Key]
			state := model.ScoreStateMissing
			var raw *float64
			rationale := "确定性判据没有给出结果"
			if found {
				rationale = truncate(verdict.Rationale, 2000)
				raw = verdict.RawScore
				switch {
				case verdict.State != "":
					state = verdict.State
				case verdict.RawScore != nil:
					state = model.ScoreStateScored
				}
			}
			if state == model.ScoreStateScored {
				scoredDimensions++
			} else {
				anyMissing = true
			}
			// judge_connection_id = 0：这不是任何一条模型连接给出的分，
			// 而是确定性判据。用 0 而不是借用某条连接，避免报告的
			// 「裁判分歧」把确定性维度算进裁判统计。
			if _, recordErr := runner.Experiments.RecordScore(ctx, store.RecordScoreInput{
				ExperimentID: experiment.ID, ExperimentItemID: item.ID,
				JudgeConnectionID: 0, JudgeIndex: 0,
				IsIndependent: true, Dimension: dimension.Key,
				RawScore: raw, ScoreState: state, Rationale: rationale,
			}); recordErr != nil {
				return recordErr
			}
		}
	}

	judgedKeys := dimensionKeys(judgeDimensions)
	for index, judge := range experiment.Judges {
		isIndependent := judgeIsIndependentForItem(judge, item)
		verdicts, judgeErr := runner.Judge.JudgeItem(ctx, JudgeRequest{
			ExperimentID: experiment.ID, ItemID: item.ID, TargetKind: experiment.TargetKind,
			SampleID: item.SampleID, SampleVersionID: item.SampleVersionID,
			Payload: payload, GeneratorFingerprint: item.GeneratorFingerprint,
			Rubric: experiment.Rubric, Judge: judge, IsIndependent: isIndependent,
			JudgedDimensions: judgedKeys, TargetConfig: experiment.TargetConfig,
		})
		if judgeErr != nil {
			// 裁判整体失败：为该裁判的每个维度写一行 error（不是缺分行）。
			// 区分「裁判出错」（要重试）与「裁判给了缺分」（该维度无法评价）。
			judgeFailed = true
			for _, dimension := range judgeDimensions {
				if _, recordErr := runner.Experiments.RecordScore(ctx, store.RecordScoreInput{
					ExperimentID: experiment.ID, ExperimentItemID: item.ID,
					JudgeConnectionID: judge.ConnectionID, JudgeIndex: index,
					IsIndependent: isIndependent, Dimension: dimension.Key,
					ScoreState: model.ScoreStateError, ErrorClass: classifyUnitError(judgeErr),
					Rationale: truncate(judgeErr.Error(), 500),
				}); recordErr != nil {
					return recordErr
				}
			}
			continue
		}

		byDimension := map[string]JudgeVerdict{}
		for _, verdict := range verdicts {
			byDimension[verdict.Dimension] = verdict
		}
		for _, dimension := range judgeDimensions {
			verdict, found := byDimension[dimension.Key]
			if !found {
				// 裁判没给这个维度：缺分（不是 0）。
				anyMissing = true
				if _, recordErr := runner.Experiments.RecordScore(ctx, store.RecordScoreInput{
					ExperimentID: experiment.ID, ExperimentItemID: item.ID,
					JudgeConnectionID: judge.ConnectionID, JudgeIndex: index,
					IsIndependent: isIndependent, Dimension: dimension.Key,
					ScoreState: model.ScoreStateMissing,
					Rationale:  "裁判未返回该维度的评分",
				}); recordErr != nil {
					return recordErr
				}
				continue
			}
			state := verdict.State
			if state == "" {
				if verdict.RawScore == nil {
					state = model.ScoreStateMissing
				} else {
					state = model.ScoreStateScored
				}
			}
			if state == model.ScoreStateScored {
				scoredDimensions++
			} else {
				anyMissing = true
			}
			if _, recordErr := runner.Experiments.RecordScore(ctx, store.RecordScoreInput{
				ExperimentID: experiment.ID, ExperimentItemID: item.ID,
				JudgeConnectionID: judge.ConnectionID, JudgeIndex: index,
				IsIndependent: isIndependent, Dimension: dimension.Key,
				RawScore: verdict.RawScore, ScoreState: state,
				Rationale: truncate(verdict.Rationale, 2000), ErrorClass: verdict.ErrorClass,
			}); recordErr != nil {
				return recordErr
			}
		}
	}

	// 项状态：裁判整体出错 → error；有缺分 → missing；否则 scored。
	// 「部分评了」不算已评分：那会让覆盖虚高，而覆盖是判断结论可信度的唯一依据。
	status := model.ExperimentItemScored
	errorClass := ""
	message := ""
	switch {
	case judgeFailed:
		status, errorClass = model.ExperimentItemError, model.ErrorClassProvider
		message = "至少一名裁判调用失败，未完成全部评分"
	case anyMissing:
		status = model.ExperimentItemMissing
		message = "存在未获得评分的维度（缺分不计为 0 分）"
	}
	if _, err := runner.Experiments.MarkExperimentItemStatus(ctx, item.ID, status, errorClass, message); err != nil {
		return err
	}
	return nil
}

// judgeFacingDimensions 返回需要**模型裁判**回答的量表维度（T24）。
//
// 确定性维度（如 GRPO 的 level_coverage）由 LocalJudge 计算并单独记录，
// 模型裁判只负责其余维度。不做这个切分会让同一维度出现两个来源的分，
// 而报告无法判断该信哪个。
func judgeFacingDimensions(rubric model.RubricSpec, localKeys []string) []model.RubricDimension {
	if len(localKeys) == 0 {
		return rubric.Dimensions
	}
	local := make(map[string]bool, len(localKeys))
	for _, key := range localKeys {
		local[key] = true
	}
	filtered := make([]model.RubricDimension, 0, len(rubric.Dimensions))
	for _, dimension := range rubric.Dimensions {
		if !local[dimension.Key] {
			filtered = append(filtered, dimension)
		}
	}
	return filtered
}

// dimensionKeys 提取维度键，供 JudgeRequest.JudgedDimensions 使用。
func dimensionKeys(dimensions []model.RubricDimension) []string {
	keys := make([]string, 0, len(dimensions))
	for _, dimension := range dimensions {
		keys = append(keys, dimension.Key)
	}
	return keys
}

// judgeIsIndependentForItem 判断某裁判对该项是否独立。
//
// 逐项判断而不是逐实验判断：同一实验可能含多个生成来源（T14 验收项
// 「若实验含多生成来源，逐条判断独立性」），因此「裁判 X 对 A 来源独立、
// 对 B 来源不独立」是正常结论，必须按项记录。
func judgeIsIndependentForItem(judge model.JudgeSpec, item model.ExperimentItem) bool {
	return model.IsIndependentJudge(judge, model.GeneratorSource{
		ConnectionID:        parsedConnectionID(item.GeneratorSource),
		EndpointFingerprint: item.GeneratorFingerprint,
	})
}

// parsedConnectionID 把 item 里存的连接标识解析回 int64（解析失败返回 0）。
//
// 0 与「解析失败」在这里等价：独立性判定的依据是 **endpoint 指纹**，
// 连接 ID 只是兜底区分，因此解析失败不会让判定失效。
func parsedConnectionID(raw string) int64 {
	trimmed := strings.TrimSpace(raw)
	var value int64
	for _, char := range trimmed {
		if char < '0' || char > '9' {
			return 0
		}
		value = value*10 + int64(char-'0')
	}
	return value
}

// truncate 截断长文本（评分理由会进报告与界面）。
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// JudgePromptFor 构造裁判提示词（供真实实现使用，也可被测试断言内容）。
//
// 提示词里显式禁止用 0 表示缺分：模型很自然会用 0 表示「不知道」，
// 而 0 会被聚合当成最差分。独立性检查在服务端已经拦住了自评，
// 提示词里的来源指纹只是减少语义混淆，不承担安全职责。
func JudgePromptFor(request JudgeRequest) string {
	payload := strings.TrimSpace(string(request.Payload))
	if payload == "" {
		payload = "（内容缺失）"
	}
	dimensions := make([]string, 0, len(request.Rubric.Dimensions))
	for _, dimension := range request.Rubric.Dimensions {
		dimensions = append(dimensions, fmt.Sprintf("- %s（%s）：原始分范围 %.2f–%.2f，权重 %.2f",
			dimension.Key, dimension.Label, dimension.Min, dimension.Max, dimension.Weight))
	}
	return fmt.Sprintf(`你是独立评审。请按下列维度对给定内容打分，并给出简短理由。

维度（必须逐项回答）：
%s

内容（原文，不可修改）：
%s

要求：
1. 只返回 JSON 对象，键为维度键，值为 {"score": 数值, "rationale": "理由"}；
2. 无法评价某个维度时**省略该键**（缺分），不要填 0 —— 0 是一个真实的分数；
3. 不要参考生成该内容的模型（来源指纹 %s），只看内容本身。`,
		strings.Join(dimensions, "\n"), payload, request.GeneratorFingerprint)
}
