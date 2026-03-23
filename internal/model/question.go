package model

import "time"

type Question struct {
  ID            int64     `json:"id"`
  DatasetID     int64     `json:"datasetId"`
  DomainID      int64     `json:"domainId"`
  DomainName    string    `json:"domainName"`
  Content       string    `json:"content"`
  CanonicalHash string    `json:"canonicalHash"`
  Status        string    `json:"status"`
  CreatedAt     time.Time `json:"createdAt"`
  UpdatedAt     time.Time `json:"updatedAt"`
}
