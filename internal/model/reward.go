package model

import "time"

type RewardRecord struct {
  ID           int64     `json:"id"`
  DatasetID    int64     `json:"datasetId"`
  QuestionID   int64     `json:"questionId"`
  QuestionText string    `json:"questionText"`
  Score        float64   `json:"score"`
  ObjectKey    string    `json:"objectKey"`
  Status       string    `json:"status"`
  CreatedAt    time.Time `json:"createdAt"`
  UpdatedAt    time.Time `json:"updatedAt"`
}
