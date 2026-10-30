# dyno

A self-hosted network speed test by Redline Labs: as easy as fast.com, prettier
than LibreSpeed or OpenSpeedTest, with the depth those tools' users ask for
behind progressive disclosure. Private repo, solo developer. Working name.

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
  `market-and-demand.md` before adding a feature or touching licensing.
- `prototypes/`: the signed-off UI direction lives here. The web UI matches
  the prototype the owner picked; it is the spec for look and behaviour.

## Shape

| Path | What |
|---|---|
| `server/` | Rust server: test endpoints, JSON API, scheduler, SQLite, embedded web assets. One static binary. |
| `web/` | TypeScript UI, no framework, bundled by Bun into static files the server embeds. |
| `bench/` | Reproducible runtime benchmark (`bench/README.md`). Also the base for footprint regression checks. |
| `prototypes/` | Standalone HTML prototypes. Reference only, never imported. |

`server/` and `web/` don't exist yet; the roadmap's first milestone creates
them. Follow ADR-0002 for layout and the API contract.

## Rules that are easy to miss

- **Footprint is a feature.** Every change to the server keeps idle RSS,
  RSS under load, CPU per GB and the soak slope within the budgets in
  ADR-0001. Measure with `bench/`, report the numbers in the PR, and treat
  a regression as a bug.
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
- **No Ookla code, CLI or servers.** Their EULA bans the multi-device and
  commercial use dyno exists for.
- **Paid features stay separable.** Keep anything that could become paid in
  its own module so the licence split (pending) costs no rewrite.
- **Brand.** Redline Labs palette and type (carbon #15171C, red #E3242B,
  deep red #8E141B, needle orange #FF5A1F, bone #F2EDE2, bezel #D8DBE0; Big
  Shoulders Display for numerals). Gauges have no numerals on the logo, but the
  app's dial labels its log scale.

## Tooling

TypeScript scripts run directly with Bun (`bun scripts/x.ts`), using Bun's own
APIs. Python is not used here. Rust is the server language. Build and test
commands go in a root `Makefile` once milestone 1 lands; list them here then.

This machine is a headless Debian box. Toolchains are user-local: Go in
`~/.local/go`, Zig in `~/.local/zig`, Rust through rustup. Docker isn't
installed here, so container builds and device tests run on the owner's other
machines or in CI. When you hand back work you couldn't run, say exactly what
is unverified.
