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

	// 首次启动时从环境变量引导写入的默认结果存储 profile（issue #83）。
	//
	// 为什么需要它：全新部署下 storage_profiles 表为空，而答案/评分/导出三个阶段
	// 都要写对象存储。没有默认配置时，任务能建成功、却要到答案阶段才以
	// `no rows in result set` 这种内部错误失败，用户既不知原因也不知怎么修。
	//
	// 与 provider 引导同样采用「配置不完整就跳过并告警，不阻断启动」的语义：
	// 一个漏填的 S3_BUCKET 不应把「容器起不来」升级成整服务不可用。
	BootstrapStorageEnabled       bool
	BootstrapStorageName          string
	BootstrapStorageProvider      string
	BootstrapStorageEndpoint      string
	BootstrapStorageRegion        string
	BootstrapStorageBucket        string
	BootstrapStorageAccessKeyID   string
	BootstrapStorageSecretKey     string
	BootstrapStorageUsePathStyle  bool
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

		// 结果存储引导：字段直接复用 compose 里已有的 S3_* 变量。
		// 默认开启（enabled），但只有四项必需信息齐全时才真正写入 —— 见 storage_bootstrap.go。
		// 这样「什么都不配」的全新部署也能开箱可用，而「只配了一半」不会被静默当成成功。
		BootstrapStorageEnabled:      getenvBool("APP_BOOTSTRAP_STORAGE_ENABLED", true),
		BootstrapStorageName:         getenv("APP_BOOTSTRAP_STORAGE_NAME", "默认结果存储"),
		BootstrapStorageProvider:     getenv("S3_PROVIDER", "minio"),
		BootstrapStorageEndpoint:     getenv("S3_ENDPOINT", "http://minio:9000"),
		BootstrapStorageRegion:       getenv("S3_REGION", "us-east-1"),
		BootstrapStorageBucket:       getenv("S3_BUCKET", "llm-factory-dev"),
		BootstrapStorageAccessKeyID:  getenv("S3_ACCESS_KEY", "minioadmin"),
		BootstrapStorageSecretKey:    getenv("S3_SECRET_KEY", "minioadmin"),
		BootstrapStorageUsePathStyle: getenvBool("S3_USE_PATH_STYLE", true),
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

// getenvBool 解析布尔配置。
//
// 只把「显式的假」当作假（false/0/no/off），其余一律用 fallback ——
// 与 getenvInt 同样的思路：拼错的配置不应该静默变成「关掉了某个功能」。
func getenvBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "":
		return fallback
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}
