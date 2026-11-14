# ADR-0008: No login by default

Status: accepted 2026-10-30 by the owner (was decisions-pending #9): "I don't
need auth to start with."

## Decision

dyno is open to anyone who can reach it, the way LibreSpeed and
OpenSpeedTest are. There is no login, user model or token in v1. Exposure is
the network's job: run it on the LAN, or behind a VPN or a reverse proxy that
does the auth.

## Consequences

- Anyone who can reach dyno can run tests, read history and post results.
  On a LAN that's the point; on the internet it means anyone can read your
  results and use your bandwidth. The README says so.
- The recommendation it replaces (optional password, forward-auth header,
  OIDC, all free) is still the likely shape if auth is added. Nothing in the
  API rules it out: clients already send nothing but plain HTTP requests, so
  a bearer token or a forward-auth header can be added without changing the
  routes (ADR-0002).
