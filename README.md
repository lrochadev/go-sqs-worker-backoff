# go-sqs-worker-backoff

A Go SQS consumer service using LocalStack for local development and testing.

## Overview

This project implements an SQS message consumer that:
- Consumes messages from an SQS queue
- Parses JSON payloads (planet data)
- Logs structured message data
- Gracefully handles message deletion and shutdown

## Prerequisites

- Go 1.22+
- Docker & Docker Compose
- curl (for health checks)
- macOS ARM64 compatible environment

## Quick Start

### 1. Setup LocalStack and Create Queues

```bash
./scripts/setup.sh
```

This script will:
- Start LocalStack 3.5.0 container
- Create `planet-queue` (main queue)
- Create `planet-dlq` (dead-letter queue)
- Publish a test message
- Verify the message exists in the queue

### 2. Configure environment variables

The app loads config from a `.env` file (or from process env vars in production). A committed `.env.example` documents every supported variable.

If you ran `./scripts/setup.sh`, a `.env` was generated automatically pointing at LocalStack — you can skip this step.

Otherwise, copy the template and fill in the required values:

```bash
cp .env.example .env
# then edit .env
```

**Required** variables (app fails fast if missing):
- `SQS_QUEUE_URL` — full queue URL
- `AWS_REGION` — e.g. `us-east-1`

**Optional:**
- `SQS_ENDPOINT` — set only for LocalStack or a custom endpoint; leave empty in prod so the SDK hits real AWS
- `WORKERS`, `POLLERS`, `MAX_ATTEMPTS`, `BASE_BACKOFF`, `MAX_BACKOFF`, `VISIBILITY_TIMEOUT_SECS`, `WAIT_TIME_SECS`, `SHUTDOWN_TIMEOUT` — tuning knobs (see `.env.example` for defaults)

> `.env` is gitignored. `.env.example` is committed.

### 3. Run the Consumer

```bash
go run ./cmd/worker
```

The consumer will:
- Connect to LocalStack SQS endpoint (`http://localhost:4566`)
- Poll `planet-queue` for messages
- Log each message payload as formatted JSON
- Delete successfully processed messages
- Gracefully shutdown on `Ctrl+C`

### 4. Stop LocalStack

```bash
docker compose down
```

## Project Structure

```
.
├── docker-compose.yml      # LocalStack SQS service
├── main.go                 # SQS consumer implementation
├── go.mod                  # Go module dependencies
├── go.sum                  # Go module checksums
├── scripts/
│   └── setup.sh            # Setup script for queues and test data
└── README.md               # This file
```

## Message Format

The consumer expects JSON payloads with the following structure:

```json
{
  "name": "Tatooine",
  "climate": "arid",
  "terrain": "desert"
}
```

## Configuration

### LocalStack
- **Endpoint**: `http://localhost:4566`
- **Region**: `us-east-1`
- **Service**: SQS only
- **Version**: 3.5.0 (last fully open-source version)

### Queues
- **planet-queue**: Main queue for planet messages
- **planet-dlq**: Dead-letter queue (redrive after 3 failed receives)

## Consumer Behavior

- **Poll Interval**: 20-second long-polling (WaitTimeSeconds)
- **Max Messages**: 10 per poll
- **Message Deletion**: Immediate after successful processing
- **Shutdown**: Graceful handling of SIGINT/SIGTERM

## Testing

### Manually Publish a Message

```bash
docker compose exec localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/planet-queue \
  --message-body '{"name":"Hoth","climate":"frozen","terrain":"ice"}'
```

### Peek at Queue (without deleting)

```bash
docker compose exec localstack awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/planet-queue \
  --attribute-names All
```

### Get Queue Attributes

```bash
docker compose exec localstack awslocal sqs get-queue-attributes \
  --queue-url http://localhost:4566/000000000000/planet-queue \
  --attribute-names All
```

## Logs

All processed messages are logged with:
- Timestamp
- Planet name, climate, and terrain
- Message ID (for audit trail)

Example:
```
2026/04/21 10:30:45 Received planet: {
  "name": "Tatooine",
  "climate": "arid",
  "terrain": "desert"
}
```

## Notes

- LocalStack 3.5.0 is used because newer versions restricted core features to paid tiers
- AWS SDK v2 is used for better performance and Go compatibility
- Platform is explicitly set to `linux/arm64` in docker-compose for macOS ARM64 compatibility
