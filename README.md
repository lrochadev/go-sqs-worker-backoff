# go-sqs-worker-backoff

A Go SQS consumer service using LocalStack for local development and testing.

## Overview

**Version:** 0.2.0

This project implements an SQS message consumer that:
- Consumes messages from `planet-queue` (long polling, 10 msgs/request)
- Parses JSON payloads (planet data)
- Logs structured message data
- Republishes each message body to the `planet-topic` SNS topic via `PublishBatch` with a linger+batch aggregator (1 aggregator goroutine + N publishers)
- Attaches a random `correlation-id` as an SNS `MessageAttribute` on every entry
- Deletes SQS messages only after SNS confirms the publish — **at-least-once end-to-end** (on SNS failure, the message is redelivered via visibility timeout and retried with exponential backoff)
- Uses `DeleteMessageBatch` to ack up to 10 receipts per round-trip, closing the batch symmetry (10 receive → 10 publish → 10 delete)
- Gracefully flushes pending entries on shutdown

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
- `SNS_TOPIC_ARN` — full ARN of the SNS topic to republish to

**Optional:**
- `SQS_ENDPOINT` / `SNS_ENDPOINT` — set only for LocalStack or a custom endpoint; leave empty in prod so the SDK hits real AWS. If `SNS_ENDPOINT` is unset, it falls back to `SQS_ENDPOINT`.
- `WORKERS`, `POLLERS`, `MAX_ATTEMPTS`, `BASE_BACKOFF`, `MAX_BACKOFF`, `VISIBILITY_TIMEOUT_SECS`, `WAIT_TIME_SECS`, `SHUTDOWN_TIMEOUT` — tuning knobs (see `.env.example` for defaults)
- `SNS_BATCH_SIZE` (default 10, clamped to AWS max 10), `SNS_LINGER` (default `150ms`), `SNS_PUBLISHERS` (default 8 goroutines), `SNS_INPUT_BUFFER` (default `WORKERS*4`)

### SNS publishing design

Instead of Java's `ExecutorService + BlockingQueue + 512-thread pool`, the Go version uses **1 aggregator goroutine + N publisher goroutines + a buffered channel**. The aggregator flushes whenever the batch hits `SNS_BATCH_SIZE` or the linger timer fires. Publishers call `sns:PublishBatch`; on success, receipts are batched through `sqs:DeleteMessageBatch` (1 round-trip). On failure, no delete happens — the SQS message becomes visible again and falls into the existing retry/backoff.

**Rough throughput (batch=10, linger=150ms, publishers=8):** ~1,000–1,600 msgs/s sustained, limited mostly by SNS publish latency (~40–80ms/call). Scale `SNS_PUBLISHERS` if you need more.

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

### 4. Run tests

```bash
go test ./... -race -cover
```

Current coverage: `config` 93%, `worker` 90%, `sns` 76%, `sqs` 66%.

### 5. Stop LocalStack

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

## Metrics (Prometheus)

Each worker exposes `/metrics` on port `8080`:

- `sqs_messages_consumed_total{status="success|failure|delete_error"}` — counter
- `sqs_messages_retried_total` — counter
- `sqs_message_processing_duration_seconds{status=...}` — histogram
- `sqs_messages_in_flight` — gauge
- Default `go_*` / `process_*` runtime metrics

Port mapping: `worker-1` → `http://localhost:8081/metrics`, `worker-2` → `http://localhost:8082/metrics`.

In production (ECS), the same endpoint is scraped by the Amazon Managed Prometheus agent, CloudWatch Agent, or an ADOT collector sidecar — no code changes.

## Load test (2 containers × 500k messages)

```bash
./scripts/setup.sh          # localstack + queues + .env
./scripts/loadtest.sh        # seed 500k, start 2 workers, drain, report TPS
```

Knobs (env vars):

- `TOTAL` — number of messages to seed (default `500000`).
- `WORKERS` — space-separated list of compose services to start (default `"worker-1 worker-2"`). To run with a single container: `WORKERS=worker-1 ./scripts/loadtest.sh`. Make sure the listed services exist in `docker-compose.yml` (e.g. `worker-2` is commented out by default).

Open dashboards:
- Grafana: http://localhost:3000 (anonymous viewer; admin/admin for edit)
- Prometheus: http://localhost:9090

Result on this machine (Apple Silicon, Docker Desktop, LocalStack 3.5.0):

| Metric | Value |
|---|---|
| Producer rate (seed, 64 goroutines) | ~11,300 msg/s |
| Total consumed | 500,000 |
| Drain time (2 workers) | ~383 s |
| Avg TPS | ~1,305 msg/s |
| Peak TPS (30s window) | ~1,380 msg/s |
| Peak TPS (10s window) | ~1,440 msg/s |
| p99 handler duration | ~0.5 ms |
| worker-1 / worker-2 split | 249,620 / 250,380 |

**Ceiling é do LocalStack, não do worker.** O handler leva ~0.5ms p99 — cada container tem capacidade ociosa vasta. O gargalo real é o broker SQS single-process do LocalStack, que serializa acesso à fila. Rodando em AWS SQS real (ou aumentando réplicas do worker com outro broker), o TPS sobe uma ordem de grandeza ou mais, limitado por CPU/rede do container. Em ECS, esse mesmo binário + dashboard Prometheus funciona sem alterações.

