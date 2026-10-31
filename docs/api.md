# HTTP API

The contract every client uses: the web UI, a future native app, scripts.
The web UI gets nothing beyond this (ADR-0002). Plain HTTP/1.1; keep bulk
transfers off HTTP/2 and HTTP/3 (ADR-0003).

Status: M2. `/metrics` and `/.well-known/nq` arrive in later milestones
and will be added here.

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
  "client": "Chrome on Android",
  "download_mbps": 938.2, "upload_mbps": 412.6,
  "idle_ms": 8.4, "loaded_down_ms": 21.0, "loaded_up_ms": 39.5, "jitter_ms": 0.82,
  "streams": 5, "client_limited": false,
  "detail": { "idle": [[0.05, 8.1]], "rate": [[1.2, 870.4, 0]], "probes": [[1.3, 19.8, 0]] }
}
```

Speeds are Mbit/s and latencies milliseconds, finite and between 0 and 10⁶.
`client` is a short label, cut to 80 bytes. `detail` is any JSON value; the
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
50, at most 500) and `?before=<id>` page backwards.

### `GET /api/v1/results/{id}`

One result, `detail` included. `404` if there's no such id.

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
