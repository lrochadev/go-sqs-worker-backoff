package sqs

import (
	"context"
	"strconv"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	appconfig "go-sqs-worker-backoff/internal/config"
)

type Consumer struct {
	client   *sqs.Client
	queueURL string
	cfg      appconfig.Config
}

func NewConsumer(ctx context.Context, cfg appconfig.Config) (*Consumer, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.Endpoint != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}

	var clientOpts []func(*sqs.Options)
	if cfg.Endpoint != "" {
		ep := cfg.Endpoint
		clientOpts = append(clientOpts, func(o *sqs.Options) {
			o.BaseEndpoint = &ep
		})
	}

	return &Consumer{
		client:   sqs.NewFromConfig(awsCfg, clientOpts...),
		queueURL: cfg.QueueURL,
		cfg:      cfg,
	}, nil
}

func (c *Consumer) Receive(ctx context.Context) ([]types.Message, error) {
	out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            &c.queueURL,
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     c.cfg.WaitTimeSeconds,
		VisibilityTimeout:   c.cfg.VisibilityTimeout,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameSentTimestamp,
		},
	})
	if err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Consumer) Delete(ctx context.Context, receipt *string) error {
	_, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &c.queueURL,
		ReceiptHandle: receipt,
	})
	return err
}

func (c *Consumer) ChangeVisibility(ctx context.Context, receipt *string, seconds int32) error {
	_, err := c.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          &c.queueURL,
		ReceiptHandle:     receipt,
		VisibilityTimeout: seconds,
	})
	return err
}

func Attempts(msg types.Message) int {
	v := msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
	n, _ := strconv.Atoi(v)
	if n < 1 {
		return 1
	}
	return n
}
