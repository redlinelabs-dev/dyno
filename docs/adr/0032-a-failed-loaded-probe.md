# ADR-0032: A failed loaded-latency probe ends the probing, not the test

Status: accepted 2026-11-14 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Raised by the clean-context review of the
server's test engine (finding U1S-4).

## Decision

- While a direction runs, a loaded-latency probe that fails (an error, or no
  answer within the ping timeout) stops that direction's probing. The
  direction itself carries on, and its speed counts.
- The direction's loaded latency is the median of the probes that finished
  after warm-up, as before. With none, it is 0, which the API already reads
  as "no loaded latency": no grade, no bufferbloat, no RPM.
- The browser (`web/src/engine.ts`) already works this way. The server's
  engine (`server/internal/nodetest`), which runs scheduled tests, now
  matches it.

## Why

- ADR-0010 makes the browser's engine the reference, and two engines that
  score the same link differently make scheduled and manual results
  incomparable.
- Throughput is the number people schedule tests for. A probe lost on a
  saturated link is a symptom of the load the test is causing, not a reason
  to throw away a valid speed reading.
- A far end that stops answering altogether still fails the test: the load
  streams fail too.

## Consequences

- A scheduled result can have a speed and no loaded latency. Its grade is
  empty, as for a browser result with no probes.
