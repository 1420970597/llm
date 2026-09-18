package model

import "time"

// EvalDimension 评估维度。内置 ≥50 个长链思考维度，用户可扩展。
type EvalDimension struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	Rubric      string    `json:"rubric"`
	ScaleMin    int       `json:"scaleMin"`
	ScaleMax    int       `json:"scaleMax"`
	IsBuiltin   bool      `json:"isBuiltin"`
	IsActive    bool      `json:"isActive"`
	Weight      float64   `json:"weight"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// EvalRun 一次评估运行。抽样方式：full / ratio / count。
type EvalRun struct {
	ID                int64     `json:"id"`
	DatasetID         int64     `json:"datasetId"`
	Name              string    `json:"name"`
	SamplingMode      string    `json:"samplingMode"`
	SampleRatio       float64   `json:"sampleRatio"`
	SampleSize        int       `json:"sampleSize"`
	TargetKind        string    `json:"targetKind"`
	DimensionKeys     []string  `json:"dimensionKeys"`
	JudgeProviderIDs  []int64   `json:"judgeProviderIds"`
	GeneratorProvider int64     `json:"generatorProviderId"`
	Status            string    `json:"status"`
	TotalItems        int       `json:"totalItems"`
	ScoredItems       int       `json:"scoredItems"`
	ErrorSummary      string    `json:"errorSummary"`
	CreatedBy         int64     `json:"createdBy"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// EvalRunCreateRequest 创建评估运行的请求体。
type EvalRunCreateRequest struct {
	DatasetID         int64    `json:"datasetId"`
	Name              string   `json:"name"`
	SamplingMode      string   `json:"samplingMode"`
	SampleRatio       float64  `json:"sampleRatio"`
	SampleSize        int      `json:"sampleSize"`
	TargetKind        string   `json:"targetKind"`
	DimensionKeys     []string `json:"dimensionKeys"`
	JudgeProviderIDs  []int64  `json:"judgeProviderIds"`
	GeneratorProvider int64    `json:"generatorProviderId"`
}

// EvalRunJudge 参与某次评估的裁判模型。生成该数据集的模型必须被排除。
type EvalRunJudge struct {
	ID            int64     `json:"id"`
	EvalRunID     int64     `json:"evalRunId"`
	ProviderID    int64     `json:"providerId"`
	ProviderName  string    `json:"providerName"`
	Model         string    `json:"model"`
	Excluded      bool      `json:"excluded"`
	ExcludeReason string    `json:"excludeReason"`
	Status        string    `json:"status"`
	ScoredItems   int       `json:"scoredItems"`
	ErrorSummary  string    `json:"errorSummary"`
	CreatedAt     time.Time `json:"createdAt"`
}

// EvalJudgeOption 可选的裁判模型（来自 provider_admin）。
//
// Excluded/ExcludeReason 由 eval.ResolveJudges 按生成者填入：生成者模型与其
// 同源模型禁止自评。前端的裁判选择器据此置灰并说明原因。
type EvalJudgeOption struct {
	ProviderID    int64  `json:"providerId"`
	ProviderName  string `json:"providerName"`
	Model         string `json:"model"`
	IsActive      bool   `json:"isActive"`
	Excluded      bool   `json:"excluded"`
	ExcludeReason string `json:"excludeReason"`
}

// DatasetJudgeOptions 某数据集的裁判候选集（含剔除标注）。
//
// 带上 GeneratorProviderID 是为了让调用方自己也能复现剔除判定：
// 不需要去探测「哪个是被剔除的那个」。
type DatasetJudgeOptions struct {
	DatasetID           int64             `json:"datasetId"`
	GeneratorProviderID int64             `json:"generatorProviderId"`
	Judges              []EvalJudgeOption `json:"judges"`
}

// EvalItem 被评估的单条数据快照。
type EvalItem struct {
	ID         int64          `json:"id"`
	EvalRunID  int64          `json:"evalRunId"`
	DatasetID  int64          `json:"datasetId"`
	QuestionID int64          `json:"questionId"`
	ItemIndex  int            `json:"itemIndex"`
	Payload    map[string]any `json:"payload"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// EvalItemScore 单条数据 × 单个裁判 × 单个维度的打分。
type EvalItemScore struct {
	ID              int64     `json:"id"`
	EvalRunID       int64     `json:"evalRunId"`
	EvalItemID      int64     `json:"evalItemId"`
	JudgeProviderID int64     `json:"judgeProviderId"`
	DimensionKey    string    `json:"dimensionKey"`
	Score           float64   `json:"score"`
	Rationale       string    `json:"rationale"`
	RawResponse     string    `json:"rawResponse"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
}

// EvalSummary 汇总行。scope: overall / judge / dimension / item。
type EvalSummary struct {
	ID          int64          `json:"id"`
	EvalRunID   int64          `json:"evalRunId"`
	Scope       string         `json:"scope"`
	RefKey      string         `json:"refKey"`
	Score       float64        `json:"score"`
	SampleCount int            `json:"sampleCount"`
	Detail      map[string]any `json:"detail"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// EvalDimensionStat 单维度统计。
type EvalDimensionStat struct {
	DimensionKey string  `json:"dimensionKey"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Score        float64 `json:"score"`
	SampleCount  int     `json:"sampleCount"`
	StdDev       float64 `json:"stdDev"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
}

// EvalJudgeStat 单裁判统计。
type EvalJudgeStat struct {
	ProviderID   int64                `json:"providerId"`
	ProviderName string               `json:"providerName"`
	Model        string               `json:"model"`
	Score        float64              `json:"score"`
	SampleCount  int                  `json:"sampleCount"`
	Dimensions   []EvalDimensionStat  `json:"dimensions"`
	ItemScores   []EvalItemScoreBrief `json:"itemScores"`
}

// EvalItemScoreBrief 单条数据在某裁判下的聚合分。
type EvalItemScoreBrief struct {
	QuestionID int64   `json:"questionId"`
	ItemIndex  int     `json:"itemIndex"`
	Score      float64 `json:"score"`
}

// EvalReport 评估报告：多 LLM 汇总统计、分析、结论。
type EvalReport struct {
	EvalRun        EvalRun              `json:"evalRun"`
	DatasetName    string               `json:"datasetName"`
	OverallScore   float64              `json:"overallScore"`
	JudgeAgreement float64              `json:"judgeAgreement"`
	SampleCount    int                  `json:"sampleCount"`
	Judges         []EvalJudgeStat      `json:"judges"`
	Dimensions     []EvalDimensionStat  `json:"dimensions"`
	WeakestItems   []EvalItemScoreBrief `json:"weakestItems"`
	Conclusions    []string             `json:"conclusions"`
	GeneratedAt    time.Time            `json:"generatedAt"`
}

// EvalRunDetail 评估运行详情（含裁判与维度）。
type EvalRunDetail struct {
	Run        EvalRun         `json:"run"`
	Judges     []EvalRunJudge  `json:"judges"`
	Dimensions []EvalDimension `json:"dimensions"`
}
