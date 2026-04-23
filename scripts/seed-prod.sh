#!/bin/bash
# Publica mensagens na fila planet-queue em PRODUÇÃO (AWS real, não LocalStack).
#
# Pré-requisitos:
#   - AWS CLI configurado com o profile `prod` (o mesmo usado no Terraform).
#   - Rodar do seu laptop — SQS é endpoint HTTPS público autenticado por IAM SigV4.
#
# Uso:
#   ./scripts/seed-prod.sh                 # 200k msgs (default)
#   TOTAL=50000 ./scripts/seed-prod.sh     # qualquer quantidade
#   AWS_PROFILE=other ./scripts/seed-prod.sh
#
# Depois de enfileirar:
#   - acompanhe o drain no CloudWatch (SQS > planet-queue > ApproximateNumberOfMessagesVisible — nome da métrica CloudWatch)
#   - ou via CLI:   watch -n 5 'aws sqs get-queue-attributes --queue-url "$SQS_QUEUE_URL" --attribute-names ApproximateNumberOfMessages'
#     (no GetQueueAttributes o atributo é ApproximateNumberOfMessages, sem "Visible" — diferente do nome da métrica CloudWatch)

set -euo pipefail

: "${AWS_PROFILE:=prod}"
: "${AWS_REGION:=us-east-2}"
: "${TOTAL:=200000}"
: "${SEED_WORKERS:=64}"

export AWS_PROFILE AWS_REGION
# O binário só carrega .env quando APP_ENV=local. Este script não seta APP_ENV,
# então o .env de desenvolvimento (LocalStack) é ignorado — SDK usa AWS default.

SQS_QUEUE_URL=$(aws sqs get-queue-url --queue-name planet-queue --query QueueUrl --output text)
export SQS_QUEUE_URL

echo "Profile:    $AWS_PROFILE"
echo "Region:     $AWS_REGION"
echo "Queue URL:  $SQS_QUEUE_URL"
echo "Total:      $TOTAL"
echo "Workers:    $SEED_WORKERS"
echo

cd "$(dirname "$0")/.."
go run ./cmd/seed -total "$TOTAL" -workers "$SEED_WORKERS"

echo
echo "Depth check:"
aws sqs get-queue-attributes \
  --queue-url "$SQS_QUEUE_URL" \
  --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible
