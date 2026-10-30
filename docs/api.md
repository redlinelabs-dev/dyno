# HTTP API

The contract every client uses: the web UI, a future native app, scripts.
The web UI gets nothing beyond this (ADR-0002). Plain HTTP/1.1; keep bulk
transfers off HTTP/2 and HTTP/3 (ADR-0003).

Status: M1. Results, `/metrics` and `/.well-known/nq` arrive in later
milestones and will be added here.

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

## Endpoints

### `GET /api/v1/plan`

```json
{
  "server": { "name": "dyno", "version": "v0.1.0" },
  "endpoints": { "download": "/dl", "upload": "/ul", "ping": "/ping" },
  "latency": { "idle_samples": 20, "loaded_interval_ms": 200 },
  "download": { "streams": 5, "duration_ms": 10000, "warmup_ms": 1500,
                "start_bytes": 1048576, "max_bytes": 268435456, "target_request_ms": 1000 },
  "upload":   { "streams": 5, "duration_ms": 10000, "warmup_ms": 3000,
                "start_bytes": 1048576, "max_bytes": 268435456, "target_request_ms": 1000 }
}
```

Endpoint paths are relative to the origin that served the plan. Clients must
ignore fields they don't know; new fields can appear without a version bump.
Renaming or removing a field needs `/api/v2`.

### `GET /dl?bytes=N`

`200` with exactly N bytes of incompressible data, `0 ≤ N ≤ 17179869184`
(16 GiB). Anything else is `400`. Add a cache-busting parameter anyway (for
example `&r=<random>`); the server ignores unknown parameters.

### `POST /ul`

Send any body up to 16 GiB. The server discards it and replies:

```json
{ "bytes": 26214400, "elapsed_us": 210345 }
```

`elapsed_us` runs from the server receiving the request headers to it reading
the last body byte. The same figure is in `Server-Timing: recv;dur=<ms>`. A
body over 16 GiB gets `413`. A body that stops arriving for 10 s gets `408`
and the connection is closed.

### `GET /ping`

`204`, no body. The latency probe.

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
