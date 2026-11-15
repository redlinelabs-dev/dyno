# ADR-0028: How scheduled tests run

Status: accepted 2026-11-13 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Fills in ADR-0019, which settled what scheduled
tests are for. How they look in the app is the owner's pick from the M3
prototypes.

## Decision

- **The server runs them itself**, in Go (package `nodetest`). It follows the
  same public plan any client follows (`docs/api.md`, "Running a test") against
  the far end, under the same rules (ADR-0003). Its arithmetic is
  `web/src/stats.ts` ported to Go (package `stats`), so a scheduled result and
  a browser's are worked out the same way.
- **The far end needs nothing but a dyno.** Pairing is adding a schedule with
  the far end's address. dyno checks that the address answers with a dyno plan
  before it saves the schedule. No token in v1.
- **Two sizes of test:**
  - full: the far end's plan as it is;
  - light, for frequent runs: at most 5 s per direction with a 1 s warm-up,
    and at most 100 MiB each way.
- **The path settles first:** 5 s of pings before idle latency, so a tailnet
  path has gone direct before anything is measured (ADR-0019).
- **A far end can't make a run unbounded or send it elsewhere:**
  - dyno clamps the far end's plan: at most 5 streams, at most 30 s per
    direction, and caps on sizes, samples and intervals;
  - it follows no redirects;
  - a reply that isn't dyno's ends the run, and so does a plan over 64 KiB
    (`not_dyno`): dyno's own plan is a few hundred bytes.
- **A failed run is stored**, with a code that points at an end:
  - `unreachable`, `timeout`, `not_dyno`, `http_status` or `stream_failed`;
  - and a plain sentence for the reason.

  A far end that can't be reached may be down, or this end's connection may be.
  The reason says what was seen, not a guess.
- **Results stay on the dyno that ran them**, with source `schedule` and the
  schedule they came from. Nothing is posted to the far end.
- **A path change is flagged, not counted as slow.** The far end's baseline is
  the median idle latency of its last 10 good results (it needs at least 3).
  A result is flagged when its idle latency is more than twice the baseline and
  at least 10 ms above it.
- **Schedules live in the app:** set in the UI or through the API, stored in
  SQLite, and applied at once, with no restart. They are not set in
  environment variables. Each setting has one home (`competitor-features.md`
  §6).
- **Limits:** at most 16 schedules, each at least 15 minutes apart, and one
  scheduled test running at a time. A schedule that comes due while another
  runs waits its turn.
- **Each finished run logs one `schedule.run` event** (ADR-0020) with
  `result`, `schedule_id`, `kind` and `duration_ms`:
  - a success is `succeeded`, at `INFO`;
  - a failure is `failed` with `error.code` and `error.message`: at `WARN`
    for a far-end code, which says something about the network, and at
    `ERROR` for `internal`, which is a bug;
  - a run stopped by a delete or a shutdown is `canceled`, at `INFO`, and
    stores nothing.

  Amended 2026-11-14, from building the scheduler:
  - a result the database won't store is `failed` with `store_write_failed`,
    at `ERROR`, and isn't counted in `/metrics`, so the metrics match the
    database;
  - a run that panics is stored as `internal`, and its event adds
    `error.stack`, cut to 4 KiB. An `internal` message is cut to 240 bytes
    of text without control characters, since a panic can quote anything
    and the message is shown in the UI;
  - a run that returns after its 5 minutes is a `timeout`, even a success,
    whose figures are dropped;
  - schedules that can't be read (at start, after a change, or for run now)
    log `schedule.load` at `ERROR` with `store_read_failed`. The scheduler
    tries again within 30 s, so an edit isn't lost silently.
- **The scheduler wakes at least once a minute**, even with nothing due.
  Go's timers don't count time the host spends suspended, so a laptop or VM
  that sleeps would otherwise run its schedules late by as long as it slept.
  A clock set back is noticed too: a next run more than one interval away is
  worked out again.

## Why

- Owning the engine is what lets dyno answer the trackers' top three requests
  (ADR-0019). Porting the browser's arithmetic, not re-deriving it, keeps the
  two kinds of result comparable on one chart.
- ADR-0008 leaves dyno without a login, so anyone who can reach it can add a
  schedule. The limits and the dyno-only far end keep a schedule from making
  dyno send gigabytes to an arbitrary address.

## Consequences

- `web/src/stats.ts` and `server/internal/stats` must change together. Their
  tests share inputs and expected values (case S1).
- A scheduled test uses this server's CPU and network while it runs. The
  footprint check gains a scheduled run (M3's done-when keeps the soak
  passing).
- One-box users still can't monitor their ISP without a second dyno
  (ADR-0019).
