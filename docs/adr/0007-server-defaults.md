# ADR-0007: Server defaults, caps and the footprint gate

Status: accepted 2026-10-30 while building M1. Builder's call, not the owner's;
reopen any of these with a reason.
Evidence: `docs/research/methodology-and-architecture.md` §5, the M1 footprint
runs in `bench/results/dyno-footprint.jsonl`.

## Decision

**Port 3966** by default ("DYNO" on a phone keypad), set with `DYNO_LISTEN`.
Compose uses host networking (ADR-0003), so the default must stay clear of
the ports homelab services usually take (80, 3000, 8080, 8443).

**A 4 MiB random payload.** The research asks for 4–16 MiB so the stream is
longer than any compression window on the path; brotli's default window is
4 MiB. The bench server used 1 MiB, so this costs about 3 MiB of idle RSS.

**Hard caps**, all constants in `server/main.go` and `server/internal/measure`:

| Cap | Value | Why |
|---|---|---|
| Open connections | 256 | A test uses 6 per client (5 load streams plus the probe), so about 40 clients at once. Past the cap, `Accept` waits and new connections queue in the kernel's queue instead of being refused. |
| Header timeout | 10 s | Closes slowloris connections. |
| Idle keep-alive | 60 s | |
| Stall timeout | 10 s per read or write | A test request is long by design, so there's no whole-request timeout. Instead each 256 KiB write and each 64 KiB read must finish within 10 s. |
| Bytes per request | 16 GiB each way | A ceiling on every request. The plan's `max_bytes` (256 MiB) is what clients use. |
| Metrics connections | 8 | Only on `DYNO_METRICS_LISTEN` (ADR-0017). A scraper or two, never a crowd. |
| Metrics request timeout | 10 s whole request, read and write | Only on `DYNO_METRICS_LISTEN`. A scrape is one small reply, so unlike the test endpoints it gets a whole-request timeout. |

**Upload replies say what the server saw:** `{"bytes", "elapsed_us"}` plus
`Server-Timing`. An upload that stalls or aborts gets an explicit 408 or 400:
a handler that returns without writing gets an empty 200 from net/http, which
would read as a finished upload.

**No CORS yet.** Measurement responses send `Timing-Allow-Origin: *`, which
only exposes timings. CORS arrives with multi-node, when there is a second
origin to allow, and after decisions-pending #9 (auth).

**`dyno -healthcheck`** pings the local server, because the image is
`FROM scratch` and has no curl for `HEALTHCHECK`.

**The footprint gate runs in two sizes.** `make check` runs the quick check
(idle RSS after 10 s, peak RSS at 32 streams, 1000 stalled connections) in
about 1.5 minutes. The soak budget is defined over 10 minutes after a 2-minute
warmup, so the quick check skips it rather than fit a slope to a short, noisy
run. `make bench` runs everything, including a 12-minute soak, idle RSS at
60 s, and CPU and ping at 1 Gbit/s. It also appends a row to
`bench/results/dyno-footprint.jsonl`. Every PR that touches `server/` pastes
the `make bench` table. Both sizes run with `DYNO_METRICS_LISTEN` set and
scrape `/metrics` during the load phases, so the budgets cover an operator
who runs Prometheus.

## Consequences

- Idle RSS is about 15 MiB against the 16 MiB budget before SQLite exists.
  M1 doesn't add SQLite (it's in M2), so ADR-0006's planned re-measurement
  with SQLite happens in M2, and the idle budget will likely need its one
  adjustment then.
- RSS stays near its high-water mark after a burst of stalled connections
  (20–22 MiB after 1000). That is the retention the benchmark found, not a
  leak; the soak shows no growth.
