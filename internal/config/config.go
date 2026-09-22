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
	BootstrapStorageEnabled      bool
	BootstrapStorageName         string
	BootstrapStorageProvider     string
	BootstrapStorageEndpoint     string
	BootstrapStorageRegion       string
	BootstrapStorageBucket       string
	BootstrapStorageAccessKeyID  string
	BootstrapStorageSecretKey    string
	BootstrapStorageUsePathStyle bool

	// StudioEnabled 是 Atelier 新能力的**总开关**（Issue #160 T33）。
	//
	// 语义：false 时**拒绝新的 Studio 命令**（设计/运行/判断/发布），
	// 但读取与已发布文件下载仍然可用。回退时用户还能把数据拿走 ——
	// 一个「关掉后连自己已经发布的东西都下载不了」的开关不会被人敢用。
	StudioEnabled bool
	// StudioDisabledProjectIDs 是项目级回退名单（灰度用）。
	// 用环境变量而不是表：回退必须在**不依赖数据库写权限**的前提下可用，
	// 而一次部署就能同时改完所有副本。
	StudioDisabledProjectIDs []int64
	// StudioDisabledProjectsRaw 保留原始写法以支持「id:原因」形式
	//（例如 `12:发布积压,13:成本失控`）：只记 ID 的回退名单在一周后
	// 没人记得为什么被关，而原因会显示在状态接口里。
	StudioDisabledProjectsRaw string
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

	// StudioQueueName 是 Atelier 新作业（`studio.*`）的独立队列（Issue #160 T06）。
	//
	// 为什么**必须**是独立队列而不是复用 QueueName：旧消费者按
	// `{type, datasetId}` 解析消息，它既不认识 `{schemaVersion, jobId}`，
	// 也不可能在解析失败时保持沉默（`type` 为空会走 `default:` 分支打印
	// 「worker ignored job type=」然后**丢掉消息**）。用两条队列让
	// 「旧 worker 不得误吞新消息」成为结构性事实，而不是靠双方的约定。
	StudioQueueName string

	// StudioConcurrency 是单进程同时处理的 Studio 作业数上界。
	//
	// 串行（=1）会让一个长批次占满 worker，用户看到别的批次一直排队；
	// 无上限则无法解释「在途数量」与预算预留。取一个小上界：
	// 批次内部的并发度由批次自己的 generation_config.concurrency 决定
	//（T12），这里只控制「同时有几个批次在跑」。
	StudioConcurrency int

	// StudioEnabled 是 worker 侧的同一开关（T33）。
	//
	// false 时 worker **不再抢占新的 Studio 作业**，但正在执行的那个作业
	// 会正常跑完（ctx 不被取消）。“停止新请求，在途仍会完成”与 T13 的
	// 暂停语义一致 —— 中途杀进程会把一个已经花钱的批次丢在中途。
	StudioEnabled bool
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

		// Atelier 特性开关（Issue #160 T33）。
		// 默认**开启**：新能力是当前主线，关掉它属于运维动作。
		StudioEnabled:             getenvBool("STUDIO_ENABLED", true),
		StudioDisabledProjectIDs:  parseInt64List(getenv("STUDIO_DISABLED_PROJECT_IDS", "")),
		StudioDisabledProjectsRaw: getenv("STUDIO_DISABLED_PROJECT_IDS", ""),
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

		// 默认在旧队列名后加 `-studio`：一个只需要 `WORKER_QUEUE_NAME` 的部署
		// 自动获得两条互不干扰的队列，无需运维记忆两个变量；
		// 需要时用 WORKER_STUDIO_QUEUE_NAME 覆盖。
		StudioQueueName:   getenv("WORKER_STUDIO_QUEUE_NAME", getenv("WORKER_QUEUE_NAME", "dataset-generation")+"-studio"),
		StudioConcurrency: getenvInt("WORKER_STUDIO_CONCURRENCY", 2),
		// 默认与 API 侧同值：一个只改一侧的部署会让「API 停止新命令、
		// worker 继续跑旧队列」变成长期状态（而不是回退状态）。
		StudioEnabled: getenvBool("STUDIO_ENABLED", true),
	}
}

// parseInt64List 解析逗号分隔的整数列表（用于项目级回退名单）。
//
// 非法项被**保留为可诊断的信号**而不是静默丢弃：调用方（rollout）会把
// 解析失败的项目当作「未知」并在状态里报告。这里的契约是「只解析，不判断」。
func parseInt64List(raw string) []int64 {
	values := []int64{}
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed <= 0 {
			continue
		}
		values = append(values, parsed)
	}
	return values
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
