package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"

	appconfig "go-sqs-worker-backoff/internal/config"
	appsqs "go-sqs-worker-backoff/internal/sqs"
)

type SQSAPI interface {
	Receive(ctx context.Context) ([]types.Message, error)
	Delete(ctx context.Context, receipt *string) error
	ChangeVisibility(ctx context.Context, receipt *string, seconds int32) error
}

type Handler interface {
	Handle(ctx context.Context, msg types.Message) error
}

type Pool struct {
	cfg      appconfig.Config
	consumer SQSAPI
	handler  Handler
	log      *zap.Logger
}

func New(cfg appconfig.Config, consumer SQSAPI, handler Handler, log *zap.Logger) *Pool {
	return &Pool{cfg: cfg, consumer: consumer, handler: handler, log: log}
}

func (p *Pool) Start(ctx context.Context) {
	jobs := make(chan types.Message, p.cfg.Workers*2)

	var workerWG sync.WaitGroup
	for i := 0; i < p.cfg.Workers; i++ {
		workerWG.Add(1)
		go func(id int) {
			defer workerWG.Done()
			p.runWorker(ctx, id, jobs)
		}(i)
	}

	var pollerWG sync.WaitGroup
	for i := 0; i < p.cfg.Pollers; i++ {
		pollerWG.Add(1)
		go func(id int) {
			defer pollerWG.Done()
			p.runPoller(ctx, id, jobs)
		}(i)
	}

	pollerWG.Wait()
	close(jobs)
	p.log.Info("pollers stopped, waiting for workers to drain")
	workerWG.Wait()
	p.log.Info("all workers stopped")
}

func (p *Pool) runPoller(ctx context.Context, id int, jobs chan<- types.Message) {
	log := p.log.With(zap.Int("poller_id", id))
	errBackoff := backoff.NewExponentialBackOff()
	errBackoff.InitialInterval = 500 * time.Millisecond
	errBackoff.MaxInterval = 10 * time.Second
	errBackoff.MaxElapsedTime = 0

	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := p.consumer.Receive(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			wait := errBackoff.NextBackOff()
			log.Error("receive error, backing off", zap.Error(err), zap.Duration("wait", wait))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			continue
		}
		errBackoff.Reset()

		for _, m := range msgs {
			select {
			case jobs <- m:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (p *Pool) runWorker(ctx context.Context, id int, jobs <-chan types.Message) {
	log := p.log.With(zap.Int("worker_id", id))
	for msg := range jobs {
		p.handleMessage(ctx, log, msg)
	}
}

func (p *Pool) handleMessage(ctx context.Context, log *zap.Logger, msg types.Message) {
	receivedAt := time.Now()
	attempt := appsqs.Attempts(msg)
	messageID := ""
	if msg.MessageId != nil {
		messageID = *msg.MessageId
	}

	log = log.With(
		zap.String("message_id", messageID),
		zap.Int("attempt", attempt),
		zap.Int("max_attempts", p.cfg.MaxAttempts),
		zap.Time("received_at", receivedAt),
	)

	if attempt > 1 {
		log.Info("reprocessing message (retry)",
			zap.Int("retry_number", attempt-1),
		)
	}

	err := p.handler.Handle(ctx, msg)
	duration := time.Since(receivedAt)

	if err == nil {
		if delErr := p.consumer.Delete(ctx, msg.ReceiptHandle); delErr != nil {
			log.Error("delete failed after success", zap.Error(delErr))
			return
		}
		log.Info("message processed", zap.Duration("duration", duration))
		return
	}

	if attempt >= p.cfg.MaxAttempts {
		log.Error("max attempts reached, letting SQS route to DLQ",
			zap.Error(err),
			zap.Duration("duration", duration),
		)
		return
	}

	delay := backoffDelay(p.cfg.BaseBackoff, p.cfg.MaxBackoff, attempt)
	nextVisibleAt := time.Now().Add(delay)

	log.Warn("processing failed, scheduling retry via ChangeMessageVisibility",
		zap.Error(err),
		zap.Duration("duration", duration),
		zap.Duration("delay", delay),
		zap.Time("next_visible_at", nextVisibleAt),
	)

	if cvErr := p.consumer.ChangeVisibility(ctx, msg.ReceiptHandle, int32(delay.Seconds())); cvErr != nil {
		log.Error("change visibility failed", zap.Error(cvErr))
	}
}

// backoffDelay returns a full-jitter exponential backoff derived from the
// current attempt. Each delivery recomputes independently, so we advance the
// backoff generator attempt times to get the current interval.
func backoffDelay(base, max time.Duration, attempt int) time.Duration {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = base
	b.MaxInterval = max
	b.RandomizationFactor = 1.0 // full jitter
	b.Multiplier = 2.0
	b.MaxElapsedTime = 0
	b.Reset()

	var d time.Duration
	n := attempt
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		d = b.NextBackOff()
	}
	// ensure at least 1 second (SQS minimum visibility change is 0 but we want observable delay)
	if d < time.Second {
		d = time.Second
	}
	// cap, defensive
	if d > max {
		d = max
	}
	return d
}
