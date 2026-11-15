# HTTP API

The contract every client uses: the web UI, a future native app, scripts.
The web UI gets nothing beyond this (ADR-0002). Plain HTTP/1.1; keep bulk
transfers off HTTP/2 and HTTP/3 (ADR-0003). Every JSON reply, errors
included, is `Content-Type: application/json`.

Status: M3, server side. Schedules, settings, history by range, CSV export and
retention are in; the web UI for them comes next. `/metrics` is on its own
listener. `/.well-known/nq` arrives in a later milestone and will be added here.

## Running a test

1. `GET /api/v1/plan` and follow it. Don't hard-code paths or stream counts.
2. Idle latency: send `latency.idle_samples` pings, one after another, on a
   warm connection that carries nothing else.
3. Each direction: open `streams` connections and send requests back to back
   for `duration_ms`. Start each stream at `start_bytes` per request and
   double the size until a request takes `target_request_ms`, never above
   `max_bytes`. Upload instead sizes each request to take `target_request_ms`
   at the rate the previous one reached, from 64 KiB to `max_bytes`: its
   count only arrives with each reply, so long requests leave it stale.
   Drop samples from the first `warmup_ms`. If any stream fails, the test
   has failed: don't report or store a result.
4. While a direction runs, ping every `latency.loaded_interval_ms` on a
   separate connection, never on a load stream.
5. Upload speed comes from the server's `bytes`, not from client progress
   events, which count bytes the browser handed to its socket (ADR-0003). Treat
   each request's `bytes` as spread evenly from opening it to its reply. The
   count is complete up to the earliest of the streams' last replies; the
   result is all streams' bytes from the end of warmup to that point, over that
   one window. If the window is under a second, or a request in it lasted more
   than half of it, use every byte over the span from the first request to the
   last reply instead. Chart the same way: the rate over the
   last second while spreading holds, else every confirmed byte over the time
   since the first request. Don't add per-stream rates timed over different
   windows, and don't divide by `elapsed_us`: both read high.
6. Tag every `/dl` and `/ul` request with `?t=<test_id>` from the plan, then
   `POST` the result to `endpoints.results` with the same `test_id`. The
   server stores its own byte counts and the grade with it.

The web UI's choices within these rules are ADR-0010.

## Endpoints

### `GET /api/v1/plan`

```json
{
  "test_id": "3f9c2a7e01b44d6a9e5c8b12",
  "server": { "name": "dyno", "version": "v0.1.0" },
  "endpoints": { "download": "/dl", "upload": "/ul", "ping": "/ping", "results": "/api/v1/results" },
  "latency": { "idle_samples": 20, "loaded_interval_ms": 200 },
  "download": { "streams": 5, "duration_ms": 10000, "warmup_ms": 1500,
                "start_bytes": 1048576, "max_bytes": 268435456, "target_request_ms": 1000 },
  "upload":   { "streams": 5, "duration_ms": 10000, "warmup_ms": 3000,
                "start_bytes": 262144, "max_bytes": 33554432, "target_request_ms": 1000 }
}
```

Every call returns a fresh `test_id` (24 hex characters). The server counts
bytes only for ids it issued, for 10 minutes, and for at most 64 tests at a
time; when all 64 are moving bytes, a new plan's bytes go uncounted rather
than wiping a running test's.
Upload requests stop at 32 MiB because a browser holds the whole body in
memory.

Endpoint paths are relative to the origin that served the plan. Clients must
ignore fields they don't know; new fields can appear without a version bump.
Renaming or removing a field needs `/api/v2`.

### `GET /dl?bytes=N`

`200` with exactly N bytes of incompressible data, `0 ≤ N ≤ 17179869184`
(16 GiB). Anything else is `400`. Add a cache-busting parameter anyway (for
example `&r=<random>`); the server ignores unknown parameters. `&t=<test_id>`
adds the bytes sent to that test's count.

### `POST /ul`

Send any body up to 16 GiB. The server discards it and replies:

```json
{ "bytes": 26214400, "elapsed_us": 210345 }
```

`elapsed_us` runs from the server receiving the request headers to it reading
the last body byte, so it is a per-request figure rather than a stream's rate
(see step 5). The same figure is in `Server-Timing: recv;dur=<ms>`.
`?t=<test_id>` adds the bytes received to that test's count. A
body over 16 GiB gets `413`. A body that takes more than 10 s to deliver any
64 KiB of itself gets `408` and the connection is closed. A body that ends
before its `Content-Length` gets `400`: an aborted upload, never a short one.

Errors from `/dl` and `/ul` are `{"error": "..."}` with the headers below.

### `GET /ping`

`204`, no body. The latency probe.

### `POST /api/v1/results`

Stores a finished test. Body, at most 256 KiB:

```json
{
  "test_id": "3f9c2a7e01b44d6a9e5c8b12",
  "client": "Kitchen iPad",
  "device": "Kq3xT0b9yWm2vRcL5aZ1eA",
  "download_mbps": 938.2, "upload_mbps": 412.6,
  "idle_ms": 8.4, "loaded_down_ms": 21.0, "loaded_up_ms": 39.5, "jitter_ms": 0.82,
  "streams": 5, "client_limited": false,
  "detail": { "idle": [[0.05, 8.1]], "rate": [[1.2, 870.4, 0]], "probes": [[1.3, 19.8, 0]] }
}
```

Speeds are Mbit/s and latencies milliseconds, finite and between 0 and 10⁶.
`client` is the device's name, cut to 80 bytes: one the owner gave it, or a
label like "Chrome on Android". `device` is optional: an id the client makes
up for itself and sends with every result, up to 64 letters, digits, `-` or
`_` (ADR-0022). It isn't a secret or a login, just a way to list one
device's results. `detail` is any JSON value; the
web UI sends its time series there (seconds from test start, `dir` 0 for
download and 1 for upload), plus `spans`: each transfer's
`[dir, start, counted, end]`, where `counted` is when warm-up ends. The
server stores it as is. `test_id` must be the plan's or empty. A result
needs at least one speed or latency, and the body holds one JSON object and
nothing after it. Bad input is `400` with `{"error": "..."}`.

The reply is `201` with `Location: /api/v1/results/<id>` and the stored
result: the fields above plus

| Field | Meaning |
|---|---|
| `id`, `at` | Result id and server time (RFC 3339, UTC) |
| `source` | `client` for posted results, `schedule` for scheduled tests |
| `bufferbloat_ms` | Worse of the two loaded latencies minus idle |
| `grade` | `A+` under 10 ms, `A` 20, `B` 50, `C` 100, `D` 500, else `F` (Cloudflare's thresholds); empty when there's no loaded latency |
| `rpm` | Round trips per minute under load: 60000 / the worse loaded latency, rounded, at most 1,000,000 |
| `server_bytes_down`, `server_bytes_up` | What the server itself sent and received for this `test_id`; 0 if unknown or expired |

A scheduled test's result (see Schedules) has `client` `dyno/<version>` for
the dyno that ran it, no `device` and no `test_id`, and these fields, which
client results don't have:

| Field | Meaning |
|---|---|
| `schedule_id` | The schedule it ran for |
| `far_end` | The far end's base URL when it ran |
| `path_changed` | `true` when its idle latency is more than twice the far end's baseline and at least 10 ms above it: likely a relay or VPN on the path, not a slow ISP. The baseline is the median idle latency of the schedule's last 10 successful results against the same `far_end`, and needs at least 3. Absent when `false` |
| `failure` | Only on a failed run: `{"code": "...", "phase": "...", "message": "..."}`. A failed run's speeds and latencies are 0 and its grade is empty |

A failure's `code` says what was seen, which points at an end:

| `code` | What was seen |
|---|---|
| `unreachable` | No connection: the name didn't resolve, the port refused, there was no route, or TLS failed (an untrusted certificate, say). The far end may be down, or this end's connection may be |
| `timeout` | Connected, but a reply didn't come in time, or the run passed 5 minutes |
| `not_dyno` | Something answered that isn't a dyno: its plan request got an error status or a reply that isn't a dyno plan, or it redirected |
| `http_status` | The far end answered a test request with an error status |
| `stream_failed` | A transfer broke off part way |
| `bad_far_end` | The stored address can't be a far end |
| `internal` | The run failed inside this dyno; its log says why |

`phase` is the stage that failed: `warm`, `latency`, `download` or
`upload`, or empty before the far end's plan was read. `message` is a plain
sentence about what was seen.

### `GET /api/v1/results`

`{"results": [...]}`, newest first, without `detail`. Rows are sent as they
are read, so a long list costs the server no extra memory.

| Parameter | Meaning |
|---|---|
| `limit` | At most this many; default 50, at most 10,000 |
| `before` | Only results with a smaller id, to page backwards from the last id seen |
| `device` | One device's results (ADR-0022) |
| `from`, `to` | Results stored at or after `from` and before `to`, both RFC 3339 (`2026-11-13T00:00:00Z`, or with an offset). `to` must be after `from` |
| `source` | `client` or `schedule` |
| `schedule` | One schedule's results, by id |
| `outcome` | `succeeded` or `failed` |

Parameters combine. A bad value is `400` with `{"error": "..."}`.

### `GET /api/v1/results.csv`

Every result the parameters above select (`limit` and `before` don't
apply), oldest first, as `text/csv; charset=utf-8` with
`Content-Disposition: attachment; filename="dyno-results.csv"`. The first
row names the columns:

```
id,at,source,schedule_id,far_end,device,client,download_mbps,upload_mbps,idle_ms,loaded_down_ms,loaded_up_ms,jitter_ms,bufferbloat_ms,grade,rpm,streams,client_limited,path_changed,failure_code,failure_phase,failure_message,server_bytes_down,server_bytes_up
```

`at` is RFC 3339 in UTC, as in the JSON. Booleans are `true` or `false`.
`schedule_id` is empty for client results. Quoting follows RFC 4180. A text
cell that starts with `=`, `+`, `-`, `@`, a tab or a carriage return gets a
leading `'`, so a spreadsheet doesn't run it as a formula (ADR-0030). A bad
parameter is `400` with `{"error": "..."}`, before any CSV is sent.

Neither list holds the database while it writes: rows are read at most 500
at a time, so a client that stops reading doesn't hold up anyone else. If
the database fails after rows were sent, the server breaks the connection
instead of ending the reply, so a client never mistakes part of a list for
all of it.

### `GET /api/v1/results/{id}`

One result, `detail` included. `404` if there's no such id.

## Schedules

A schedule runs a test from this dyno against another dyno, the far end, on
a timetable (ADR-0028, ADR-0029). Results are stored here with `source`
`schedule`, and nothing is posted to the far end. At most 16 schedules exist,
and one scheduled test runs at a time; the others queue.

A schedule as the API returns it:

```json
{
  "id": 1,
  "name": "tailmeadow",
  "far_end": "http://tailmeadow:3966",
  "kind": "light",
  "every_minutes": 180,
  "offset_minutes": 0,
  "paused": false,
  "created_at": "2026-11-13T17:02:11.408Z",
  "updated_at": "2026-11-13T17:02:11.408Z",
  "next_run": "2026-11-13T18:00:00Z",
  "running": false,
  "queued": false,
  "last_result": null,
  "last_success_at": null
}
```

| Field | Meaning |
|---|---|
| `id` | Never reused, even after a delete |
| `name` | Up to 64 bytes of UTF-8 without control characters, trimmed of surrounding spaces. Empty means the far end's host, cut to 64 bytes if it is longer |
| `far_end` | The far end's base URL. What's sent is normalized: no scheme means `http://`, and then no port means `:3966`, so `tailmeadow` and `tailmeadow:3966` both become `http://tailmeadow:3966`. With a scheme, its default port applies, so `https://dyno.example.com` stays as it is. Only `http` and `https`, with no path (a lone `/` is dropped), query, fragment or user info. An IPv6 address goes in brackets. A host name is ASCII, so write any other as punycode (`xn--…`). Scheme and host are lowercased |
| `kind` | `light` (the default, also for `""`): at most 5 s per direction after a 1 s warm-up, and at most 100 MiB each way. `full`: the far end's plan as it is |
| `every_minutes` | 15, 20, 30, 60, 120, 180 (the default, also for `0`), 240, 360, 480, 720 or 1440 |
| `offset_minutes` | Runs happen at midnight UTC plus `offset_minutes`, plus multiples of `every_minutes`. 0 (the default) to `every_minutes` − 1 |
| `paused` | A paused schedule runs only when asked to, through `/run` |
| `created_at`, `updated_at` | Server times |
| `next_run` | The next time on its grid after now; `null` when paused |
| `running`, `queued` | A run of it is going, or is waiting: for another schedule's run, or for a test this dyno is serving to end |
| `last_result` | Its newest result, failed or not, without `detail`; `null` before the first |
| `last_success_at` | When its newest successful result was stored; `null` if none |

Runs missed while dyno was down aren't made up. Before a run starts, if this
dyno is serving a test (`/dl` or `/ul` in flight), the run waits up to 2
minutes for it to end, then runs. A run still going after 5 minutes is
stopped and stored as `timeout`. Warm-up, clamps and failures follow ADR-0028.

Request bodies are one JSON object, at most 4 KiB. An unknown field, a wrong
type or a value out of range is `400` with `{"error": "..."}` naming the
field, and changes nothing.

A failure on this server's side is `500` with `{"error": "..."}`: a database
that refuses a read or a write, or a far-end check that failed inside dyno
rather than at the far end. The server's log says why (ADR-0020).

### `GET /api/v1/schedules`

`{"schedules": [...]}`, oldest first.

### `GET /api/v1/schedules/{id}`

One schedule. `404` if there's no such id.

### `POST /api/v1/schedules/check`

Asks whether a dyno answers at an address, and saves nothing. It makes one
`GET` of the address's plan, follows no redirects, and gives up after 10 s.
Body: `{"far_end": "tailmeadow"}`. The reply is one of:

- `200` with `{"far_end": "http://tailmeadow:3966", "name": "dyno", "version": "0.1.0"}`:
  the normalized address, and the plan's `server.name` and `server.version`;
- `400` when the address can't be a far end;
- `422` with `{"error": "...", "code": "unreachable"}` when the check fails.
  The code is `unreachable`, `timeout` or `not_dyno`, and the error is a plain
  sentence;
- `503` with `Retry-After: 1` when 4 checks are running already, or when the
  server is stopping (the `error` says which).

### `POST /api/v1/schedules`

Adds a schedule. Body: `far_end` (required), and optionally `name`, `kind`,
`every_minutes`, `offset_minutes` and `paused`. The far end is checked first,
as above. The reply is one of:
- `201` with `Location: /api/v1/schedules/<id>` and the schedule;
- `400` for bad input;
- `422` when the check fails, with nothing saved;
- `409` when 16 schedules exist already, before any check;
- `503` as above.

The schedule runs from its next time on, with no restart.

### `PATCH /api/v1/schedules/{id}`

Changes the fields sent and leaves the rest. Only a changed `far_end` is
checked, so a schedule can be paused or retimed while its far end is down. A
`name` of `""` goes back to the far end's host. The reply is one of:
- `200` with the schedule; `next_run` reflects the change at once;
- `400` for bad input, or a patch that leaves the schedule invalid;
- `404` if there's no such id;
- `422` when the check of a changed `far_end` fails, with nothing changed;
- `503` with `Retry-After: 1` when `far_end` changed and 4 checks are
  running already, or the server is stopping.

### `DELETE /api/v1/schedules/{id}`

`204`. A run of it that's going is stopped, and nothing is stored for it. Its
results stay. `404` if there's no such id.

### `POST /api/v1/schedules/{id}/run`

Queues a run now, paused or not, and replies `202` with the schedule, with
`queued` or `running` set. A schedule that is queued or running already isn't
queued again. `429` with `Retry-After` (seconds) when its last result was
stored less than 60 s ago. `404` if there's no such id. `503` if this server
isn't running scheduled tests.

## Settings

### `GET /api/v1/settings`

```json
{"plan_down_mbps": 500, "plan_up_mbps": 50, "keep_days": 0}
```

| Field | Meaning |
|---|---|
| `plan_down_mbps`, `plan_up_mbps` | The speeds the owner's plan promises, for the plan line on charts and for alerts relative to it. 0 (the default) means not set; at most 100,000 |
| `keep_days` | Results older than this many days are deleted, failed runs included. 0 (the default) keeps everything; at most 36,500 |

### `PATCH /api/v1/settings`

Changes the fields sent, leaves the rest, and replies `200` with every
setting. `{}` changes nothing and replies `200` too. Bodies follow the rules
under Schedules: an unknown field, a wrong type or a value out of range is
`400`, and nothing changes. Old results are deleted at start, every hour, and
at once when `keep_days` is sent (ADR-0030). If that deletion fails, the
reply is still `200`: the settings are saved, the failure is logged, and the
hourly run tries again.

## Metrics: `GET /metrics`

Prometheus text exposition format 0.0.4 (`Content-Type: text/plain;
version=0.0.4; charset=utf-8`), for the operator's own Prometheus
(ADR-0017). It is **not on the main port**: set `DYNO_METRICS_LISTEN` to an
address such as `127.0.0.1:3967` or a tailnet address to turn it on. Unset,
there is no metrics listener at all. If the address can't be bound, dyno logs
`server.start` with `error.code` `metrics_listen_failed` and exits 1.

That listener answers `GET /metrics` and nothing else: any other path or
method is `404`. It takes at most 8 connections, request headers within 10 s,
the whole request within 10 s, and closes idle connections after 60 s.

Counters count from process start; use `rate()` or `increase()`, which
handle restarts. Results are counted as they are stored, not read back from
the database at start; the one exception is a schedule's last-success gauges,
which are.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `dyno_build_info` | gauge | `version` | Always 1; the running version |
| `dyno_results_total` | counter | `grade`: `A+`, `A`, `B`, `C`, `D`, `F`, `none` | Client results stored; scheduled ones have their own metrics below. `none` is a result without loaded latency. Every grade is always present |
| `dyno_result_download_bytes_per_second` | histogram | `le` | Download speed of stored client results, in bytes per second as Prometheus base units ask (multiply by 8 for bits). Buckets match 1, 5, 10, 25, 50, 100, 250, 500 Mbit/s and 1, 2.5, 5, 10 Gbit/s: 125e3, 625e3, 1.25e6, 3.125e6, 6.25e6, 1.25e7, 3.125e7, 6.25e7, 1.25e8, 3.125e8, 6.25e8, 1.25e9 |
| `dyno_result_upload_bytes_per_second` | histogram | `le` | Upload speed of stored client results, same buckets |
| `dyno_result_idle_latency_seconds` | histogram | `le` | Idle latency of stored client results. Buckets .005, .01, .02, .05, .1, .2, .5, 1, 2 |
| `dyno_result_loaded_latency_seconds` | histogram | `le` | The worse of a result's two loaded latencies, same buckets. Results without loaded latency are skipped |
| `dyno_bytes_sent_total` | counter | | Every byte `/dl` wrote, tagged with a test id or not |
| `dyno_bytes_received_total` | counter | | Every byte `/ul` read, tagged or not |
| `dyno_streams_active` | gauge | `direction`: `download`, `upload` | `/dl` and `/ul` requests in flight |
| `dyno_requests_total` | counter | `route`, `code` | Finished requests on the main listener. `route` is one of `dl`, `ul`, `ping`, `plan`, `results` (all of `/api/v1/results`, CSV included), `schedules` (all of `/api/v1/schedules`), `settings`, `ui` (the web UI, including its 404s), `other` (no route matched: 404 or 405); never a raw path. `code` is the numeric status. A pair appears once it has been seen, and the metric once any pair has |
| `dyno_schedule_runs_total` | counter | `schedule`, `name`, `result`: `succeeded`, `failed` | Scheduled runs finished since start. Both `result` values are present for every schedule |
| `dyno_schedule_last_success_timestamp_seconds` | gauge | `schedule`, `name` | When the schedule's newest successful result was stored, Unix seconds. Read back from the database at start, and absent until the schedule has one. Alert when `time()` minus this passes a few intervals: the scheduler has stopped, or every run is failing |
| `dyno_schedule_download_bytes_per_second`, `dyno_schedule_upload_bytes_per_second` | gauge | `schedule`, `name` | The newest successful result's speeds |
| `dyno_schedule_idle_latency_seconds`, `dyno_schedule_loaded_latency_seconds` | gauge | `schedule`, `name` | Its idle latency, and the worse of its two loaded latencies. The loaded one is absent when it wasn't measured |
| `process_resident_memory_bytes` | gauge | | Resident memory, from `/proc/self/statm` at scrape time. Left out where that file doesn't exist |
| `process_start_time_seconds` | gauge | | Process start, Unix seconds |
| `go_goroutines` | gauge | | Goroutines running |
| `go_gc_heap_live_bytes` | gauge | | Heap held by live objects as of the last garbage collection |
| `go_memory_classes_total_bytes` | gauge | | All memory the Go runtime has mapped |
| `go_memory_classes_heap_released_bytes` | gauge | | Heap memory the Go runtime has returned to the OS. Mapped minus released is what Go may have resident; the footprint soak uses it to tell Go's memory from SQLite's (ADR-0006) |

Speeds are bytes per second and latencies seconds, the Prometheus base units;
results store Mbit/s and ms, and the server converts. For the schedule
metrics, `schedule` is the schedule's id and `name` its name, with `"`,
`\` and line breaks escaped as the format asks; a deleted schedule's
series go away.

## Headers on measurement responses

`/dl`, `/ul` and `/ping` send `Cache-Control: no-store, no-transform`,
`X-Accel-Buffering: no` (so nginx doesn't buffer the response) and
`Timing-Allow-Origin: *` (so Resource Timing works across origins). Nothing is
compressed. CORS isn't enabled yet; it arrives with multi-node.

## Limits

- At most 256 open connections. Past that, new connections wait in the
  kernel's queue until one closes.
- Request headers must arrive within 10 s; idle keep-alive connections close
  after 60 s.
- A download whose client stops reading, or an upload whose client stops
  sending, is dropped after 10 s.
