package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	appconfig "go-sqs-worker-backoff/internal/config"
)

type fakeConsumer struct {
	mu       sync.Mutex
	batches  [][]types.Message
	received atomic.Int32
	deletes  []string
	changes  []int32
	cvErr    error
}

func (f *fakeConsumer) pushBatch(msgs []types.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, msgs)
}

func (f *fakeConsumer) Receive(ctx context.Context) ([]types.Message, error) {
	for {
		f.mu.Lock()
		if len(f.batches) > 0 {
			batch := f.batches[0]
			f.batches = f.batches[1:]
			f.mu.Unlock()
			f.received.Add(1)
			return batch, nil
		}
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (f *fakeConsumer) Delete(_ context.Context, receipt *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, *receipt)
	return nil
}

func (f *fakeConsumer) ChangeVisibility(_ context.Context, _ *string, seconds int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, seconds)
	return f.cvErr
}

type fakeHandler struct {
	fn func(types.Message) error
}

func (h *fakeHandler) Handle(_ context.Context, msg types.Message) error { return h.fn(msg) }

type capturePublisher struct {
	mu      sync.Mutex
	entries []struct{ body, receipt string }
}

func (p *capturePublisher) Enqueue(body, receipt string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = append(p.entries, struct{ body, receipt string }{body, receipt})
}

func (p *capturePublisher) snapshot() []struct{ body, receipt string } {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]struct{ body, receipt string }, len(p.entries))
	copy(cp, p.entries)
	return cp
}

func baseCfg() appconfig.Config {
	return appconfig.Config{
		Workers:     2,
		Pollers:     1,
		MaxAttempts: 3,
		BaseBackoff: 1 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	}
}

func msg(body, receipt string, attempt int) types.Message {
	return types.Message{
		Body:          &body,
		ReceiptHandle: &receipt,
		MessageId:     aws.String(body + "-id"),
		Attributes: map[string]string{
			string(types.MessageSystemAttributeNameApproximateReceiveCount): itoa(attempt),
		},
	}
}

func itoa(n int) string {
	if n <= 0 {
		return "1"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestPool_Success_EnqueuesPublisherNoDelete(t *testing.T) {
	c := &fakeConsumer{}
	c.pushBatch([]types.Message{msg("planet-a", "r1", 1)})
	pub := &capturePublisher{}
	h := &fakeHandler{fn: func(types.Message) error { return nil }}

	ctx, cancel := context.WithCancel(context.Background())
	pool := New(baseCfg(), c, h, pub, zap.NewNop())
	done := make(chan struct{})
	go func() { pool.Start(ctx); close(done) }()

	require.Eventually(t, func() bool { return len(pub.snapshot()) == 1 }, time.Second, 5*time.Millisecond)
	cancel()
	<-done

	entries := pub.snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, "planet-a", entries[0].body)
	assert.Equal(t, "r1", entries[0].receipt)
	assert.Empty(t, c.deletes, "success path must not delete directly; publisher is responsible")
}

func TestPool_HandlerError_TriggersChangeVisibility(t *testing.T) {
	c := &fakeConsumer{}
	c.pushBatch([]types.Message{msg("planet-b", "r2", 1)})
	pub := &capturePublisher{}
	h := &fakeHandler{fn: func(types.Message) error { return errors.New("fail") }}

	ctx, cancel := context.WithCancel(context.Background())
	pool := New(baseCfg(), c, h, pub, zap.NewNop())
	done := make(chan struct{})
	go func() { pool.Start(ctx); close(done) }()

	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.changes) == 1
	}, time.Second, 5*time.Millisecond)
	cancel()
	<-done

	assert.Empty(t, pub.snapshot())
	assert.Empty(t, c.deletes)
}

func TestPool_MaxAttemptsReached_NoChangeNoPublish(t *testing.T) {
	c := &fakeConsumer{}
	c.pushBatch([]types.Message{msg("planet-c", "r3", 3)}) // attempt==MaxAttempts
	pub := &capturePublisher{}
	h := &fakeHandler{fn: func(types.Message) error { return errors.New("fail") }}

	ctx, cancel := context.WithCancel(context.Background())
	pool := New(baseCfg(), c, h, pub, zap.NewNop())
	done := make(chan struct{})
	go func() { pool.Start(ctx); close(done) }()

	// Wait until poller has drained the batch at least once.
	require.Eventually(t, func() bool { return c.received.Load() >= 1 }, time.Second, 5*time.Millisecond)
	// Give worker a moment to process the failure.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	assert.Empty(t, pub.snapshot())
	assert.Empty(t, c.changes, "at max attempts, no visibility change — let DLQ handle it")
}

func TestBackoffDelay_Monotonic(t *testing.T) {
	d1 := backoffDelay(time.Second, time.Minute, 1)
	d5 := backoffDelay(time.Second, time.Minute, 5)
	assert.LessOrEqual(t, d1, d5, "later attempts back off at least as long (jittered)")
	assert.LessOrEqual(t, d5, time.Minute)
}
