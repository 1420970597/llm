package model

import "time"

type Artifact struct {
  ID          int64     `json:"id"`
  DatasetID   int64     `json:"datasetId"`
  ArtifactType string   `json:"artifactType"`
  ObjectKey   string    `json:"objectKey"`
  ContentType string    `json:"contentType"`
  CreatedAt   time.Time `json:"createdAt"`
}

type RuntimeStatus struct {
  DatasetCount   int `json:"datasetCount"`
  QuestionCount  int `json:"questionCount"`
  ReasoningCount int `json:"reasoningCount"`
  RewardCount    int `json:"rewardCount"`
  ArtifactCount  int `json:"artifactCount"`
  QueueDepth     int `json:"queueDepth"`
}
