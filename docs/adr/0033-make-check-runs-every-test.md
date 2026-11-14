# ADR-0033: `make check` runs every Go test, slow ones included

Status: accepted 2026-11-14 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Raised when M3's tests took the engine package
to about two minutes under `-race`.

## Decision

- `make check` keeps running `go test -race ./...` with no `-short`. Every
  test runs before every commit.
- `-short` stays available by hand (`cd server && go test -short ./...`) for
  a quick loop while working. Tests that skip under it say how long they take.
- A test that can only pass with a long wait is a bug in the test: bound it
  by the event it waits for, not by wall-clock time, and keep seeds small
  enough that a run under `-race` takes seconds.

## Why

- The slow tests are the ones that pin timing and shutdown: a check, a
  prune or a run cut off when the server stops, probes one at a time, caps on
  a far end's plan. They are where M3's review rounds found real defects, so
  skipping them is skipping the part most likely to break.
- Go runs packages in parallel, so the check takes about as long as the
  slowest package (the engine, about two minutes under `-race`), not the sum.
- A solo developer has no CI-only suite to catch what a local `-short` would
  skip; CI runs the same `make check`.

## Consequences

- `make check` takes about five minutes with the footprint check.
- Under parallel load some timing bounds have been widened (N10, H5 to H7);
  each widened bound says in a comment what wrong behaviour it still catches.
