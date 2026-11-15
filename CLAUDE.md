# dyno

A self-hosted network speed test by Redline Labs: as easy as fast.com, prettier
than LibreSpeed or OpenSpeedTest, with the depth those tools' users ask for
behind progressive disclosure. Private repo, solo developer.

## Read first

- `docs/brief.md`: the owner's request word for word, plus the acceptance
  checks. It outranks every other doc.
- `docs/decisions-pending.md`: open owner calls. Ask about them; leave them
  undecided in code.
- `docs/adr/`: settled decisions, one file each. Write a new ADR whenever you
  settle something the next session would otherwise re-argue.
- `docs/ROADMAP.md`: what to build next, in order.
- `docs/research/`: the evidence. Read the matching file before touching that
  area: `methodology-and-architecture.md` before any measurement code,
  `runtime-benchmark.md` before changing the server's runtime or hot path,
  `market-and-demand.md` and `competitor-features.md` before adding a feature
  (the latter ranks what competitors ship and what their users still ask
  for), `market-and-demand.md` before touching licensing.
- `prototypes/dyno-ui.html`: the signed-off UI (ADR-0004). The web UI matches
  it; it is the spec for look and behaviour. The other files there are the
  rounds it was picked from.

## Shape

| Path | What |
|---|---|
| `server/` | Go server: test endpoints, JSON API, scheduler, SQLite, embedded web assets. One static binary. |
| `web/` | TypeScript UI, no framework, bundled by Bun into static files the server embeds. |
| `bench/` | Reproducible runtime benchmark (`bench/README.md`). Also the base for footprint regression checks. |
| `install/` | `install.sh` (systemd service on Debian/Ubuntu) and `proxmox-lxc.sh` (container on a Proxmox host), POSIX sh (ADR-0024). |
| `prototypes/` | Standalone HTML prototypes. Reference only, never imported. |

`web/` builds into `server/internal/webui/dist/`, which is gitignored, so
build with `make` rather than bare `go build`. Follow ADR-0002 for layout and
ADR-0010 for how the browser runs a test. The API contract
is `docs/api.md`; keep it in step with the code. Server defaults and caps are
ADR-0007.

## Rules that are easy to miss

- **Footprint is a feature.** Every change to the server keeps idle RSS,
  RSS under load, CPU per GB and the soak leak check within the budgets in
  ADR-0006. Measure with `make bench`, paste its table into the PR, and
  treat a regression as a bug.
- **The web UI is just another API client.** It uses only the public HTTP API
  a native app would use (ADR-0002). Server-rendered state or UI-only
  endpoints break the future mobile app.
- **Measurement rules come from the research, not intuition** (ADR-0003):
  HTTP/1.1 for bulk transfer, at most 5 load streams, latency probes on their
  own connection, upload counted on the server, incompressible payloads with
  compression off.
- **Progressive disclosure is the product.** The first screen holds one action,
  one number, and latency. A new feature goes behind the telemetry layer
  unless the owner says otherwise.
- **Accessibility is WCAG 2.2 AA** (ADR-0015): keyboard paths, screen-reader
  text for anything drawn, 4.5:1 text contrast. Re-check after UI changes.
- **No Ookla code, CLI or servers.** Their EULA bans the multi-device and
  commercial use dyno exists for.
- **Paid features stay separable.** Keep anything that could become paid in
  its own module so a commercial license or pro module (ADR-0021) costs no
  rewrite.
- **Brand.** Redline Labs palette and type (carbon #15171C, red #E3242B,
  deep red #8E141B, needle orange #FF5A1F, bone #F2EDE2, bezel #D8DBE0; Big
  Shoulders Display for numerals). Gauges have no numerals on the logo, but the
  app's dial labels its log scale.

## Tooling

TypeScript scripts run directly with Bun (`bun scripts/x.ts`), using Bun's own
APIs. The installers in `install/` are the exception: POSIX sh, because a
fresh host has no Bun (ADR-0024). Python is not used here. Go is the server
language. Build and test from the root `Makefile` (it finds Go in `~/.local/go` itself):

| Command | What |
|---|---|
| `make web` | Web UI into `server/internal/webui/dist/` (the other targets run it first) |
| `make build` | Static server binary at `bin/dyno`, web UI embedded |
| `make release` | Both static binaries, the install scripts and `SHA256SUMS` in `dist/` (ADR-0024) |
| `make check` | gofmt, go vet, `go test -race`, web typecheck and `bun test`, installer tests (`make installcheck`: `dash -n` and `bun test install/`), quick footprint check (~2 min). Run before every commit. |
| `make bench` | Full footprint check with the 12-minute soak (~15 min); records to `bench/results/dyno-footprint.jsonl` |
| `make image` / `make image-multiarch` | Container image; needs Docker, so not on this machine |

Run the server locally with `bin/dyno` (listens on :3966, `DYNO_LISTEN` to
change; results go to `./data/dyno.db`, `DYNO_DATA` to change).

This machine is a headless Debian box. Toolchains are user-local: Go in
`~/.local/go` (not on PATH; use `~/.local/go/bin/go`). Zig and Rust are
there only for `bench/`. Docker isn't
installed here, so container builds and device tests run on the owner's other
machines or in CI. When you hand back work you couldn't run, say exactly what
is unverified.
