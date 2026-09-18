package model

import "time"

// Question 一个具体问题。difficulty: easy / medium / hard。
// direction_domain_id 指向生成该问题的「方向」（level=2 的 domain）。
type Question struct {
	ID                int64     `json:"id"`
	DatasetID         int64     `json:"datasetId"`
	DomainID          int64     `json:"domainId"`
	DomainName        string    `json:"domainName"`
	DirectionDomainID int64     `json:"directionDomainId"`
	Content           string    `json:"content"`
	CanonicalHash     string    `json:"canonicalHash"`
	DedupeKey         string    `json:"dedupeKey"`
	Difficulty        string    `json:"difficulty"`
	DifficultyScore   int       `json:"difficultyScore"`
	Source            string    `json:"source"`
	CleaningStatus    string    `json:"cleaningStatus"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}
