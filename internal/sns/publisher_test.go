package sns

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	appconfig "go-sqs-worker-backoff/internal/config"
)

type fakeSNS struct {
	mu      sync.Mutex
	calls   []*sns.PublishBatchInput
	fail    map[string]string // entryID -> error code (partial failure)
	returns error             // whole call error
}

func (f *fakeSNS) PublishBatch(_ context.Context, in *sns.PublishBatchInput, _ ...func(*sns.Options)) (*sns.PublishBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.returns != nil {
		return nil, f.returns
	}
	out := &sns.PublishBatchOutput{}
	for _, e := range in.PublishBatchRequestEntries {
		id := *e.Id
		if code, bad := f.fail[id]; bad {
			out.Failed = append(out.Failed, types.BatchResultErrorEntry{
				Id: aws.String(id), Code: aws.String(code), Message: aws.String("forced"),
			})
		} else {
			out.Successful = append(out.Successful, types.PublishBatchResultEntry{
				Id: aws.String(id), MessageId: aws.String("msg-" + id),
			})
		}
	}
	return out, nil
}

func (f *fakeSNS) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSNS) lastCall() *sns.PublishBatchInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

type fakeDeleter struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (d *fakeDeleter) DeleteBatch(_ context.Context, receipts []string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]string, len(receipts))
	copy(cp, receipts)
	d.calls = append(d.calls, cp)
	return nil, d.err
}

func (d *fakeDeleter) allReceipts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var all []string
	for _, c := range d.calls {
		all = append(all, c...)
	}
	return all
}

func cfg(batch int, linger time.Duration, publishers int) appconfig.Config {
	return appconfig.Config{
		SNSTopicARN:    "arn:aws:sns:us-east-1:000000000000:t",
		SNSBatchSize:   batch,
		SNSLinger:      linger,
		SNSPublishers:  publishers,
		SNSInputBuffer: 64,
	}
}

func TestPublisher_FlushOnBatchFull(t *testing.T) {
	api := &fakeSNS{}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(10, time.Hour, 1), zap.NewNop())
	p.Start(context.Background())

	for i := 0; i < 10; i++ {
		p.Enqueue("b", "r")
	}

	require.Eventually(t, func() bool { return api.callCount() == 1 }, time.Second, 5*time.Millisecond)
	p.Shutdown()

	assert.Len(t, api.lastCall().PublishBatchRequestEntries, 10)
	assert.Len(t, del.allReceipts(), 10)
}

func TestPublisher_FlushOnLinger(t *testing.T) {
	api := &fakeSNS{}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(10, 40*time.Millisecond, 1), zap.NewNop())
	p.Start(context.Background())

	for i := 0; i < 3; i++ {
		p.Enqueue("b", "r")
	}

	require.Eventually(t, func() bool { return api.callCount() == 1 }, time.Second, 5*time.Millisecond)
	p.Shutdown()

	assert.Len(t, api.lastCall().PublishBatchRequestEntries, 3)
	assert.Len(t, del.allReceipts(), 3)
}

func TestPublisher_SplitsWhenExceedsBatch(t *testing.T) {
	api := &fakeSNS{}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(10, time.Hour, 2), zap.NewNop())
	p.Start(context.Background())

	for i := 0; i < 23; i++ {
		p.Enqueue("b", "r")
	}
	// 2 full batches fire immediately; the remaining 3 need Shutdown to flush.
	p.Shutdown()

	// Expect 3 calls: 10 + 10 + 3
	require.Equal(t, 3, api.callCount())

	seenSizes := map[int]int{}
	api.mu.Lock()
	for _, c := range api.calls {
		seenSizes[len(c.PublishBatchRequestEntries)]++
		assert.LessOrEqual(t, len(c.PublishBatchRequestEntries), 10, "publish batch must never exceed 10")
	}
	api.mu.Unlock()
	assert.Equal(t, 2, seenSizes[10])
	assert.Equal(t, 1, seenSizes[3])

	assert.Len(t, del.allReceipts(), 23)
}

func TestPublisher_PartialFailure_DeletesOnlySuccessful(t *testing.T) {
	api := &fakeSNS{fail: map[string]string{"1": "Throttled"}}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(3, time.Hour, 1), zap.NewNop())
	p.Start(context.Background())

	p.Enqueue("b0", "receipt-0")
	p.Enqueue("b1", "receipt-1") // will fail (entry id "1")
	p.Enqueue("b2", "receipt-2")

	require.Eventually(t, func() bool { return api.callCount() == 1 }, time.Second, 5*time.Millisecond)
	p.Shutdown()

	// Failed entry's receipt must NOT be in any delete call.
	receipts := del.allReceipts()
	assert.NotContains(t, receipts, "receipt-1")
	assert.Contains(t, receipts, "receipt-0")
	assert.Contains(t, receipts, "receipt-2")
	assert.Len(t, receipts, 2)
}

func TestPublisher_PublishBatchError_NoDelete(t *testing.T) {
	api := &fakeSNS{returns: errors.New("network down")}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(3, time.Hour, 1), zap.NewNop())
	p.Start(context.Background())

	for i := 0; i < 3; i++ {
		p.Enqueue("b", "r")
	}
	require.Eventually(t, func() bool { return api.callCount() == 1 }, time.Second, 5*time.Millisecond)
	p.Shutdown()

	assert.Empty(t, del.allReceipts(), "no deletes when SNS call fails — messages will be redelivered")
}

func TestPublisher_CorrelationIdAndBodyAsIs(t *testing.T) {
	api := &fakeSNS{}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(2, time.Hour, 1), zap.NewNop())
	p.Start(context.Background())

	p.Enqueue(`{"name":"tatooine"}`, "r0")
	p.Enqueue(`{"name":"naboo"}`, "r1")

	require.Eventually(t, func() bool { return api.callCount() == 1 }, time.Second, 5*time.Millisecond)
	p.Shutdown()

	entries := api.lastCall().PublishBatchRequestEntries
	require.Len(t, entries, 2)
	for _, e := range entries {
		attr, ok := e.MessageAttributes["correlation-id"]
		require.True(t, ok, "correlation-id must be set")
		assert.Equal(t, "String", *attr.DataType)
		assert.NotEmpty(t, *attr.StringValue)
	}
	bodies := []string{*entries[0].Message, *entries[1].Message}
	assert.Contains(t, bodies, `{"name":"tatooine"}`)
	assert.Contains(t, bodies, `{"name":"naboo"}`)
}

func TestPublisher_ShutdownFlushesPending(t *testing.T) {
	api := &fakeSNS{}
	del := &fakeDeleter{}
	p := newPublisher(api, del, cfg(10, time.Hour, 1), zap.NewNop())
	p.Start(context.Background())

	p.Enqueue("b", "r")
	p.Enqueue("b", "r")
	// No flush trigger, only shutdown.
	p.Shutdown()

	assert.Equal(t, 1, api.callCount())
	assert.Len(t, api.lastCall().PublishBatchRequestEntries, 2)
	assert.Len(t, del.allReceipts(), 2)
}
