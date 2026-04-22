#!/bin/bash
# Cria as filas SQS no LocalStack e escreve um .env com as URLs.
# Não publica mensagens — o producer é ./cmd/seed.

set -e

echo "=== Starting LocalStack ==="
docker compose up -d localstack

echo "=== Waiting for LocalStack to be ready ==="
max_attempts=60
attempt=0
while [ $attempt -lt $max_attempts ]; do
    if curl -s http://localhost:4566/_localstack/health | grep -q '"sqs": "available"\|"sqs": "running"'; then
        echo "LocalStack is ready!"
        break
    fi
    attempt=$((attempt + 1))
    sleep 1
done

if [ $attempt -eq $max_attempts ]; then
    echo "ERROR: LocalStack did not become ready in time"
    exit 1
fi

echo ""
echo "=== Creating DLQ ==="
docker compose exec -T localstack awslocal sqs create-queue --queue-name planet-dlq > /dev/null
DLQ_URL=$(docker compose exec -T localstack awslocal sqs get-queue-url --queue-name planet-dlq | grep -o '"QueueUrl": "[^"]*' | cut -d'"' -f4)
echo "DLQ: $DLQ_URL"

echo ""
echo "=== Creating main queue ==="
docker compose exec -T localstack awslocal sqs create-queue \
  --queue-name planet-queue \
  --attributes '{"RedrivePolicy":"{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:000000000000:planet-dlq\",\"maxReceiveCount\":\"3\"}"}' \
  > /dev/null
QUEUE_URL=$(docker compose exec -T localstack awslocal sqs get-queue-url --queue-name planet-queue | grep -o '"QueueUrl": "[^"]*' | cut -d'"' -f4)
echo "Queue: $QUEUE_URL"

# Host-facing URL (containers use http://localstack:4566; host scripts use localhost:4566)
HOST_QUEUE_URL=$(echo "$QUEUE_URL" | sed 's#http://localstack:4566#http://localhost:4566#')

cat > .env <<EOF
SQS_QUEUE_URL=$HOST_QUEUE_URL
AWS_REGION=us-east-1
SQS_ENDPOINT=http://localhost:4566
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
EOF
echo "✓ .env written"
echo ""
echo "Queues ready. Now run: ./scripts/loadtest.sh"
