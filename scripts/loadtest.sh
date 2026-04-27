#!/bin/bash
# End-to-end load test:
#   1. garante queues vazias
#   2. seed 500k mensagens
#   3. sobe 2 workers + prom + grafana
#   4. espera drenar e reporta TPS
#
# Usage: TOTAL=500000 ./scripts/loadtest.sh

set -e

TOTAL="${TOTAL:-500000}"
WORKERS="${WORKERS:-worker-1 worker-2}"
cd "$(dirname "$0")/.."

echo "=== 1/6 Ensure localstack up ==="
docker compose up -d localstack
./scripts/setup.sh >/dev/null 2>&1 || true

# derive URLs
QUEUE_URL_HOST=$(grep '^SQS_QUEUE_URL=' .env | cut -d'=' -f2-)
echo "Queue: $QUEUE_URL_HOST"

echo ""
echo "=== 2/6 Purge queue (clean slate) ==="
docker compose exec -T localstack awslocal sqs purge-queue --queue-url "$(echo "$QUEUE_URL_HOST" | sed 's#http://localhost:4566#http://localhost:4566#')" >/dev/null 2>&1 || true
sleep 2

echo ""
echo "=== 3/6 Build worker image ==="
docker compose build worker-1

echo ""
echo "=== 4/6 Seed ${TOTAL} messages ==="
SEED_START=$(date +%s)
go run ./cmd/seed -total "$TOTAL" -workers 64
SEED_END=$(date +%s)
SEED_ELAPSED=$((SEED_END - SEED_START))
echo "Seed elapsed: ${SEED_ELAPSED}s"

echo ""
echo "=== Queue depth check ==="
docker compose exec -T localstack awslocal sqs get-queue-attributes \
  --queue-url "$QUEUE_URL_HOST" \
  --attribute-names ApproximateNumberOfMessages

echo ""
echo "=== 5/6 Starting workers ($WORKERS) + Prometheus + Grafana ==="
T0=$(date +%s)
docker compose up -d --force-recreate --no-deps $WORKERS
docker compose up -d prometheus grafana

echo ""
echo "=== 6/6 Monitoring drain (Ctrl-C to abort; stats every 5s) ==="
echo "Grafana:    http://localhost:3000  (anonymous viewer enabled)"
echo "Prometheus: http://localhost:9090"
echo ""

LAST=-1
STABLE_SAMPLES=0
while true; do
  REMAINING=$(docker compose exec -T localstack awslocal sqs get-queue-attributes \
    --queue-url "$QUEUE_URL_HOST" \
    --attribute-names ApproximateNumberOfMessages 2>/dev/null \
    | grep -o '"ApproximateNumberOfMessages": "[0-9]*"' | grep -o '[0-9]*' || echo "?")
  NOW=$(date +%s)
  ELAPSED=$((NOW - T0))
  CONSUMED=$((TOTAL - REMAINING))
  if [ "$ELAPSED" -gt 0 ]; then
    TPS=$((CONSUMED / ELAPSED))
  else
    TPS=0
  fi
  echo "[$(date +%H:%M:%S)] elapsed=${ELAPSED}s remaining=${REMAINING} consumed≈${CONSUMED} avg_tps=${TPS}"
  if [ "$REMAINING" = "0" ]; then
    STABLE_SAMPLES=$((STABLE_SAMPLES + 1))
    if [ "$STABLE_SAMPLES" -ge 2 ]; then
      break
    fi
  else
    STABLE_SAMPLES=0
  fi
  if [ "$ELAPSED" -gt 1800 ]; then
    echo "Timeout at 30min, breaking"
    break
  fi
  LAST="$REMAINING"
  sleep 3
done

T1=$(date +%s)
TOTAL_ELAPSED=$((T1 - T0))

echo ""
echo "========================================"
echo " LOAD TEST COMPLETE"
echo "========================================"
echo " messages:     $TOTAL"
echo " workers:      $(echo $WORKERS | wc -w | tr -d ' ') ($WORKERS)"
echo " total_time:   ${TOTAL_ELAPSED}s"
if [ "$TOTAL_ELAPSED" -gt 0 ]; then
  echo " avg_tps:      $(awk -v t="$TOTAL" -v e="$TOTAL_ELAPSED" 'BEGIN{printf "%.2f", t/e}') msg/s"
fi

WINDOW_SECS=$((TOTAL_ELAPSED + 10))
PEAK_QUERY="max_over_time(sum(rate(sqs_messages_consumed_total{status=\"success\"}[15s]))[${WINDOW_SECS}s:5s] @ ${T1})"
PEAK=$(curl -sG http://localhost:9090/api/v1/query \
  --data-urlencode "query=${PEAK_QUERY}" 2>/dev/null \
  | grep -o '"value":\[[^]]*\]' \
  | grep -oE '"[0-9.]+([eE][+-]?[0-9]+)?"' \
  | tail -1 | tr -d '"' || echo "")
if [ -n "$PEAK" ]; then
  echo " peak_tps:     $(awk -v p="$PEAK" 'BEGIN{printf "%.2f", p}') msg/s"
else
  echo " peak_tps:     (Prometheus unreachable — query manually below)"
fi

echo ""
echo " Peak TPS (Prometheus query):"
echo "   ${PEAK_QUERY}"
echo " Open: http://localhost:9090/graph"
echo " Dashboard: http://localhost:3000/d/sqs-worker-load"
echo "========================================"
