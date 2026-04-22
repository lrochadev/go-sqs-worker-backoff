#!/bin/bash

set -e

echo "=== Starting LocalStack ==="
docker compose up -d

echo "=== Waiting for LocalStack to be ready ==="
max_attempts=30
attempt=0
while [ $attempt -lt $max_attempts ]; do
    if curl -s http://localhost:4566/_localstack/health | grep -q '"services"'; then
        echo "LocalStack is ready!"
        break
    fi
    attempt=$((attempt + 1))
    echo "Waiting... (attempt $attempt/$max_attempts)"
    sleep 1
done

if [ $attempt -eq $max_attempts ]; then
    echo "ERROR: LocalStack did not become ready in time"
    exit 1
fi

echo ""
echo "=== Creating DLQ ==="
DLQ_RESPONSE=$(docker compose exec -T localstack awslocal sqs create-queue --queue-name planet-dlq 2>/dev/null)
DLQ_URL=$(echo "$DLQ_RESPONSE" | grep -o '"QueueUrl": "[^"]*' | cut -d'"' -f4)
echo "DLQ created: $DLQ_URL"

echo ""
echo "=== Creating main queue with redrive policy ==="
QUEUE_RESPONSE=$(docker compose exec -T localstack awslocal sqs create-queue \
  --queue-name planet-queue \
  --attributes '{"RedrivePolicy":"{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:000000000000:planet-dlq\",\"maxReceiveCount\":\"3\"}"}' \
  2>/dev/null)
QUEUE_URL=$(echo "$QUEUE_RESPONSE" | grep -o '"QueueUrl": "[^"]*' | cut -d'"' -f4)
echo "Queue created: $QUEUE_URL"

echo ""
echo "=== Publishing test message ==="
MESSAGE_BODY='{"name":"Tatooine","climate":"arid","terrain":"desert"}'
docker compose exec -T localstack awslocal sqs send-message \
  --queue-url "$QUEUE_URL" \
  --message-body "$MESSAGE_BODY" > /dev/null

echo "Message published: $MESSAGE_BODY"

echo ""
echo "=== Verifying message in queue ==="
MESSAGES=$(docker compose exec -T localstack awslocal sqs receive-message --queue-url "$QUEUE_URL" 2>/dev/null)

if echo "$MESSAGES" | grep -q "Tatooine"; then
    echo "✓ Message successfully received and exists in queue!"
    echo ""
    echo "Message content:"
    echo "$MESSAGES" | grep -A 5 '"Body"' || true
else
    echo "✗ Message not found in queue"
    exit 1
fi

echo ""
echo "=== Setup Complete ==="
echo ""
echo "LocalStack is running on http://localhost:4566"
echo "Queue URL: $QUEUE_URL"
echo "DLQ URL: $DLQ_URL"
echo ""
echo "To start the consumer, run: go run main.go"
echo "To stop LocalStack, run: docker compose down"
