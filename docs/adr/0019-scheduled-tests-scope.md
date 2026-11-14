# ADR-0019: Scheduled ISP monitoring, against the operator's own dyno

Status: accepted 2026-10-31 by the owner (was decisions-pending #4). Offered
"your own dyno as the far end only (recommended for now)" or "also add an
opt-in Cloudflare engine in M3", the owner answered "ok all this sounds
great - go ahead then" and confirmed: "yes exactly".

## Decision

- **v1 does both.** A: a browser tests against the dyno that served it, as
  built in M2. B: a dyno runs scheduled tests from the server side, in M3.
- **B's far end is always another dyno the operator runs**: on a VPS, at
  another site over a tailnet, or on the LAN. Never Ookla (its EULA), never a
  server Redline Labs runs (ADR-0018).
- **No Cloudflare engine for now.** Testing against Cloudflare's public speed
  endpoints, as MySpeed and cloudflare-speed-cli do, would let people with one
  box monitor their ISP. It is deferred because Cloudflare publishes no usage
  terms for those endpoints, it would break ADR-0002's "never a third-party
  test server", and dyno's numbers would then depend on a service it doesn't
  control. Reopen if users ask for it and Cloudflare's terms allow it. If it
  ever ships, it is opt-in and off by default (ADR-0017).

## Why

Scheduled history, alerts and a non-Ookla engine are the three largest open
requests across the trackers (`docs/research/competitor-features.md` §5).
Every tracker but Netronome wraps the Ookla CLI, and speedtest-tracker
declined a second engine in August 2026. dyno owns its engine, so it can
answer all three.

## Consequences

- One-box users can't monitor their ISP until they run a second dyno. Pairing
  a second node must be close to one command (ADR-0018).
- A far end on someone else's line caps what it can measure at that line's
  speed. The owner's first far end is `tailmeadow`, at a relative's house
  (about 500 Mbit/s). Scheduled tests there default to the light, byte-capped
  test, a few times a day.
- Tests that cross a tailnet can run over a relay before a direct path forms,
  which measures the relay. Scheduled tests warm the path with latency probes
  first, and a result whose latency is far above that far end's baseline is
  flagged as a path change, not counted as an ISP slowdown.
