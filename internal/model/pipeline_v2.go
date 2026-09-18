package model

import "time"

// ChainStep 长链思维标准步骤的单步。可编辑、可版本化。
type ChainStep struct {
	Index       int    `json:"index"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Checkpoint  string `json:"checkpoint"`
}

// ChainStandard 一个方向对应的一份长链思维标准步骤。
type ChainStandard struct {
	ID             int64       `json:"id"`
	DatasetID      int64       `json:"datasetId"`
	DomainID       int64       `json:"domainId"`
	DomainName     string      `json:"domainName"`
	DirectionKey   string      `json:"directionKey"`
	CurrentVersion int         `json:"currentVersion"`
	Status         string      `json:"status"`
	Steps          []ChainStep `json:"steps"`
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`
}

// ChainStandardVersion 版本历史。source: ai / user。
type ChainStandardVersion struct {
	ID         int64       `json:"id"`
	StandardID int64       `json:"standardId"`
	Version    int         `json:"version"`
	Steps      []ChainStep `json:"steps"`
	Source     string      `json:"source"`
	ChangeNote string      `json:"changeNote"`
	CreatedBy  int64       `json:"createdBy"`
	CreatedAt  time.Time   `json:"createdAt"`
}

// ChainStandardUpdateRequest 用户编辑标准步骤的请求体。
type ChainStandardUpdateRequest struct {
	Steps      []ChainStep `json:"steps"`
	ChangeNote string      `json:"changeNote"`
}

// ChainStandardGenerateRequest 触发生成的请求体。
type ChainStandardGenerateRequest struct {
	DomainIDs []int64 `json:"domainIds"`
}

// GrpoPrompt 一个问题的教师模型评判提示词（按打分档次生成）。
type GrpoPrompt struct {
	ID           int64             `json:"id"`
	DatasetID    int64             `json:"datasetId"`
	QuestionID   int64             `json:"questionId"`
	QuestionText string            `json:"questionText"`
	DomainID     int64             `json:"domainId"`
	DomainName   string            `json:"domainName"`
	Levels       []string          `json:"levels"`
	JudgePrompt  string            `json:"judgePrompt"`
	LevelRubrics []GrpoLevelRubric `json:"levelRubrics"`
	FrameworkRef string            `json:"frameworkRef"`
	Status       string            `json:"status"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// GrpoLevelRubric 单档打分对应的评判标准。
type GrpoLevelRubric struct {
	Level      string `json:"level"`
	Label      string `json:"label"`
	Criteria   string `json:"criteria"`
	AcceptCase string `json:"acceptCase"`
	RejectCase string `json:"rejectCase"`
}

// GrpoGenerateRequest 触发 GRPO 提示词生成的请求体。
type GrpoGenerateRequest struct {
	Levels []string `json:"levels"`
}

// SftRecord 一个问题的 SFT 训练样本（思维链 + 答案）。
type SftRecord struct {
	ID             int64       `json:"id"`
	DatasetID      int64       `json:"datasetId"`
	QuestionID     int64       `json:"questionId"`
	QuestionText   string      `json:"questionText"`
	DomainID       int64       `json:"domainId"`
	DomainName     string      `json:"domainName"`
	ChainOfThought string      `json:"chainOfThought"`
	Answer         string      `json:"answer"`
	ChainSteps     []ChainStep `json:"chainSteps"`
	Status         string      `json:"status"`
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`
}

// SftGenerateRequest 触发 SFT 生成的请求体。
type SftGenerateRequest struct {
	IncludeAnswer bool `json:"includeAnswer"`
}

// ExportMapping 导出字段映射。
type ExportMapping struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Format     string         `json:"format"`
	TargetKind string         `json:"targetKind"`
	FieldMap   map[string]any `json:"fieldMap"`
	Options    map[string]any `json:"options"`
	IsBuiltin  bool           `json:"isBuiltin"`
	IsDefault  bool           `json:"isDefault"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// ExportFormatList 支持的格式与映射清单。
type ExportFormatList struct {
	Formats  []string        `json:"formats"`
	Mappings []ExportMapping `json:"mappings"`
}

// ExportRequest 多格式导出请求体。
type ExportRequest struct {
	Format    string         `json:"format"`
	MappingID int64          `json:"mappingId"`
	Filters   map[string]any `json:"filters"`
}

// GenerationRun 生成阶段的运行记录，支持断点续跑。
type GenerationRun struct {
	ID           int64          `json:"id"`
	DatasetID    int64          `json:"datasetId"`
	Stage        string         `json:"stage"`
	Status       string         `json:"status"`
	Cursor       map[string]any `json:"cursor"`
	TotalUnits   int            `json:"totalUnits"`
	DoneUnits    int            `json:"doneUnits"`
	Attempts     int            `json:"attempts"`
	ErrorSummary string         `json:"errorSummary"`
	StartedAt    *time.Time     `json:"startedAt,omitempty"`
	FinishedAt   *time.Time     `json:"finishedAt,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

// DirectionGenerateRequest 方向生成请求体（m 用户可控）。
type DirectionGenerateRequest struct {
	DirectionCount int `json:"directionCount"`
}

// QuestionGenerateRequestV2 问题生成请求体（x 用户可控 + 难度分层）。
type QuestionGenerateRequestV2 struct {
	QuestionsPerDirection int                `json:"questionsPerDirection"`
	DifficultyMix         map[string]float64 `json:"difficultyMix"`
}

// RewardLevelsRequest GRPO 打分档次设置请求体。
type RewardLevelsRequest struct {
	Levels []string `json:"levels"`
}

// RewardLevelsResponse GRPO 打分档次设置响应。
type RewardLevelsResponse struct {
	Levels []string `json:"levels"`
}

// DifficultyStats 难度分布统计。
type DifficultyStats struct {
	Levels map[string]int `json:"levels"`
	Total  int            `json:"total"`
}
