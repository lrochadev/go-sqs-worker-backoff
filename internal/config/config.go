package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	QueueURL          string
	Region            string
	Endpoint          string
	Workers           int
	Pollers           int
	MaxAttempts       int
	BaseBackoff       time.Duration
	MaxBackoff        time.Duration
	VisibilityTimeout int32
	WaitTimeSeconds   int32
	ShutdownTimeout   time.Duration
}

func Load() Config {
	return Config{
		QueueURL:          getEnv("SQS_QUEUE_URL", "http://localhost:4566/000000000000/planet-queue"),
		Region:            getEnv("AWS_REGION", "us-east-1"),
		Endpoint:          getEnv("SQS_ENDPOINT", "http://localhost:4566"),
		Workers:           getEnvInt("WORKERS", 10),
		Pollers:           getEnvInt("POLLERS", 3),
		MaxAttempts:       getEnvInt("MAX_ATTEMPTS", 3),
		BaseBackoff:       getEnvDuration("BASE_BACKOFF", 2*time.Second),
		MaxBackoff:        getEnvDuration("MAX_BACKOFF", 60*time.Second),
		VisibilityTimeout: int32(getEnvInt("VISIBILITY_TIMEOUT_SECS", 30)),
		WaitTimeSeconds:   int32(getEnvInt("WAIT_TIME_SECS", 20)),
		ShutdownTimeout:   getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
