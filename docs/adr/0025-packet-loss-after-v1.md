# ADR-0025: Jitter in v1, packet loss later over WebTransport

Status: accepted 2026-11-13 by the build session, on the owner's delegation of
non-UI calls: "take my UX decisions and user stories to formulate
recommendations decision wise, and then implement those recommendations you
have made yourself". Was decisions-pending #5.

## Decision

- v1 measures jitter (it already does) and not packet loss.
- Packet loss arrives with the stability mode (M5), over WebTransport
  datagrams.
- WebRTC stays out.

## Why

- The research disagreed. `market-and-demand.md` put loss in the MVP, the way
  Cloudflare does it, over WebRTC. `methodology-and-architecture.md` said
  WebRTC means ICE, DTLS and SCTP on the server: a lot of code and footprint
  for one number. WebTransport datagrams give the same unreliable delivery
  for less.
- Demand is real but small next to monitoring: about 14 signal against about
  97 for scheduled history (`competitor-features.md` §5). The stories it
  serves (37 and 38 in `docs/user-stories.md`) are the stability ones.

## Consequences

- The prototype's packet-loss row stays out of the Latency tab until M5
  (ADR-0004 already says so).
- M5 needs HTTP/3 on the server for WebTransport, on its own path. It must not
  touch the HTTP/1.1 measurement paths (ADR-0003), and it must stay within
  ADR-0006's budgets. If it can't, that's a new decision, not a quiet
  exception.
