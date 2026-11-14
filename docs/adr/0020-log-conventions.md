# ADR-0020: Log conventions

Status: accepted 2026-10-31 while building M2.5. Builder's call, not the
owner's; reopen with a reason.

## Decision

dyno logs with `log/slog`: JSON lines on stdout, one object per event, for
Docker or journald to keep (ADR-0017). The JSON handler is the default
logger, so anything still printing through the `log` package (net/http's own
messages, a dependency) comes out as JSON too, with its text as `msg`.
net/http's messages are logged at `WARN`.

- **The message is a stable event name** in `domain.action` form:
  `server.start`, `server.stop`, `result.store`, `result.list`,
  `result.get`. Never a sentence and never interpolated. A new event gets a
  new name; a name doesn't change meaning.
- **Keys are flat snake_case with the unit in the name:**
  `download_bits_per_second`, `loaded_latency_ms`, `duration_ms`,
  `size_bytes`. Values are primitives: strings, numbers, booleans. No
  `slog.Group`, no structs, no maps. The one namespace is `error.`: the
  flat keys `error.code` and `error.message`.
- **`result` is `succeeded` or `failed`** on every outcome event, so a
  query can split on it without parsing anything else.
- **Levels:** `INFO` for outcomes that went as planned. `WARN` for expected
  or recoverable failures: a client's bad input, a shutdown that cut off a
  running test. `ERROR` only for what the operator must act on: a listener
  that can't bind, a database that refuses writes or reads. A failure carries
  `error.code` (a short, stable slug such as `invalid_json`,
  `store_write_failed`, `metrics_listen_failed`) and `error.message` (the Go
  error text).
- **One outcome event per stored result.** Every `POST /api/v1/results`
  ends in exactly one `result.store`, on success and on every failure path.
  It carries `duration_ms` (handler time), `status_code`, `source`, and,
  once the result has passed validation, `result_id`, `grade` (`none`
  without loaded latency), the measured figures
  (`download_bits_per_second`, `upload_bits_per_second`, `idle_latency_ms`,
  `loaded_latency_ms`, `jitter_ms`, `bufferbloat_ms`),
  `server_bytes_sent`, `server_bytes_received`, `client_limited` and
  `streams`. A rejected submission logs none of these: its figures are
  unvalidated and can be out of range.
  Speeds are in bits, as people quote them. `/metrics` exports the same
  speeds in bytes per second instead, because Prometheus base units are
  bytes and `promtool check metrics` flags bits.
- **Reads log only when they fail.** `result.list` and `result.get` appear
  when the database errors, not on every page load. A client's 400 or 404 on
  a read isn't logged.
- **`server.start`** carries `version`, `listen_address`,
  `metrics_listen_address` (empty when metrics are off) and `data_dir`; it
  is also the `ERROR` event when startup fails. `server.stop` carries
  `uptime_seconds`.
- **No client IP, user agent or client label in logs.** ADR-0017's spirit:
  the operator learns what tests ran, not who ran them. The `client` field
  of a result is left out because it is derived from the user agent. An
  operator who wants access logs gets them from their reverse proxy. The one
  exception is net/http's own panic message (`http: panic serving <addr>`),
  which includes the remote address; a panic is a bug to fix anyway.
- **Measurement endpoints don't log.** `/dl`, `/ul` and `/ping` run hundreds
  of times a test. Their totals are in `/metrics`.

## Why

Adapted from the wide-event and canonical-log-line style: Sentry's "structure
a log" (July 2026) and Stripe's canonical log lines. One rich event per unit
of work, with stable names and flat typed keys, is something `jq`, Loki or
any log store can filter and aggregate without regexes.

Unlike the Sentry post, outcome events carry `duration_ms`. That post leaves
timing to traces; dyno has no tracing, so the outcome event is the only place
to put it.

## Consequences

- Event names and keys are an interface: dashboards and alerts will match on
  them. Rename one only with a note in the PR description and a new ADR
  that supersedes this list.
- Tests assert the `result.store` line's keys (`server/internal/api`), and
  that `server.start` and `server.stop` are the only lines of a clean run.
- Free-text logging (`slog.Info("storing the result for " + id)`) is a
  review failure.
