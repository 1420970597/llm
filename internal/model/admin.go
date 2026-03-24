package model

import "time"

type ModelProvider struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	BaseURL         string    `json:"baseUrl"`
	Model           string    `json:"model"`
	ProviderType    string    `json:"providerType"`
	ReasoningEffort string    `json:"reasoningEffort"`
	MaxConcurrency  int       `json:"maxConcurrency"`
	TimeoutSeconds  int       `json:"timeoutSeconds"`
	IsActive        bool      `json:"isActive"`
	APIKey          string    `json:"apiKey,omitempty"`
	APIKeyMasked    string    `json:"apiKeyMasked"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ProviderModelInfo struct {
	ID string `json:"id"`
}

type ProviderModelsResponse struct {
	Models []ProviderModelInfo `json:"models"`
}

type ProviderConnectivityResult struct {
	OK              bool     `json:"ok"`
	StatusCode      int      `json:"statusCode"`
	LatencyMs       int64    `json:"latencyMs"`
	Message         string   `json:"message"`
	ModelFound      bool     `json:"modelFound"`
	AvailableModels []string `json:"availableModels,omitempty"`
}

type StorageProfile struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Provider        string    `json:"provider"`
	Endpoint        string    `json:"endpoint"`
	Region          string    `json:"region"`
	Bucket          string    `json:"bucket"`
	AccessKeyID     string    `json:"accessKeyId"`
	SecretAccessKey string    `json:"secretAccessKey,omitempty"`
	SecretKeyMasked string    `json:"secretKeyMasked"`
	UsePathStyle    bool      `json:"usePathStyle"`
	IsDefault       bool      `json:"isDefault"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type GenerationStrategy struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	DomainCount        int       `json:"domainCount"`
	QuestionsPerDomain int       `json:"questionsPerDomain"`
	AnswerVariants     int       `json:"answerVariants"`
	RewardVariants     int       `json:"rewardVariants"`
	PlanningMode       string    `json:"planningMode"`
	IsDefault          bool      `json:"isDefault"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type PromptTemplate struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Stage        string    `json:"stage"`
	Version      string    `json:"version"`
	SystemPrompt string    `json:"systemPrompt"`
	UserPrompt   string    `json:"userPrompt"`
	IsActive     bool      `json:"isActive"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type AuditLog struct {
	ID           int64     `json:"id"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Detail       string    `json:"detail"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AdminDashboard struct {
	ProviderCount       int `json:"providerCount"`
	ActiveProviderCount int `json:"activeProviderCount"`
	StorageProfileCount int `json:"storageProfileCount"`
	StrategyCount       int `json:"strategyCount"`
	PromptCount         int `json:"promptCount"`
	AuditLogCount       int `json:"auditLogCount"`
}
