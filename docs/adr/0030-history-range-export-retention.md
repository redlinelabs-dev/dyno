# ADR-0030: History by date range, CSV export, retention and server settings

Status: accepted 2026-11-13 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). How History looks is the owner's pick from the M3
prototypes. This ADR settles only the API underneath. The contract is
`docs/api.md`.

## Decision

- **`GET /api/v1/results` takes a date range and filters:**
  - `from` and `to` in RFC 3339; `from` is inclusive and `to` exclusive;
  - `source`: `client` or `schedule`;
  - `schedule`: a schedule id;
  - `outcome`: `succeeded` or `failed`.

  `limit` rises to at most 10,000. Rows stream out as they are read, so memory
  doesn't grow with the count.
- **`GET /api/v1/results.csv`** takes the same filters and sends every
  matching row, oldest first, as an attachment. Any text cell a spreadsheet
  would read as a formula gets a leading `'`.
- **Long reads never hold the database.** Results are read in pages of at most
  500 rows, and the connection is let go between pages. A client that stops
  reading an export can't stall stored results or the scheduler: the store
  uses a single connection, to keep SQLite's page cache to one (ADR-0006).
  A page bounds the rows returned, not the rows scanned: a filter that
  matches few rows, such as `outcome=failed`, can scan the whole history in
  one statement. At dyno's volume (a run every 15 minutes is 35,000 rows a
  year) that is milliseconds; if histories ever reach millions of rows, bound
  each page by an id window too.
- **Retention:** `keep_days`. The default is 0, which keeps everything; the
  range is 1 to 36,500 days. Results older than that are deleted, failed runs
  included. Deletion runs at start, every hour, and at once when `keep_days`
  changes. A deletion that removed rows logs one `results.prune` event
  (ADR-0020) at `INFO`, with `deleted_results`, `keep_days` and
  `duration_ms`. One that fails logs it at `ERROR`, because the database
  refused it, with `error.code` `store_read_failed` when `keep_days` can't
  be read and `store_delete_failed` when the delete fails; a failed delete
  also carries `deleted_results`, the rows it removed before it failed. One
  that found nothing to delete logs nothing. The delete goes in batches of
  500 and stops between batches once shutdown starts (or the `PATCH` that
  started it hangs up): it logs `result` `canceled` with `deleted_results` so
  far, or nothing when it deleted nothing, and the next prune finishes the
  backlog. A `PATCH` whose prune shutdown stopped still answers 200 with the
  saved settings, within the grace period.
- **Server settings live at `GET` and `PATCH /api/v1/settings`:**
  `plan_down_mbps`, `plan_up_mbps` and `keep_days`. A `PATCH` changes only the
  fields it sends. An older client that doesn't know a newer field can't
  reset it.
- **Plan speed moves to the server.** It was kept in each browser's storage.
  Alerts compare scheduled results with it (ROADMAP M3), and every device
  should draw the same plan line, so it gets one home
  (`competitor-features.md` §6). The web UI moves a browser's value up the
  first time it finds the server's unset. That lands with the monitoring UI.

## Why

- One range for every chart is the request (ROADMAP M3, History), and the
  chart needs every point in the range. A month of 15-minute runs across 16
  schedules is about 46,000 rows, so the server has to stream, not build the
  list in memory.
- A merge-style `PATCH` keeps the web UI and a future native app (ADR-0027)
  from overwriting each other's newer settings.
- CSV goes to spreadsheets, where a cell starting with `=` runs as a formula,
  and the `client` label is whatever the poster sent.

## Consequences

- Anyone who can reach dyno can set `keep_days` to 1 and delete all but a
  day of history (ADR-0008). The UI confirms before it shrinks retention.
- Deleting the newest results can let SQLite hand their ids out again. An id
  only names a result while that result exists.
