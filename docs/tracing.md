# Tracing

Each service initializes an OpenTelemetry tracer provider with a batched OTLP gRPC exporter, service resource attributes, and W3C Trace Context plus baggage propagation. The local sampler is `AlwaysSample` so demo traces are reproducible. HTTP server and client boundaries use `otelhttp`; the gateway and downstream handlers pass request contexts into HTTP and PostgreSQL operations.

The inventory reservation has a manual span around the actual transaction operation. It includes safe database classification and bounded wait evidence; raw SQL values, customer data, and identifiers are not added as metric labels. The observer uses another pool connection, never the blocked reservation connection. Checkout does not query Tempo and does not depend on Tempo availability; asynchronous export happens after request spans are recorded.

Service logs are JSON `slog` records. Middleware adds trace and span IDs when the request has a valid recording context. The metrics middleware exports bounded service, route, and status dimensions; it does not label metrics with trace IDs.
