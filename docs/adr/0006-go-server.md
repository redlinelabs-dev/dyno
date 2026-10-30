# ADR-0006: Go for the server

Status: accepted 2026-10-30 by the owner (was decisions-pending #3).
Supersedes ADR-0001. Evidence: `docs/research/runtime-benchmark.md`.

## Context

ADR-0001 picked Rust for the smallest footprint. The owner's answer: "if the
difference on a docker host is trivial, and go is easier to maintain, then I am
happy to use Go. I just don't want to be negligent about resource usage. I
don't need to optimize it to the 15th degree".

The measured difference on a Docker host is trivial:

| | Go net/http | Rust current-thread |
|---|---|---|
| Idle RSS | 10.8 MiB | 3.7 MiB |
| Peak RSS under load | 18.6 MiB | 19.1 MiB |
| CPU at 1 Gbit/s, down/up | 3.6% / 4.6% | 2.6% / 2.5% |
| Ping p99 at 1 Gbit/s | 117 µs | 73 µs |
| Soak slope | −5 KiB/min | −5 KiB/min |
| 1,000 stalled connections | closed | closed |
| Static arm64 binary with SQLite | 6.3 MiB | 1.9 MiB |

Go also does SQLite, TLS, embedding and arm64 cross-builds with nothing but its
own toolchain, where Rust needed `zig cc` for SQLite.

## Decision

- Go with the standard library's `net/http` and `http.ServeMux`, `embed` for
  the web assets, and `modernc.org/sqlite` (pure Go) for storage. Add a
  dependency only when std can't do the job.
- Built with `CGO_ENABLED=0` into one static binary, shipped `FROM scratch` as
  a non-root user, for amd64 and arm64.
- `bench/servers/go/main.go` is the starting point for the hot path: one
  random payload generated at start, upload read into pooled buffers, header
  and idle timeouts set.
- Set `GOMEMLIMIT` in the image (start at 64 MiB) so a burst can't leave the
  heap high. Change it only with measurements.

## Footprint budgets

Not negligent, not tuned to the last byte. Each has headroom over what Go
measured, and the footprint check fails the build when one is exceeded.

- Idle RSS ≤ 24 MiB with an empty database. This is the one adjustment,
  made in M2 with SQLite and the web UI in. It was 16 MiB, set from 10.8 MiB
  measured without SQLite. Measured by hand after 10 s idle: M1 was 15.2 MiB,
  M2 is 18.6 MiB, and the footprint check read 20.5 MiB. Anonymous memory
  didn't move (9.3 MiB in both). All of the growth
  is file-backed pages of the larger binary (6.95 → 11.34 MiB), which the
  kernel can reclaim. Don't raise this again; cut instead.
- Peak RSS ≤ 48 MiB at 32 concurrent streams.
- Soak slope ≤ +16 KiB/min over 10 minutes after a 2-minute warmup.
- Stalled connections are closed by timeout.

## Consequences

- **Latency on a CPU-bound server reads high.** With every server core busy,
  Go's ping p99 rose to 15 ms (benchmark finding 5). It doesn't happen at
  normal speeds: 1 Gbit/s used about 4% CPU, and p99 stayed at 117 µs. It could
  on a slow box at multi-gigabit. Handle it honestly instead of engineering it
  away: the server records its own CPU use during a test, and the ADR-0003
  self-check marks loaded latency as unreliable when the server was CPU-bound.
- Rerun `bench/` on a real arm64 board before the first release.
- Rust stays the fallback only if Go fails a budget and tuning can't fix it.
