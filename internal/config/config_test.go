package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("SQS_QUEUE_URL", "http://localhost/q")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:planet-topic")
}

func TestLoad_Defaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 10, cfg.Workers)
	assert.Equal(t, 3, cfg.Pollers)
	assert.Equal(t, 3, cfg.MaxAttempts)
	assert.Equal(t, 2*time.Second, cfg.BaseBackoff)
	assert.Equal(t, 60*time.Second, cfg.MaxBackoff)
	assert.Equal(t, int32(30), cfg.VisibilityTimeout)
	assert.Equal(t, int32(20), cfg.WaitTimeSeconds)
	assert.Equal(t, 30*time.Second, cfg.ShutdownTimeout)

	assert.Equal(t, 10, cfg.SNSBatchSize)
	assert.Equal(t, 150*time.Millisecond, cfg.SNSLinger)
	assert.Equal(t, 8, cfg.SNSPublishers)
	assert.Equal(t, 40, cfg.SNSInputBuffer, "default = workers*4")
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Run("missing queue url", func(t *testing.T) {
		t.Setenv("SQS_QUEUE_URL", "")
		t.Setenv("AWS_REGION", "us-east-1")
		t.Setenv("SNS_TOPIC_ARN", "arn")
		_, err := Load()
		require.Error(t, err)
	})

	t.Run("missing region", func(t *testing.T) {
		t.Setenv("SQS_QUEUE_URL", "u")
		t.Setenv("AWS_REGION", "")
		t.Setenv("SNS_TOPIC_ARN", "arn")
		_, err := Load()
		require.Error(t, err)
	})

	t.Run("missing topic arn", func(t *testing.T) {
		t.Setenv("SQS_QUEUE_URL", "u")
		t.Setenv("AWS_REGION", "us-east-1")
		t.Setenv("SNS_TOPIC_ARN", "")
		_, err := Load()
		require.Error(t, err)
	})
}

func TestLoad_BatchSizeClamped(t *testing.T) {
	setRequired(t)
	t.Setenv("SNS_BATCH_SIZE", "25")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 10, cfg.SNSBatchSize, "must clamp to AWS limit")
}

func TestLoad_SNSEndpointFallsBackToSQSEndpoint(t *testing.T) {
	setRequired(t)
	t.Setenv("SQS_ENDPOINT", "http://localhost:4566")
	t.Setenv("SNS_ENDPOINT", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:4566", cfg.SNSEndpoint)
}

func TestLoad_OverrideTuning(t *testing.T) {
	setRequired(t)
	t.Setenv("WORKERS", "20")
	t.Setenv("SNS_LINGER", "50ms")
	t.Setenv("SNS_PUBLISHERS", "16")
	t.Setenv("SNS_INPUT_BUFFER", "123")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 20, cfg.Workers)
	assert.Equal(t, 50*time.Millisecond, cfg.SNSLinger)
	assert.Equal(t, 16, cfg.SNSPublishers)
	assert.Equal(t, 123, cfg.SNSInputBuffer)
}
