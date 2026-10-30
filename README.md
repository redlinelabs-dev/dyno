# dyno

A self-hosted network speed test by Redline Labs. Press start, get one honest
number and your latency. Open telemetry when you want the rest: bufferbloat
grade, latency under load, speed over time, scheduled tests and history.

It's designed to idle in under 8 MiB of memory and ship as one static binary
in one container.

Status: research and UI prototypes. No server code yet. See `docs/ROADMAP.md`.

## Where things are

| Path | What |
|---|---|
| `docs/brief.md` | The original request and the definition of done |
| `docs/decisions-pending.md` | Open questions for the owner |
| `docs/adr/` | Settled decisions: runtime, architecture, measurement |
| `docs/research/` | Market and demand, measurement methodology, runtime benchmark |
| `prototypes/` | Interactive UI prototypes; open the HTML files in a browser |
| `bench/` | Reproducible runtime benchmark (Rust, Go, Zig, Bun) |

## Starting a build session

Open Claude Code in this folder and paste:

> Read CLAUDE.md, then docs/brief.md, docs/decisions-pending.md and
> docs/ROADMAP.md. Ask me any pending decision that blocks the next
> milestone. Then plan that milestone and build it.
