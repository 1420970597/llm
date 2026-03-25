package model

import "time"

type PlanEstimate struct {
  DomainCount        int `json:"domainCount"`
  QuestionsPerDomain int `json:"questionsPerDomain"`
  AnswerVariants     int `json:"answerVariants"`
  RewardVariants     int `json:"rewardVariants"`
  EstimatedQuestions int `json:"estimatedQuestions"`
  EstimatedSamples   int `json:"estimatedSamples"`
}

type Dataset struct {
  ID               int64        `json:"id"`
  Name             string       `json:"name"`
  RootKeyword      string       `json:"rootKeyword"`
  TargetSize       int          `json:"targetSize"`
  Status           string       `json:"status"`
  StrategyID       int64        `json:"strategyId"`
  ProviderID       int64        `json:"providerId"`
  StorageProfileID int64        `json:"storageProfileId"`
  Estimate         PlanEstimate `json:"estimate"`
  CreatedAt        time.Time    `json:"createdAt"`
  UpdatedAt        time.Time    `json:"updatedAt"`
}

type Domain struct {
  ID           int64     `json:"id"`
  DatasetID    int64     `json:"datasetId"`
  Name         string    `json:"name"`
  Canonical    string    `json:"canonicalName"`
  Level        int       `json:"level"`
  ParentID     *int64    `json:"parentId,omitempty"`
  Source       string    `json:"source"`
  ReviewStatus string    `json:"reviewStatus"`
  CreatedAt    time.Time `json:"createdAt"`
  UpdatedAt    time.Time `json:"updatedAt"`
}

type DomainEdge struct {
  ID         int64     `json:"id"`
  DatasetID  int64     `json:"datasetId"`
  SourceID   int64     `json:"sourceId"`
  TargetID   int64     `json:"targetId"`
  Relation   string    `json:"relation"`
  CreatedAt  time.Time `json:"createdAt"`
}

type DatasetGraph struct {
  Dataset Dataset      `json:"dataset"`
  Domains []Domain     `json:"domains"`
  Edges   []DomainEdge `json:"edges"`
}

type PipelineStageStatus struct {
  Key      string `json:"key"`
  Label    string `json:"label"`
  State    string `json:"state"`
  Count    int    `json:"count"`
  Summary  string `json:"summary"`
}

type PipelineProgress struct {
  DatasetID         int64                 `json:"datasetId"`
  DatasetStatus     string                `json:"datasetStatus"`
  CurrentStage      string                `json:"currentStage"`
  CompletionPercent int                   `json:"completionPercent"`
  Stages            []PipelineStageStatus `json:"stages"`
  QuestionCount     int                   `json:"questionCount"`
  ReasoningCount    int                   `json:"reasoningCount"`
  RewardCount       int                   `json:"rewardCount"`
  ArtifactCount     int                   `json:"artifactCount"`
}

type StageEnqueueResult struct {
  DatasetID  int64  `json:"datasetId"`
  Stage      string `json:"stage"`
  State      string `json:"state"`
  Message    string `json:"message"`
  AcceptedAt string `json:"acceptedAt"`
}

type GeneratePlanRequest struct {
  RootKeyword string `json:"rootKeyword"`
  TargetSize  int    `json:"targetSize"`
  StrategyID  int64  `json:"strategyId"`
}
