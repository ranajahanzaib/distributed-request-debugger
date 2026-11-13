# Troubleshooting

- make up: check Docker daemon availability and docker compose ps.
- No trace in Tempo: check collector and service logs; OTLP export is asynchronous.
- Checkout fails: wait for Postgres health and all service containers to start.
- Lock demo does not show a wait: verify inventory observer logs and ensure no prior SKU-001 request exhausted stock.
