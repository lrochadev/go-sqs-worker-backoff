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
echo "=== Publishing 20 valid + 2 poison-pill messages ==="

VALID=(
  '{"name":"Tatooine","climate":"arid","terrain":"desert"}'
  '{"name":"Alderaan","climate":"temperate","terrain":"grasslands"}'
  '{"name":"Hoth","climate":"frozen","terrain":"tundra"}'
  '{"name":"Dagobah","climate":"murky","terrain":"swamp"}'
  '{"name":"Endor","climate":"temperate","terrain":"forest"}'
  '{"name":"Naboo","climate":"temperate","terrain":"grassy hills"}'
  '{"name":"Coruscant","climate":"temperate","terrain":"cityscape"}'
  '{"name":"Bespin","climate":"temperate","terrain":"gas giant"}'
  '{"name":"Kamino","climate":"stormy","terrain":"ocean"}'
  '{"name":"Geonosis","climate":"arid","terrain":"rock"}'
  '{"name":"Mustafar","climate":"hot","terrain":"volcanic"}'
  '{"name":"Kashyyyk","climate":"tropical","terrain":"forest"}'
  '{"name":"Utapau","climate":"arid","terrain":"sinkholes"}'
  '{"name":"Mygeeto","climate":"frigid","terrain":"crystalline"}'
  '{"name":"Felucia","climate":"hot","terrain":"fungi forest"}'
  '{"name":"Cato Neimoidia","climate":"temperate","terrain":"rock arches"}'
  '{"name":"Saleucami","climate":"hot","terrain":"caves"}'
  '{"name":"Jakku","climate":"arid","terrain":"desert"}'
  '{"name":"Crait","climate":"arid","terrain":"salt flats"}'
  '{"name":"Exegol","climate":"stormy","terrain":"ruins"}'
)

# Poison pills: o consumer vai falhar no parse/validação destas,
# fazendo elas entrarem em backoff e eventualmente DLQ (maxReceiveCount=3).
POISON=(
  'this-is-not-valid-json-at-all'
  '{"name":123,"climate":true,"terrain":null}'
)

for body in "${VALID[@]}"; do
  docker compose exec -T localstack awslocal sqs send-message \
    --queue-url "$QUEUE_URL" --message-body "$body" > /dev/null
done
echo "✓ Published ${#VALID[@]} valid messages"

for body in "${POISON[@]}"; do
  docker compose exec -T localstack awslocal sqs send-message \
    --queue-url "$QUEUE_URL" --message-body "$body" > /dev/null
done
echo "✓ Published ${#POISON[@]} poison-pill messages (should end up in DLQ after 3 retries)"

echo ""
echo "=== Verifying queue depth ==="
ATTRS=$(docker compose exec -T localstack awslocal sqs get-queue-attributes \
  --queue-url "$QUEUE_URL" --attribute-names ApproximateNumberOfMessages 2>/dev/null)
echo "$ATTRS"

echo ""
echo "=== Setup Complete ==="
echo ""
echo "LocalStack is running on http://localhost:4566"
echo "Queue URL: $QUEUE_URL"
echo "DLQ URL: $DLQ_URL"
echo ""
echo "To start the consumer, run: go run ./cmd/worker"
echo "To stop LocalStack, run: docker compose down"
