package sns

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/google/uuid"
	"go.uber.org/zap"

	appconfig "go-sqs-worker-backoff/internal/config"
	"go-sqs-worker-backoff/internal/metrics"
)

// snsAPI is the package-private surface of *sns.Client we depend on.
// Kept unexported so the public package API stays focused on Publisher itself.
type snsAPI interface {
	PublishBatch(ctx context.Context, params *sns.PublishBatchInput, optFns ...func(*sns.Options)) (*sns.PublishBatchOutput, error)
}

// BatchDeleter is the consumer-side interface declaring what Publisher needs
// from its downstream ack target. Lives here because the Publisher consumes it.
type BatchDeleter interface {
	DeleteBatch(ctx context.Context, receipts []string) ([]string, error)
}

// Entry is a single message queued for publish. ReceiptHandle links back to
// SQS so that a successful publish can trigger a batched delete.
type Entry struct {
	Body          string
	ReceiptHandle string
}

type entry struct {
	body          string
	receipt       string
	entryID       string
	correlationID string
}

type Publisher struct {
	client     snsAPI
	deleter    BatchDeleter
	topicARN   string
	batchSize  int
	linger     time.Duration
	publishers int

	input     chan entry
	batches   chan []entry
	aggWG     sync.WaitGroup
	publishWG sync.WaitGroup
	once      sync.Once

	log *zap.Logger
}

// New builds a Publisher with a real *sns.Client. LocalStack override mirrors
// internal/sqs/consumer.go.
func New(ctx context.Context, cfg appconfig.Config, deleter BatchDeleter, log *zap.Logger) (*Publisher, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.SNSEndpoint != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}

	var clientOpts []func(*sns.Options)
	if cfg.SNSEndpoint != "" {
		ep := cfg.SNSEndpoint
		clientOpts = append(clientOpts, func(o *sns.Options) {
			o.BaseEndpoint = &ep
		})
	}
	return newPublisher(sns.NewFromConfig(awsCfg, clientOpts...), deleter, cfg, log), nil
}

// newPublisher wires a Publisher with an explicit client. Unexported because
// only New (real client) and the package's own tests need it.
func newPublisher(client snsAPI, deleter BatchDeleter, cfg appconfig.Config, log *zap.Logger) *Publisher {
	buf := cfg.SNSInputBuffer
	if buf < cfg.SNSBatchSize {
		buf = cfg.SNSBatchSize
	}
	return &Publisher{
		client:     client,
		deleter:    deleter,
		topicARN:   cfg.SNSTopicARN,
		batchSize:  cfg.SNSBatchSize,
		linger:     cfg.SNSLinger,
		publishers: cfg.SNSPublishers,
		input:      make(chan entry, buf),
		batches:    make(chan []entry, cfg.SNSPublishers*2),
		log:        log,
	}
}

// Start spawns 1 aggregator + N publisher goroutines. Non-blocking.
func (p *Publisher) Start(ctx context.Context) {
	p.aggWG.Add(1)
	go p.aggregate(ctx)

	for i := 0; i < p.publishers; i++ {
		p.publishWG.Add(1)
		go p.publishLoop(ctx, i)
	}
}

// Enqueue hands a message to the aggregator. Safe for concurrent callers.
// Blocks only if input buffer is full; otherwise O(1).
func (p *Publisher) Enqueue(body, receipt string) {
	e := entry{
		body:          body,
		receipt:       receipt,
		entryID:       uuid.NewString(),
		correlationID: uuid.NewString(),
	}
	p.input <- e
	metrics.SNSPendingBuffer.Set(float64(len(p.input)))
}

// Shutdown closes the input channel (aggregator drains + flushes), then waits
// for publishers to finish processing queued batches.
func (p *Publisher) Shutdown() {
	p.once.Do(func() {
		close(p.input)
	})
	p.aggWG.Wait()
	close(p.batches)
	p.publishWG.Wait()
}

func (p *Publisher) aggregate(ctx context.Context) {
	defer p.aggWG.Done()

	buf := make([]entry, 0, p.batchSize)
	timer := time.NewTimer(p.linger)
	timer.Stop()
	timerArmed := false

	flush := func() {
		if len(buf) == 0 {
			return
		}
		batch := make([]entry, len(buf))
		copy(batch, buf)
		buf = buf[:0]
		p.batches <- batch
		if timerArmed {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timerArmed = false
		}
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			// Drain any late arrivals before exit.
			for e := range p.input {
				buf = append(buf, e)
				if len(buf) >= p.batchSize {
					flush()
				}
			}
			flush()
			return
		case e, ok := <-p.input:
			if !ok {
				flush()
				return
			}
			buf = append(buf, e)
			if len(buf) >= p.batchSize {
				flush()
				continue
			}
			if !timerArmed {
				timer.Reset(p.linger)
				timerArmed = true
			}
		case <-timer.C:
			timerArmed = false
			flush()
		}
	}
}

func (p *Publisher) publishLoop(ctx context.Context, id int) {
	defer p.publishWG.Done()
	log := p.log.With(zap.Int("publisher_id", id))
	for batch := range p.batches {
		p.publishBatch(ctx, log, batch)
	}
}

func (p *Publisher) publishBatch(ctx context.Context, log *zap.Logger, batch []entry) {
	entries := make([]types.PublishBatchRequestEntry, len(batch))
	byID := make(map[string]entry, len(batch))
	for i := range batch {
		e := batch[i]
		id := strconv.Itoa(i)
		body := e.body
		corr := e.correlationID
		entries[i] = types.PublishBatchRequestEntry{
			Id:      &id,
			Message: &body,
			MessageAttributes: map[string]types.MessageAttributeValue{
				"correlation-id": {
					DataType:    aws.String("String"),
					StringValue: &corr,
				},
			},
		}
		byID[id] = e
	}

	start := time.Now()
	out, err := p.client.PublishBatch(ctx, &sns.PublishBatchInput{
		TopicArn:                   &p.topicARN,
		PublishBatchRequestEntries: entries,
	})
	metrics.SNSPublishBatchDuration.Observe(time.Since(start).Seconds())

	if err != nil {
		metrics.SNSPublishTotal.WithLabelValues("failure").Add(float64(len(batch)))
		log.Error("sns publish batch failed, messages will be redelivered",
			zap.Error(err), zap.Int("batch_size", len(batch)))
		return
	}

	receipts := make([]string, 0, len(out.Successful))
	for _, s := range out.Successful {
		if s.Id == nil {
			continue
		}
		e, ok := byID[*s.Id]
		if !ok {
			continue
		}
		receipts = append(receipts, e.receipt)
	}

	failed := 0
	for _, f := range out.Failed {
		failed++
		if f.Id == nil {
			continue
		}
		e, ok := byID[*f.Id]
		if !ok {
			continue
		}
		code := ""
		if f.Code != nil {
			code = *f.Code
		}
		msg := ""
		if f.Message != nil {
			msg = *f.Message
		}
		log.Warn("sns publish entry failed",
			zap.String("correlation_id", e.correlationID),
			zap.String("code", code),
			zap.String("message", msg),
		)
	}

	metrics.SNSPublishTotal.WithLabelValues("success").Add(float64(len(receipts)))
	metrics.SNSPublishTotal.WithLabelValues("failure").Add(float64(failed))

	if len(receipts) > 0 {
		if failedReceipts, delErr := p.deleter.DeleteBatch(ctx, receipts); delErr != nil {
			log.Error("sqs delete batch failed after sns publish",
				zap.Error(delErr), zap.Int("count", len(receipts)))
		} else if len(failedReceipts) > 0 {
			log.Warn("sqs delete batch partial failure",
				zap.Int("failed", len(failedReceipts)),
				zap.Int("total", len(receipts)))
		}
	}

	metrics.SNSPendingBuffer.Set(float64(len(p.input)))
}
