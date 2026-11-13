#!/usr/bin/env bash
set -euo pipefail
curl --fail-with-body --silent --show-error --max-time 55 \
  -X POST http://127.0.0.1:8081/api/demo/run \
  -H 'content-type: application/json' \
  -H 'origin: http://127.0.0.1:8081' \
  -d '{"scenario":"inventory-db-lock"}'
