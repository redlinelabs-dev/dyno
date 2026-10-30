# ADR-0001: Rust on a current-thread tokio runtime for the server

Status: superseded 2026-10-30 by ADR-0006 (Go). Kept for the record.
Evidence: `docs/research/runtime-benchmark.md`, raw data in `bench/results/`.

## Context

The brief asks for the smallest CPU and memory footprint possible, rock-solid
stability and no leaks. The server's hot path is streaming random bytes out,
draining bytes in, and answering pings while both run.

## Decision

The server is Rust: hyper 1.x (axum once there are more than a handful of
routes) on tokio's `current_thread` runtime, built as a static musl binary and
shipped `FROM scratch`.

## Measured (localhost, 3 reps, server on 4 cores)

| | Rust current-thread | Rust multi-thread | Go net/http | Zig std.http | Bun |
|---|---|---|---|---|---|
| Idle RSS after 60 s | 3.7 MiB | 4.0 MiB | 10.8 MiB | 3.7 MiB | 20.7 MiB |
| Peak RSS under load | 19.1 MiB | 27.4 MiB | 18.6 MiB | 21.4 MiB | 169 MiB |
| CPU at 1 Gbit/s, down/up | 2.6% / 2.5% | 3.2% / 5.1% | 3.6% / 4.6% | 3.5% / 3.7% | 3.8% / 8.7% |
| Ping p99 with 4 cores saturated | 65 µs | 17 ms | 15 ms | 4.2 ms | 65 µs |
| Soak slope after warmup | −5 KiB/min | +32 KiB/min | −5 KiB/min | +11 KiB/min | GC noise |
| 1,000 stalled connections | closed, 17 MiB | closed, 18 MiB | closed, 27 MiB | not closed, 584 MiB | closed, 21 MiB |
| Binary | 0.65 MiB | 0.65 MiB | 5.6 MiB | 0.27 MiB | 78 MiB |

Zig is out: `std.http` has no timeouts and no TLS server, and it held stalled
connections open. Bun's upload memory is the least predictable.

## Consequences

- Go was a close second and is easier to maintain. If Rust slows development
  badly, Go is the fallback, at roughly 3× the RSS.
- Bundled SQLite needs `zig cc` as the C cross-compiler for arm64 (checked:
  1.9 MiB static binary).
- If one Pi core can't fill 2.5 Gbit/s, switch to the multi-thread runtime with
  two workers. That's a one-line change.
- Rerun `bench/` on a real arm64 board before the first release. TLS was not
  measured; measure it if dyno terminates TLS itself.

## Footprint budgets

These fail the build once CI exists. Revisit them only with measurements.

- Idle RSS ≤ 8 MiB with an empty database.
- Peak RSS ≤ 40 MiB at 32 concurrent streams.
- Soak slope ≤ +16 KiB/min over 10 minutes after a 2-minute warmup.
- Stalled connections are closed by timeout.
