package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	_ "go.uber.org/automaxprocs"
	"go.uber.org/zap"

	"go-sqs-worker-backoff/internal/config"
	"go-sqs-worker-backoff/internal/logging"
	"go-sqs-worker-backoff/internal/metrics"
	appsns "go-sqs-worker-backoff/internal/sns"
	appsqs "go-sqs-worker-backoff/internal/sqs"
	"go-sqs-worker-backoff/internal/worker"
)

type Planet struct {
	Name    string `json:"name"`
	Climate string `json:"climate"`
	Terrain string `json:"terrain"`
}

type PlanetHandler struct {
	log *zap.Logger
}

func (h *PlanetHandler) Handle(_ context.Context, msg types.Message) error {
	if msg.Body == nil {
		return fmt.Errorf("empty body")
	}
	var p Planet
	if err := json.Unmarshal([]byte(*msg.Body), &p); err != nil {
		return fmt.Errorf("json unmarshal: %w", err)
	}
	if p.Name == "" {
		return fmt.Errorf("planet name is empty")
	}
	h.log.Debug("planet received",
		zap.String("name", p.Name),
		zap.String("climate", p.Climate),
		zap.String("terrain", p.Terrain),
	)
	return nil
}

func main() {
	logger := logging.New()
	defer func() { _ = logger.Sync() }()

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("invalid config", zap.Error(err))
	}
	logger.Info("starting worker",
		zap.String("queue", cfg.QueueURL),
		zap.Int("workers", cfg.Workers),
		zap.Int("pollers", cfg.Pollers),
		zap.Int("max_attempts", cfg.MaxAttempts),
		zap.Duration("base_backoff", cfg.BaseBackoff),
		zap.Duration("max_backoff", cfg.MaxBackoff),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	metrics.MustRegister()
	metricsAddr := os.Getenv("METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":8080"
	}
	go func() {
		if err := metrics.Serve(ctx, metricsAddr, logger); err != nil {
			logger.Error("metrics server error", zap.Error(err))
		}
	}()

	consumer, err := appsqs.NewConsumer(ctx, cfg)
	if err != nil {
		logger.Fatal("failed to create consumer", zap.Error(err))
	}

	publisher, err := appsns.New(ctx, cfg, consumer, logger)
	if err != nil {
		logger.Fatal("failed to create sns publisher", zap.Error(err))
	}
	publisher.Start(ctx)
	logger.Info("sns publisher started",
		zap.String("topic_arn", cfg.SNSTopicARN),
		zap.Int("batch_size", cfg.SNSBatchSize),
		zap.Duration("linger", cfg.SNSLinger),
		zap.Int("publishers", cfg.SNSPublishers),
	)

	handler := &PlanetHandler{log: logger}
	pool := worker.New(cfg, consumer, handler, publisher, logger)

	pool.Start(ctx)
	logger.Info("pool stopped, flushing sns publisher")
	publisher.Shutdown()
	logger.Info("shutdown complete")
}
