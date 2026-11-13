# Demo scenarios

Compose enables the allowlisted scenario controls only in the local debugger and service containers. The payment delay and trace propagation fault are selected per request from validated idempotency-key prefixes; unrelated requests do not inherit a global fault switch.

| Scenario              | Trigger                   | Expected evidence                                                                                                                                                                    |
| --------------------- | ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `healthy`             | `make demo-healthy` or UI | Normal checkout across gateway, auth, orders, inventory, and payment.                                                                                                                |
| `inventory-db-lock`   | `make demo-lock` or UI    | Runner locks `inventory.stock`, observes the inventory update blocked with a PostgreSQL lock event and blocker PID, then holds for `LOCK_HOLD_DURATION` (default 7.5s) and releases. |
| `slow-payment`        | `make demo-payment` or UI | Payment simulator delays its response; no database-lock finding should appear.                                                                                                       |
| `upstream-timeout`    | `make demo-timeout` or UI | Payment exceeds the gateway deadline; checkout returns a timeout response and contexts are canceled.                                                                                 |
| `missing-propagation` | UI or debugger CLI        | Demo-only auth call uses an uninstrumented transport, producing a separate trace/gap that the analyzer reports as incomplete.                                                        |

The lock runner confirms the wait before starting the configured hold period. It uses bounded polling and defers rollback until commit succeeds, including error and cancellation paths. The observer samples activity every 40 ms, so duration evidence is approximate. Scenario execution returns checkout data and a diagnosis when the trace has reached Tempo; if ingestion is still pending, it returns a warning instead of claiming a diagnosis succeeded.
