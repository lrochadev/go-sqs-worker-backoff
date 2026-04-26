package sqs

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
)

// The Consumer is a thin wrapper over *sqs.Client; its behaviour (param plumbing,
// HTTP semantics) is owned by the AWS SDK. Mocking the SDK to assert "we passed
// the right field" just re-tests the SDK with low fidelity. End-to-end coverage
// comes from the LocalStack-backed dev workflow (scripts/setup.sh + cmd/seed).
//
// Only the pure helper below has unit-test value.

func TestAttempts(t *testing.T) {
	tests := []struct {
		val  string
		want int
	}{
		{"", 1}, {"1", 1}, {"5", 5}, {"garbage", 1},
	}
	for _, tc := range tests {
		m := types.Message{Attributes: map[string]string{
			string(types.MessageSystemAttributeNameApproximateReceiveCount): tc.val,
		}}
		assert.Equal(t, tc.want, Attempts(m), "val=%q", tc.val)
	}
}
