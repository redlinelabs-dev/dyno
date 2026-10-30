# ADR-0002: One binary, an API-first server, a framework-free web client

Status: accepted 2026-10-30.
Evidence: `docs/research/methodology-and-architecture.md` §5.

## Decision

- **One container, one process, one static binary.** The Go server embeds
  the built web assets and serves them. `compose.yaml` needs one service and one
  volume for data. No `.env` is required to start.
- **The API is the product boundary.** Plain HTTP plus JSON. The server issues
  a test plan (endpoints, stream count, durations, payload sizes). The client
  runs it and posts its results, and the server stores the result together with
  its own byte counts. Web, iOS and Android clients drive exactly the same
  endpoints. The web UI gets no private endpoints.
- **Endpoints, first cut:** `GET /api/v1/plan`, `GET /dl` (stream N
  incompressible bytes), `POST /ul` (drain and count), `GET /ping` (204, on
  its own connection), `POST /api/v1/results`, `GET /api/v1/results`,
  `GET /metrics` (Prometheus), `GET /.well-known/nq` (Apple
  `networkQuality` compatibility). Version the JSON API from day one.
- **Storage:** SQLite in WAL mode on a local volume (not NFS or SMB). One file,
  with retention configurable from forever down to N days.
- **Scheduling** runs inside the server. Scheduled tests run node to node:
  another dyno instance is the far end, never a third-party test server.
- **Web UI:** TypeScript with no framework, matching the hand-rolled
  prototypes. Bun bundles it, and the output is plain static files. Hot UI paths
  (gauge rendering at 60 fps) avoid allocations and layout thrash, just as the
  server's hot path does.
- **Hot-path rules:** downloads come from one pre-generated random buffer;
  uploads read into fixed scratch buffers; per-session state is fixed-size;
  connection counts and timeouts are hard caps.
- **Paid features** (if the licence split happens) live in their own Go
  package and web bundle, gated by an offline-verified licence file. The free
  build compiles and runs without them.

## Consequences

A native app needs nothing from the server beyond what the web UI already uses.
Multi-node (one UI, many dyno servers) is an extension of the same API, so it
needs no redesign.
