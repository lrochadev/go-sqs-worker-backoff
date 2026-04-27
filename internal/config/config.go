package config

import (
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
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

	SNSTopicARN    string
	SNSEndpoint    string
	SNSBatchSize   int
	SNSLinger      time.Duration
	SNSPublishers  int
	SNSInputBuffer int
}

func Load() (Config, error) {
	_ = godotenv.Load()

	queueURL := os.Getenv("SQS_QUEUE_URL")
	if queueURL == "" {
		return Config{}, errors.New("SQS_QUEUE_URL is required")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		return Config{}, errors.New("AWS_REGION is required")
	}

	topicARN := os.Getenv("SNS_TOPIC_ARN")
	if topicARN == "" {
		return Config{}, errors.New("SNS_TOPIC_ARN is required")
	}

	sqsEndpoint := os.Getenv("SQS_ENDPOINT")
	snsEndpoint := os.Getenv("SNS_ENDPOINT")
	if snsEndpoint == "" {
		snsEndpoint = sqsEndpoint
	}

	workers := getEnvInt("WORKERS", 10)

	batchSize := getEnvInt("SNS_BATCH_SIZE", 10)
	if batchSize < 1 {
		batchSize = 1
	}
	if batchSize > 10 {
		batchSize = 10
	}

	return Config{
		QueueURL:          queueURL,
		Region:            region,
		Endpoint:          sqsEndpoint,
		Workers:           workers,
		Pollers:           getEnvInt("POLLERS", 3),
		MaxAttempts:       getEnvInt("MAX_ATTEMPTS", 3),
		BaseBackoff:       getEnvDuration("BASE_BACKOFF", 2*time.Second),
		MaxBackoff:        getEnvDuration("MAX_BACKOFF", 60*time.Second),
		VisibilityTimeout: int32(getEnvInt("VISIBILITY_TIMEOUT_SECS", 30)),
		WaitTimeSeconds:   int32(getEnvInt("WAIT_TIME_SECS", 20)),
		ShutdownTimeout:   getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),

		SNSTopicARN:    topicARN,
		SNSEndpoint:    snsEndpoint,
		SNSBatchSize:   batchSize,
		SNSLinger:      getEnvDuration("SNS_LINGER", 150*time.Millisecond),
		SNSPublishers:  getEnvInt("SNS_PUBLISHERS", 8),
		SNSInputBuffer: getEnvInt("SNS_INPUT_BUFFER", workers*4),
	}, nil
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
