# dyno

A self-hosted network speed test by Redline Labs. Press start, get one honest
number and your latency. Open telemetry when you want the rest: bufferbloat
grade, latency under load, speed over time, scheduled tests and history.

It's designed to idle in under 24 MiB of memory and ship as one static binary
in one container.

Status: M2. The web UI runs real tests and keeps a history; scheduled tests
and monitoring come in M3. See `docs/ROADMAP.md`.

## Running it

```sh
docker compose up -d     # then open http://<this-host>:3966
```

Or without Docker: `make build && bin/dyno` (needs Go and Bun). The API is in
`docs/api.md`. Test history is a SQLite file in the `dyno-data` volume, or
`./data` without Docker.

There is no login (ADR-0008). Anyone who can reach dyno can run tests and
read the history, so keep it on your LAN or put it behind a VPN or a reverse
proxy that does auth.

The compose file uses host networking. On Docker Desktop (macOS, Windows),
turn on host networking in Settings, Resources, Network first (4.34 or later).

## Where things are

| Path | What |
|---|---|
| `docs/brief.md` | The original request and the definition of done |
| `docs/decisions-pending.md` | Open questions for the owner |
| `docs/adr/` | Settled decisions: runtime, architecture, measurement |
| `docs/research/` | Market and demand, measurement methodology, runtime benchmark |
| `prototypes/` | Interactive UI prototypes; open the HTML files in a browser |
| `server/` | The Go server |
| `web/` | The web UI, TypeScript built by Bun and embedded in the server |
| `scripts/footprint.ts` | The footprint check behind `make check` and `make bench` |
| `bench/` | Reproducible runtime benchmark (Rust, Go, Zig, Bun) |

## Starting a build session

Open Claude Code in this folder and paste:

> Read CLAUDE.md, then docs/brief.md, docs/decisions-pending.md and
> docs/ROADMAP.md. Ask me any pending decision that blocks the next
> milestone. Then plan that milestone and build it.
