#!/usr/bin/env bash
set -euo pipefail
curl --fail --silent --show-error http://127.0.0.1:8080/healthz >/dev/null
curl --fail --silent --show-error http://127.0.0.1:8081/api/health >/dev/null
key="smoke-$(date +%s)-$$"
response=$(curl --fail --silent --show-error -X POST http://127.0.0.1:8080/api/checkout -H 'content-type: application/json' -d "{\"customer_id\":\"customer-demo\",\"sku\":\"SKU-001\",\"quantity\":1,\"idempotency_key\":\"$key\"}")
printf '%s\n' "$response"
trace_id=$(printf '%s' "$response" | sed -n 's/.*"trace_id":"\([^"]*\)".*/\1/p')
if [[ ${#trace_id} -ne 32 ]]; then echo "missing trace ID in checkout response" >&2; exit 1; fi
