package model

import "time"

// CleaningKeyword 拒答/违规关键词。match_mode: contains / regex / prefix。
type CleaningKeyword struct {
	ID        int64     `json:"id"`
	Pattern   string    `json:"pattern"`
	Category  string    `json:"category"`
	MatchMode string    `json:"matchMode"`
	Severity  string    `json:"severity"`
	IsBuiltin bool      `json:"isBuiltin"`
	IsActive  bool      `json:"isActive"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CleaningRule 清洗规则。stage_scope 限定生效阶段，action: drop / flag / retry。
type CleaningRule struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	StageScope []string       `json:"stageScope"`
	MinHits    int            `json:"minHits"`
	Action     string         `json:"action"`
	Priority   int            `json:"priority"`
	IsActive   bool           `json:"isActive"`
	Config     map[string]any `json:"config"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// CleaningRun 一次清洗运行。
type CleaningRun struct {
	ID           int64          `json:"id"`
	DatasetID    int64          `json:"datasetId"`
	Stages       []string       `json:"stages"`
	Status       string         `json:"status"`
	ScannedItems int            `json:"scannedItems"`
	FlaggedItems int            `json:"flaggedItems"`
	DroppedItems int            `json:"droppedItems"`
	Report       map[string]any `json:"report"`
	ErrorSummary string         `json:"errorSummary"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

// CleaningFinding 单条命中明细。
type CleaningFinding struct {
	ID            int64     `json:"id"`
	CleaningRunID int64     `json:"cleaningRunId"`
	DatasetID     int64     `json:"datasetId"`
	QuestionID    int64     `json:"questionId"`
	Stage         string    `json:"stage"`
	KeywordID     int64     `json:"keywordId"`
	MatchedText   string    `json:"matchedText"`
	Snippet       string    `json:"snippet"`
	Action        string    `json:"action"`
	CreatedAt     time.Time `json:"createdAt"`
}

// CleaningStageStat 分阶段统计。
type CleaningStageStat struct {
	Stage        string  `json:"stage"`
	ScannedItems int     `json:"scannedItems"`
	FlaggedItems int     `json:"flaggedItems"`
	DroppedItems int     `json:"droppedItems"`
	HitRate      float64 `json:"hitRate"`
}

// CleaningKeywordStat 单关键词命中统计。
type CleaningKeywordStat struct {
	KeywordID   int64  `json:"keywordId"`
	Pattern     string `json:"pattern"`
	Category    string `json:"category"`
	Hits        int    `json:"hits"`
	SampleSnipp string `json:"sampleSnippet"`
}

// CleaningReport 清洗报告。
type CleaningReport struct {
	Run         CleaningRun           `json:"run"`
	Stages      []CleaningStageStat   `json:"stages"`
	TopKeywords []CleaningKeywordStat `json:"topKeywords"`
	Conclusions []string              `json:"conclusions"`
	GeneratedAt time.Time             `json:"generatedAt"`
}

// CleaningRunRequest 触发清洗的请求体。
type CleaningRunRequest struct {
	Stages  []string `json:"stages"`
	RuleIDs []int64  `json:"ruleIds"`
}

// CleaningKeywordImportRequest 批量导入关键词。
type CleaningKeywordImportRequest struct {
	Patterns []string `json:"patterns"`
	Category string   `json:"category"`
	Severity string   `json:"severity"`
}
