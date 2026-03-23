package config

import "os"

type APIConfig struct {
  Environment  string
  Port         string
  PostgresHost string
  RedisHost    string
  S3Endpoint   string
}

type WorkerConfig struct {
  Environment string
  Port        string
  RedisHost   string
  RedisPort   string
  QueueName   string
}

func LoadAPIConfig() APIConfig {
  return APIConfig{
    Environment:  getenv("APP_ENV", "development"),
    Port:         getenv("API_PORT", "8080"),
    PostgresHost: getenv("POSTGRES_HOST", "postgres"),
    RedisHost:    getenv("REDIS_HOST", "redis"),
    S3Endpoint:   getenv("S3_ENDPOINT", "http://minio:9000"),
  }
}

func LoadWorkerConfig() WorkerConfig {
  return WorkerConfig{
    Environment: getenv("APP_ENV", "development"),
    Port:        getenv("WORKER_PORT", "8081"),
    RedisHost:   getenv("REDIS_HOST", "redis"),
    RedisPort:   getenv("REDIS_PORT", "6379"),
    QueueName:   getenv("WORKER_QUEUE_NAME", "dataset-generation"),
  }
}

func getenv(key, fallback string) string {
  if value := os.Getenv(key); value != "" {
    return value
  }
  return fallback
}
