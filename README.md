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

## Configuração de produção

### Variáveis de ambiente relevantes no ECS

| Var | Valor em prod | Observação |
|---|---|---|
| `LOG_LEVEL` | `warn` | Silencia o log por mensagem. Warn/Error ficam. Use `debug` só pra troubleshoot. |
| `WORKERS` | `20` | Afinado pro 0.25 vCPU do Fargate. Mais workers brigam por CPU. |
| `POLLERS` | `3` | |
| `VISIBILITY_TIMEOUT_SECS` | `60` | |
| `WAIT_TIME_SECS` | `20` | Long-poll padrão do SQS. |

Nada de `GOMAXPROCS` — o import `_ "go.uber.org/automaxprocs"` em `cmd/worker/main.go` detecta o limite do cgroup do Fargate em runtime.

## Deploy em produção (ECS Fargate ARM64)

A infra (SQS + DLQ + task def + service + roles OIDC) é provisionada via Terraform no repo privado **`lrochadev/infra-quest-starter`**, módulo `worker-planet-sqs/`. Nada de conta AWS, ARN ou queue URL vive nesse repo público.

### Pipeline (`.github/workflows/deploy.yml`)

Gatilho: **push na `main`**. A cada deploy o workflow:

1. Build multi-stage ARM64 e push no ECR compartilhado com tag `v{YYYYMMDD}-{sha7}`.
2. Checkout do `infra-quest-starter` (via PAT), `sed` no `prod.tfvars` trocando `image_tag`, commit+push com autor `github-actions[bot]`.
3. `terraform apply` no módulo → nova revisão da task definition + rollout do serviço.
4. `aws ecs wait services-stable` — job falha se o rollout não estabilizar.

Rollback = `git revert` no commit de bump + re-push.

### Secrets que o repo precisa (configurar uma vez)

Após o `terraform apply` inicial do módulo, capture os outputs e:

```bash
TF_DIR=/path/to/infra-quest-starter/worker-planet-sqs
cd $TF_DIR
gh secret set AWS_ECR_PUSH_ROLE_ARN -b "$(terraform output -raw github_actions_ecr_push_role_arn)"
gh secret set AWS_DEPLOY_ROLE_ARN   -b "$(terraform output -raw github_actions_deploy_role_arn)"
gh secret set ECR_REGISTRY          -b "$(terraform output -raw ecr_registry)"
gh secret set ECR_REPOSITORY        -b "$(terraform output -raw ecr_repository)"
gh secret set INFRA_REPO_PAT        -b "<PAT com write em infra-quest-starter>"
```

### Load test em produção (200k mensagens)

```bash
AWS_PROFILE=prod TOTAL=200000 ./scripts/seed-prod.sh
```

O seed roda do laptop via cadeia de credenciais AWS padrão (SQS é endpoint público autenticado por IAM — sem tunnel SSM/bastion necessário). Depois é só acompanhar o drain no CloudWatch SQS ou Container Insights do ECS.
