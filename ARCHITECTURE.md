# Architecture

The Compose deployment runs the gateway, auth, orders, inventory, payment, and debugger services alongside PostgreSQL and the OpenTelemetry Collector, Tempo, Prometheus, and Grafana. The checkout request travels through the gateway and downstream services; services export telemetry asynchronously through the Collector. The debugger queries Tempo separately, so trace lookup availability does not gate checkout requests.

Orders and inventory use separate PostgreSQL schemas and restricted roles. The lock demonstration holds a row lock from the scenario runner while the normal inventory reservation executes. A separate observer samples PostgreSQL activity to attach bounded wait evidence to the database span.

See the [README](README.md) for the request and telemetry diagrams, setup, and implementation limits. The [diagnosis model](docs/diagnosis.md) describes how timing and evidence are interpreted.
