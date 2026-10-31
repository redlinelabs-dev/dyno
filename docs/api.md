# HTTP API

The contract every client uses: the web UI, a future native app, scripts.
The web UI gets nothing beyond this (ADR-0002). Plain HTTP/1.1; keep bulk
transfers off HTTP/2 and HTTP/3 (ADR-0003).

Status: M2.5. `/metrics` is in, on its own listener. `/.well-known/nq`
arrives in a later milestone and will be added here.

## Running a test

1. `GET /api/v1/plan` and follow it. Don't hard-code paths or stream counts.
2. Idle latency: send `latency.idle_samples` pings, one after another, on a
   warm connection that carries nothing else.
3. Each direction: open `streams` connections and send requests back to back
   for `duration_ms`. Start each stream at `start_bytes` per request and
   double the size until a request takes `target_request_ms`, never above
   `max_bytes`. Drop samples from the first `warmup_ms`.
4. While a direction runs, ping every `latency.loaded_interval_ms` on a
   separate connection, never on a load stream.
5. Upload speed comes from the server's `bytes` and `elapsed_us`, not from
   client progress events (ADR-0003).
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
                "start_bytes": 1048576, "max_bytes": 33554432, "target_request_ms": 1000 }
}
```

Every call returns a fresh `test_id` (24 hex characters). The server keeps a
test's byte counts for 10 minutes, and for at most 64 tests at a time.
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
the last body byte. The same figure is in `Server-Timing: recv;dur=<ms>`.
`?t=<test_id>` adds the bytes received to that test's count. A
body over 16 GiB gets `413`. A body that stops arriving for 10 s gets `408`
and the connection is closed.

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
server stores it as is. Bad input is
`400` with `{"error": "..."}`.

The reply is `201` with `Location: /api/v1/results/<id>` and the stored
result: the fields above plus

| Field | Meaning |
|---|---|
| `id`, `at` | Result id and server time (RFC 3339, UTC) |
| `source` | `client` for posted results; scheduled tests arrive in M3 |
| `bufferbloat_ms` | Worse of the two loaded latencies minus idle |
| `grade` | `A+` under 10 ms, `A` 20, `B` 50, `C` 100, `D` 500, else `F` (Cloudflare's thresholds); empty when there's no loaded latency |
| `rpm` | Round trips per minute under load, 60000 / loaded latency |
| `server_bytes_down`, `server_bytes_up` | What the server itself sent and received for this `test_id`; 0 if unknown or expired |

### `GET /api/v1/results`

`{"results": [...]}`, newest first, without `detail`. `?limit=N` (default
50, at most 500) and `?before=<id>` page backwards. `?device=<id>` keeps
one device's results.

### `GET /api/v1/results/{id}`

One result, `detail` included. `404` if there's no such id.

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
the database at start.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `dyno_build_info` | gauge | `version` | Always 1; the running version |
| `dyno_results_total` | counter | `grade`: `A+`, `A`, `B`, `C`, `D`, `F`, `none` | Results stored. `none` is a result without loaded latency. Every grade is always present |
| `dyno_result_download_bytes_per_second` | histogram | `le` | Download speed of stored results, in bytes per second as Prometheus base units ask (multiply by 8 for bits). Buckets match 1, 5, 10, 25, 50, 100, 250, 500 Mbit/s and 1, 2.5, 5, 10 Gbit/s: 125e3, 625e3, 1.25e6, 3.125e6, 6.25e6, 1.25e7, 3.125e7, 6.25e7, 1.25e8, 3.125e8, 6.25e8, 1.25e9 |
| `dyno_result_upload_bytes_per_second` | histogram | `le` | Upload speed of stored results, same buckets |
| `dyno_result_idle_latency_seconds` | histogram | `le` | Idle latency of stored results. Buckets .005, .01, .02, .05, .1, .2, .5, 1, 2 |
| `dyno_result_loaded_latency_seconds` | histogram | `le` | The worse of a result's two loaded latencies, same buckets. Results without loaded latency are skipped |
| `dyno_bytes_sent_total` | counter | | Every byte `/dl` wrote, tagged with a test id or not |
| `dyno_bytes_received_total` | counter | | Every byte `/ul` read, tagged or not |
| `dyno_streams_active` | gauge | `direction`: `download`, `upload` | `/dl` and `/ul` requests in flight |
| `dyno_requests_total` | counter | `route`, `code` | Finished requests on the main listener. `route` is one of `dl`, `ul`, `ping`, `plan`, `results` (all of `/api/v1/results`), `ui` (the web UI, including its 404s), `other` (no route matched: 404 or 405); never a raw path. `code` is the numeric status. A pair appears once it has been seen, and the metric once any pair has |
| `process_resident_memory_bytes` | gauge | | Resident memory, from `/proc/self/statm` at scrape time. Left out where that file doesn't exist |
| `process_start_time_seconds` | gauge | | Process start, Unix seconds |
| `go_goroutines` | gauge | | Goroutines running |

Speeds are bits per second and latencies seconds, the Prometheus base units;
results store Mbit/s and ms, and the server converts.

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
