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
  - a reply that isn't dyno's ends the run.
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
