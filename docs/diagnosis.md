# Diagnosis model

The debugger fetches Tempo's OTLP JSON trace response and normalizes resources, spans, events, timestamps, and parent links into typed Go structures. It reports malformed IDs, invalid intervals, missing parents, and cycle warnings rather than assuming every trace is complete.

Duration comes from the measured root interval. Exclusive time is the span interval minus the union of its direct children's intervals clipped to that span. Overlapping children are counted once. Parent-child timing is useful evidence for a likely blocking chain, but does not prove causality for parallel or independently scheduled work.

Findings are evidence-backed. A database span with `db.wait.type=Lock` is confirmed as a lock wait; a long database operation without wait evidence is inferred. A slow client span identifies a slow downstream call, not network-only latency. Reports include supporting span IDs, durations, confidence, and investigation suggestions. Missing trace segments lower completeness and may yield insufficient evidence.

The PostgreSQL observer samples `pg_stat_activity` and `pg_blocking_pids()` from a separate connection every 40 ms while the reservation query executes. Its observed interval/count is approximate sampled evidence, not an exact wait duration. If the observer cannot obtain evidence, the analyzer does not claim a confirmed lock diagnosis.
