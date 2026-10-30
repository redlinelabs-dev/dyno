# ADR-0004: Fast lane screen with Cluster's latency gauge

Status: accepted 2026-10-30 by the owner (was decisions-pending #1).
Spec: `prototypes/dyno-ui.html` (https://claude.ai/artifact/UWdk9sFSMyjry6XUvnFsFr).
The variations it came from are in
`prototypes/tach-variations.html` (#fast and #cluster).

## Context

Round 1 offered Tach, Quiet and Dyno sheet; the owner chose Tach: "I love
fast.com the best, and I also love that there's telemetry options but that
it's hidden by default. I want to make sure that things like latency are still
accessible". Round 2 offered four Tach variations. The owner's answer: "go with
fast lane plus the cluster latency gauge".

## Decision

**First screen (Fast lane).** The test starts when the page opens, the way
fast.com does. One log-scale ring with the download number in the middle, the
red zone starting at the plan speed, then two chips: latency (idle and busy)
and upload. Then "Test again" and a Telemetry link. Nothing else.

**Telemetry sheet.** A bottom sheet with tabs: Latency, Speed over time, Setup,
History. Tapping the latency chip or Telemetry opens the Latency tab; the
upload chip opens Speed over time.

**Latency tab (Cluster's gauge).** A linear 0–150 ms gauge with a red zone
from 100 ms. The needle is latency now. An orange marker holds the highest
latency while the line was busy, so bufferbloat reads as the gap between needle
and marker. Under it, the reading and "peak N ms while busy". Beside it on
desktop, below it on a phone: the bufferbloat grade and the latency table
(idle, while downloading, while uploading, jitter, server).

## Consequences

- The prototype's packet-loss row stays out until decisions-pending #5 is
  settled; it is not measured in v1 under the current recommendation.
- On phones the four tabs don't fit beside Close, so the tab row scrolls
  sideways. Setup and History start off-screen; give the row a visible hint
  that it scrolls, or shorter labels.
- Fast lane is dark only. Chrono was the only theme-aware variation; a light
  theme is not part of v1 unless the owner asks.
- The prototype's numbers are simulated. Real behaviour (what busy latency
  means, how the grade is computed) comes from ADR-0003, not from the
  prototype's engine.
