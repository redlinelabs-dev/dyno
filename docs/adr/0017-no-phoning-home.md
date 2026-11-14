# ADR-0017: No phoning home; observability belongs to the operator

Status: accepted 2026-10-31 by the owner, who asked for this ADR after asking
how to capture metrics when dyno runs on a VPS. Amends ADR-0002, which listed
`GET /metrics` on the main port.

## Decision

**dyno sends nothing anywhere the operator didn't point it.** No analytics,
error reporting, update checks, install counts or "anonymous usage stats" leave
a server that someone else runs. Nothing is opt-out, because nothing exists to
opt out of. If one of these is ever added, it is off by default, named in the
README, and turned on by the operator with a setting.

The web UI follows the same rule. It loads no third-party script, font or
pixel. Fonts and assets are embedded in the binary.

**Operators get their own data instead:**

- **Results** stay in their SQLite file and their `GET /api/v1/results`.
- **Metrics:** `GET /metrics` in Prometheus text format, on its own listener
  set with `DYNO_METRICS_LISTEN` (for example `127.0.0.1:3967` or a tailnet
  address). It's off when unset, and it's never on the main port. It's written
  by hand, without the Prometheus client library, to keep the footprint small
  (ADR-0006).
- **Logs** go to stdout for Docker or journald to keep.

**An operator who wants page-view analytics on a public instance adds it to
their own deployment**, for example through their reverse proxy. dyno doesn't
ship a snippet slot for it.

## Why

- People choose self-hosted tools largely to keep their data at home.
  Telemetry on by default is a common complaint on r/selfhosted. Cloudflare's
  speed test library sends results to Cloudflare by default
  (`docs/research/market-and-demand.md` §1), and M-Lab publishes test takers'
  IP addresses. Not doing either is part of what dyno offers.
- dyno has no login (ADR-0008), so anything on the main port is public
  wherever the test page is. Aggregate results, client counts and memory use
  are the operator's business. A separate listener lets them bind it to
  loopback or the tailnet without a reverse-proxy rule.
- Redline Labs gets no usage numbers from installs. Stars, image pulls and
  issues are the signal instead. That trade is deliberate.

## Consequences

- No crash reports from the field. Bugs arrive as issues, so error messages
  and logs need to be good enough to paste into one.
- The first-party sites (a demo instance, a marketing page) are separate
  deployments and can use privacy-respecting analytics such as GoatCounter.
  That doesn't change the product.
- A future pro or fleet feature that reports to a central dyno does so
  between the operator's own nodes, never to Redline Labs.
- `docs/api.md` documents `/metrics` and `DYNO_METRICS_LISTEN` when it ships.
