package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type APIConfig struct {
	Environment          string
	Port                 string
	PostgresDSN          string
	PostgresHost         string
	PostgresPort         string
	PostgresDatabase     string
	PostgresUser         string
	PostgresPassword     string
	RedisHost            string
	RedisPort            string
	S3Endpoint           string
	EncryptionKey        string
	AllowedOrigin        string
	MigrationPath        string
	QueueName            string
	DefaultAdminEmail    string
	DefaultAdminPassword string
	DefaultUserEmail     string
	DefaultUserPassword  string

	// 首次启动时从环境变量引导写入的默认 LLM provider。
	BootstrapProviderName           string
	BootstrapProviderBaseURL        string
	BootstrapProviderModel          string
	BootstrapProviderAPIKey         string
	BootstrapProviderType           string
	BootstrapProviderTimeoutSeconds int
	BootstrapProviderMaxConcurrency int
}

type WorkerConfig struct {
	Environment      string
	Port             string
	PostgresDSN      string
	PostgresHost     string
	PostgresPort     string
	PostgresDatabase string
	PostgresUser     string
	PostgresPassword string
	RedisHost        string
	RedisPort        string
	QueueName        string
	EncryptionKey    string
	MigrationPath    string
}

func LoadAPIConfig() APIConfig {
	host := getenv("POSTGRES_HOST", "postgres")
	port := getenv("POSTGRES_PORT", "5432")
	db := getenv("POSTGRES_DB", "llm_factory")
	user := getenv("POSTGRES_USER", "llm_factory")
	password := getenv("POSTGRES_PASSWORD", "llm_factory_dev")

	return APIConfig{
		Environment:          getenv("APP_ENV", "development"),
		Port:                 getenv("API_PORT", "8080"),
		PostgresDSN:          getenv("POSTGRES_DSN", fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, db)),
		PostgresHost:         host,
		PostgresPort:         port,
		PostgresDatabase:     db,
		PostgresUser:         user,
		PostgresPassword:     password,
		RedisHost:            getenv("REDIS_HOST", "redis"),
		RedisPort:            getenv("REDIS_PORT", "6379"),
		S3Endpoint:           getenv("S3_ENDPOINT", "http://minio:9000"),
		EncryptionKey:        getenv("APP_ENCRYPTION_KEY", "phase1-dev-only-32-byte-secret!!!"),
		AllowedOrigin:        getenv("APP_ALLOWED_ORIGIN", "*"),
		MigrationPath:        getenv("MIGRATION_PATH", "sql/migrations"),
		QueueName:            getenv("WORKER_QUEUE_NAME", "dataset-generation"),
		DefaultAdminEmail:    getenv("APP_DEFAULT_ADMIN_EMAIL", "admin@company.com"),
		DefaultAdminPassword: getenv("APP_DEFAULT_ADMIN_PASSWORD", "admin123456"),
		DefaultUserEmail:     getenv("APP_DEFAULT_USER_EMAIL", "user@company.com"),
		DefaultUserPassword:  getenv("APP_DEFAULT_USER_PASSWORD", "user123456"),

		BootstrapProviderName:           getenv("APP_BOOTSTRAP_PROVIDER_NAME", "default-provider"),
		BootstrapProviderBaseURL:        getenv("APP_BOOTSTRAP_PROVIDER_BASE_URL", ""),
		BootstrapProviderModel:          getenv("APP_BOOTSTRAP_PROVIDER_MODEL", ""),
		BootstrapProviderAPIKey:         getenv("APP_BOOTSTRAP_PROVIDER_API_KEY", ""),
		BootstrapProviderType:           getenv("APP_BOOTSTRAP_PROVIDER_TYPE", "openai-compatible"),
		BootstrapProviderTimeoutSeconds: getenvInt("APP_BOOTSTRAP_PROVIDER_TIMEOUT_SECONDS", 120),
		BootstrapProviderMaxConcurrency: getenvInt("APP_BOOTSTRAP_PROVIDER_MAX_CONCURRENCY", 4),
	}
}

func LoadWorkerConfig() WorkerConfig {
	host := getenv("POSTGRES_HOST", "postgres")
	port := getenv("POSTGRES_PORT", "5432")
	db := getenv("POSTGRES_DB", "llm_factory")
	user := getenv("POSTGRES_USER", "llm_factory")
	password := getenv("POSTGRES_PASSWORD", "llm_factory_dev")

	return WorkerConfig{
		Environment:      getenv("APP_ENV", "development"),
		Port:             getenv("WORKER_PORT", "8081"),
		PostgresDSN:      getenv("POSTGRES_DSN", fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, db)),
		PostgresHost:     host,
		PostgresPort:     port,
		PostgresDatabase: db,
		PostgresUser:     user,
		PostgresPassword: password,
		RedisHost:        getenv("REDIS_HOST", "redis"),
		RedisPort:        getenv("REDIS_PORT", "6379"),
		QueueName:        getenv("WORKER_QUEUE_NAME", "dataset-generation"),
		EncryptionKey:    getenv("APP_ENCRYPTION_KEY", "phase1-dev-only-32-byte-secret!!!"),
		MigrationPath:    getenv("MIGRATION_PATH", "sql/migrations"),
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
