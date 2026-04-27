package sns

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	appconfig "go-sqs-worker-backoff/internal/config"
)

// NoopPublisher mirrors Publisher's batching/aggregation, but skips the SNS
// PublishBatch leg entirely and acks directly via the BatchDeleter. Used to
// isolate the cost of SNS roundtrips during benchmarks (SNS_ENABLED=false).
type NoopPublisher struct {
	deleter   BatchDeleter
	batchSize int
	linger    time.Duration

	input chan string
	wg    sync.WaitGroup
	once  sync.Once

	log *zap.Logger
}

func NewNoop(cfg appconfig.Config, deleter BatchDeleter, log *zap.Logger) *NoopPublisher {
	buf := cfg.SNSInputBuffer
	if buf < cfg.SNSBatchSize {
		buf = cfg.SNSBatchSize
	}
	return &NoopPublisher{
		deleter:   deleter,
		batchSize: cfg.SNSBatchSize,
		linger:    cfg.SNSLinger,
		input:     make(chan string, buf),
		log:       log,
	}
}

func (p *NoopPublisher) Start(ctx context.Context) {
	p.wg.Add(1)
	go p.run(ctx)
}

func (p *NoopPublisher) Enqueue(_, receipt string) {
	p.input <- receipt
}

func (p *NoopPublisher) Shutdown() {
	p.once.Do(func() { close(p.input) })
	p.wg.Wait()
}

func (p *NoopPublisher) run(ctx context.Context) {
	defer p.wg.Done()

	buf := make([]string, 0, p.batchSize)
	timer := time.NewTimer(p.linger)
	timer.Stop()
	armed := false

	flush := func() {
		if len(buf) == 0 {
			return
		}
		batch := make([]string, len(buf))
		copy(batch, buf)
		buf = buf[:0]
		if _, err := p.deleter.DeleteBatch(ctx, batch); err != nil {
			p.log.Error("noop publisher delete batch failed", zap.Error(err), zap.Int("count", len(batch)))
		}
		if armed {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			armed = false
		}
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			for r := range p.input {
				buf = append(buf, r)
				if len(buf) >= p.batchSize {
					flush()
				}
			}
			flush()
			return
		case r, ok := <-p.input:
			if !ok {
				flush()
				return
			}
			buf = append(buf, r)
			if len(buf) >= p.batchSize {
				flush()
				continue
			}
			if !armed {
				timer.Reset(p.linger)
				armed = true
			}
		case <-timer.C:
			armed = false
			flush()
		}
	}
}
