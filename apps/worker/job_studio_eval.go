package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件把 T14 的实验 runner 接到 Studio 作业系统上，并实现**真实裁判**
// 与 GRPO 质量适配器（Issue #160 T24）。
//
// 契约：docs/plans/atelier-implementation.md §2.2（GRPO payload）、
// §2.3（分母与覆盖）、§2.6（缺分语义）；#160 T24 的落点原文：
// `internal/eval/grpo_adapter.go`、`apps/worker/job_studio_eval.go`。
//
// 为什么 T24 需要负责「把实验接进作业系统」：
// `POST P/experiments` 在 T19 就已经返回 202（「已冻结，执行是异步的」），
// 但当时只有 runner 类，没有作业处理器与真实裁判 —— 于是实验永远停在
// queued，而界面显示的是「排队中」。用户会一直等一个不会发生的执行。
// T24 补齐这一环：作业载荷带 experimentId，worker 按实验的**冻结快照**
// 执行，并在同一条路径上分派 SFT / GRPO 的判据构造。

func init() {
	RegisterStudioJobHandler(model.JobKindExperimentRun, handleStudioExperimentRun)
}

// experimentRunPayload 是 `studio.experiment.run` 的作业载荷。
//
// 只带 experimentId：判据、量表、裁判、范围全部从 experiments /
// experiment_items 的**冻结快照**读取。把配置放进载荷会让「重投时用
// 一份过期载荷执行」成为可能 —— 那正是 T06 把载荷从 Redis 消息里去掉的原因。
type experimentRunPayload struct {
	ExperimentID int64 `json:"experimentId"`
}

// handleStudioExperimentRun 执行一个质量实验。
func handleStudioExperimentRun(ctx context.Context, env *StudioJobEnv, job model.Job) (any, error) {
	var payload experimentRunPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("实验作业载荷无法解析（作业创建路径异常）：%w", err)
		}
	}
	if payload.ExperimentID <= 0 {
		return nil, fmt.Errorf("实验作业缺少 experimentId（作业创建路径异常）")
	}

	datasets := env.datasetStore()
	if datasets == nil {
		// 拿不到凭证就继续，只会得到「provider 配置不完整」这种看似业务错误
		// 而实为部署错误的失败，让排障方向跑偏（与 T12 同一判断）。
		return nil, fmt.Errorf("worker 未注入 provider 解析依赖，无法执行质量实验")
	}

	runner := &studio.ExperimentRunner{
		Experiments: env.Experiments(),
		Batches:     env.Batches(),
		Judge:       &studioConnectionJudge{datasets: datasets},
		// 确定性判据（GRPO 的档位覆盖）由本地计算，不调模型。
		Local: grpoLocalJudge{},
	}
	result, err := runner.RunExperiment(ctx, payload.ExperimentID)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// 真实裁判：按连接解析凭证并调用模型
// ---------------------------------------------------------------------------

// sftJudgeSystemPrompt 是 SFT 裁判的系统提示词。
//
// 与 GRPO 的系统提示词一样，显式禁止用 0 表示缺分：模型很自然会用 0
// 表示「不知道」，而 0 会被聚合当成最差分（§2.6）。
const sftJudgeSystemPrompt = "你是独立评审，只按给定维度对内容打分。" +
	"你必须只返回 JSON 对象；无法评价某个维度时**省略该键**（缺分），不要填 0 —— 0 是一个真实的分数。"

// studioConnectionJudge 是按连接解析凭证的真实裁判实现。
type studioConnectionJudge struct {
	datasets *store.DatasetStore
}

// JudgeItem 调用一条连接对应的模型完成该样本的评分。
//
// 目标类型分派：GRPO 走 `internal/eval/grpo_adapter.go`，SFT 走通用维度打分。
// 不做「统一提示词」：GRPO 样本里没有 reasoning/answer，用 SFT 提示词会让
// 模型对着空内容打分（T24 明确禁止）。
func (judge *studioConnectionJudge) JudgeItem(ctx context.Context, request studio.JudgeRequest) ([]studio.JudgeVerdict, error) {
	if len(request.JudgedDimensions) == 0 {
		// 没有需要模型裁判的维度（量表全是确定性维度）：不调用模型。
		// 调用一次却什么都记不了会白花一笔钱，也会让 usage 与实际证据对不上。
		return nil, nil
	}
	baseURL, modelName, providerType, reasoningEffort, apiKey, err := judge.datasets.ResolveProvider(ctx, request.Judge.ConnectionID)
	if err != nil {
		// 连接被停用/删除时**不**回退到默认连接：那会让用户在不知情的情况下
		// 换一个裁判模型，而质量结论的独立性判定是按连接冻结的（T07/T14）。
		return nil, fmt.Errorf("裁判连接不可用：%w", err)
	}
	provider := llm.ProviderConfig{
		BaseURL: baseURL, Model: modelName, ProviderType: providerType,
		ReasoningEffort: reasoningEffort, APIKey: apiKey,
	}

	switch request.TargetKind {
	case model.TargetKindGRPO:
		return judge.judgeGRPO(ctx, provider, request)
	case model.TargetKindSFT:
		return judge.judgeSFT(ctx, provider, request)
	default:
		return nil, fmt.Errorf("目标类型 %q 没有对应的质量适配器", request.TargetKind)
	}
}

// judgeGRPO 用 GRPO 适配器评估一个样本。
func (judge *studioConnectionJudge) judgeGRPO(ctx context.Context, provider llm.ProviderConfig, request studio.JudgeRequest) ([]studio.JudgeVerdict, error) {
	sample, err := model.ParseGRPOSamplePayload(request.Payload)
	if err != nil {
		// 内容不合规是数据问题（不是裁判失败）：显式报错，让实验项记 error
		// 并附上可操作原因，而不是让裁判去猜一个坏 payload 该怎么打分。
		return nil, fmt.Errorf("GRPO 样本内容不合规，无法评估：%w", err)
	}
	config, err := model.ParseGRPOTargetConfig(request.TargetConfig)
	if err != nil {
		return nil, err
	}
	verdicts, err := eval.JudgeGRPOItem(ctx, provider, eval.GRPOJudgeRequest{
		Sample: sample, Config: config,
		Dimensions: request.JudgedDimensions, JudgeLabel: request.Judge.Label,
	}, eval.DefaultJudgeTimeout)
	if err != nil {
		return nil, err
	}
	mapped := make([]studio.JudgeVerdict, 0, len(verdicts))
	for _, verdict := range verdicts {
		mapped = append(mapped, studio.JudgeVerdict{
			Dimension: verdict.Dimension, RawScore: verdict.RawScore,
			State: verdict.State, Rationale: verdict.Rationale, ErrorClass: verdict.ErrorClass,
		})
	}
	return mapped, nil
}

// judgeSFT 用通用维度量表评估一个 SFT 样本。
func (judge *studioConnectionJudge) judgeSFT(ctx context.Context, provider llm.ProviderConfig, request studio.JudgeRequest) ([]studio.JudgeVerdict, error) {
	// 只描述需要模型回答的维度：把确定性维度也列进去会让模型给
	// 它打一个分，而那个分会与确定性判定冲突。
	scoped := request
	scoped.Rubric.Dimensions = dimensionsByKey(request.Rubric, request.JudgedDimensions)
	userPrompt := studio.JudgePromptFor(scoped)

	var payload map[string]struct {
		// Score 用指针：模型省略该键时是「缺分」，不是 0。
		Score     *float64 `json:"score"`
		Rationale string   `json:"rationale"`
	}
	if err := llm.CompleteStructured(ctx, provider, sftJudgeSystemPrompt, userPrompt, &payload, eval.DefaultJudgeTimeout); err != nil {
		return nil, err
	}

	verdicts := make([]studio.JudgeVerdict, 0, len(request.JudgedDimensions))
	for _, dimension := range request.JudgedDimensions {
		entry, found := payload[dimension]
		if !found || entry.Score == nil {
			// 缺分：显式记 missing（不是 0）。理由里点明是「裁判没给」。
			verdicts = append(verdicts, studio.JudgeVerdict{
				Dimension: dimension, State: model.ScoreStateMissing,
				Rationale: "裁判未返回该维度的评分（缺分，不计为 0）",
			})
			continue
		}
		rationale := strings.TrimSpace(entry.Rationale)
		verdicts = append(verdicts, studio.JudgeVerdict{
			Dimension: dimension, RawScore: entry.Score,
			State: model.ScoreStateScored, Rationale: rationale,
		})
	}
	return verdicts, nil
}

// dimensionsByKey 按给定键筛选量表维度（保持原顺序）。
func dimensionsByKey(rubric model.RubricSpec, keys []string) []model.RubricDimension {
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	filtered := make([]model.RubricDimension, 0, len(keys))
	for _, dimension := range rubric.Dimensions {
		if wanted[dimension.Key] {
			filtered = append(filtered, dimension)
		}
	}
	return filtered
}

// ---------------------------------------------------------------------------
// 确定性判据：GRPO 档位覆盖
// ---------------------------------------------------------------------------

// grpoLocalJudge 计算 GRPO 的确定性维度（档位覆盖）。
//
// 为什么不调模型：档位覆盖是**结构性事实**（每一档是否有可判分依据、
// 判据是否出现在裁判提示词里）。让模型来判断会引入一个本可避免的
// 「模型说覆盖了、实际判据是空的」的错误方向。
type grpoLocalJudge struct{}

func (grpoLocalJudge) LocalVerdicts(_ context.Context, request studio.JudgeRequest) ([]studio.JudgeVerdict, error) {
	if request.TargetKind != model.TargetKindGRPO {
		// 只有 GRPO 有确定性维度；其它目标类型不该走到这里
		// （runner 按 LocalDimensionKeys 决定是否调用）。
		return nil, nil
	}
	sample, err := model.ParseGRPOSamplePayload(request.Payload)
	if err != nil {
		return nil, fmt.Errorf("GRPO 样本内容不合规，无法计算档位覆盖：%w", err)
	}
	coverage := model.BuildLevelCoverage(sample)
	if !coverage.Evaluable {
		// 没有档位可判：记**缺分**而不是 0 分。
		// 0 分会被聚合当成「档位全都不可判分」，而事实是「没有档位可判」。
		return []studio.JudgeVerdict{{
			Dimension: model.GRPODimLevelCoverage,
			State:     model.ScoreStateMissing,
			Rationale: strings.Join(coverage.Reasons, "；"),
		}}, nil
	}
	score := coverage.Score
	rationale := fmt.Sprintf("档位覆盖 %d/%d", len(coverage.CoveredLevels), len(coverage.CoveredLevels)+len(coverage.MissingLevels))
	if len(coverage.Reasons) > 0 {
		rationale += "；" + strings.Join(coverage.Reasons, "；")
	}
	return []studio.JudgeVerdict{{
		Dimension: model.GRPODimLevelCoverage,
		RawScore:  &score,
		State:     model.ScoreStateScored,
		Rationale: rationale,
	}}, nil
}

// ---------------------------------------------------------------------------
// StudioJobEnv 的实验依赖
// ---------------------------------------------------------------------------

// Experiments 返回实验 store。
func (env *StudioJobEnv) Experiments() *store.ExperimentStore {
	if value, ok := env.Extra("experimentStore"); ok {
		if experiments, ok := value.(*store.ExperimentStore); ok {
			return experiments
		}
	}
	experiments := store.NewExperimentStore(env.Pool)
	env.SetExtra("experimentStore", experiments)
	return experiments
}
