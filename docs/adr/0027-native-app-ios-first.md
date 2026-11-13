# ADR-0027: A native app comes after the web UI ships, iOS first

Status: accepted 2026-11-13 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Was decisions-pending #10. How the app looks and
behaves stays the owner's call, through prototypes, when its time comes.

## Decision

- No native app before M4 (public release).
- The first one is iOS, because the owner's Mac mini can build and sign it.
  Android follows if people ask.
- It uses only the public API (ADR-0002), the same one the web UI uses. Any
  endpoint it needs goes into `docs/api.md` for every client.

## Why

- The brief asks that the architecture allow an app "if I so desire", not
  that one ship. Monitoring (M3) and the public release (M4) answer bigger
  requests first (`competitor-features.md` §5).
- The cost of waiting is low. The web UI already uses only the public API, so
  nothing built now has to be undone for an app.

## Consequences

- Every API change keeps a native client in mind: no browser-only state, no
  UI-only endpoints.
- `docs/user-stories.md` story 42 tracks it.
