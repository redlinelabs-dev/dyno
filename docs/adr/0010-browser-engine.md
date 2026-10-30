# ADR-0010: How the browser runs a test

Status: accepted 2026-10-30 while building M2. Applies ADR-0003 to the web UI;
the next session would otherwise re-argue these.

## Decision

- **A Web Worker runs the test** (`web/src/engine.ts`), so rendering the dial
  never delays reading a stream and vice versa. The page stops a test by
  terminating the worker.
- **Order:** one warm-up ping, then `idle_samples` pings back to back, then
  download, then upload. Each direction runs for its `duration_ms` times the
  test length (Quick 0.4, Standard 1, Thorough 1.5). Warmup is
  `min(warmup_ms, 0.4 × duration)`, so Quick still has a steady-state window.
- **Streams:** Auto uses the plan's count. A user choice is capped at it.
- **Download** is counted in the browser from `fetch` body chunks into 100 ms
  buckets. The result is the rate over the whole buckets after warmup. The
  live gauge shows the last 0.5 s.
- **Upload** is sent with XHR and a `Blob` body, sliced from one random 1 MiB
  block repeated up to the plan's 32 MiB cap. Progress events only drive the
  gauge. The result comes from the server's replies: each stream's bytes over
  the server's `elapsed_us`, for requests that started after warmup, summed
  over streams. A stream with no finished request after warmup falls back to
  its warmup requests. If no request finished at all, the browser's count is
  used and the result's detail says `upload_from_client`.
- **Latency probes** use Resource Timing's `responseStart − requestStart`,
  which leaves out the worker's own scheduling delay. They fall back to wall
  time when the browser zeroes those fields. Loaded probes run every
  `loaded_interval_ms` on the sixth connection. Only probes after warmup
  count toward a direction's loaded latency (their median).
- **Client-CPU label** (ADR-0003 rule 7): the worker runs a 50 ms timer
  through both transfers. If its median lateness is over 20 ms, the device
  couldn't keep up and the result is marked `client_limited`. The UI says so
  under the result.
- **The server grades.** The client posts its numbers and the server derives
  bufferbloat (worse loaded direction minus idle), RPM (60000 / that loaded
  latency) and the grade on Cloudflare's thresholds: under 10 ms A+, 20 A,
  50 B, 100 C, 500 D, else F. The prototype's thresholds were placeholders.
  Grading on the server keeps the web UI and a future app from disagreeing.
- **Every result carries the server's byte counts.** The plan hands out a
  `test_id`. Transfers tag their requests with `?t=`, and the server tallies
  the bytes it moved for that id and stores the totals with the result.
- **A test starts only while the page is visible and stops if it's hidden.**
  Browsers throttle background tabs, so a hidden-tab result would be wrong.

## Consequences

- The UI differs from the prototype in four places. It has no plan-upload
  field, because only the download red zone used one. Auto doesn't probe for
  a stream count; it uses the plan's 5. The grade thresholds are Cloudflare's.
  The Latency tab has no packet-loss row, since nothing measures loss yet.
- The rest of the ADR-0003 self-check (compression, CDN or tunnel, proxy body
  limits, server CPU) isn't built yet.
- The fixed 1 MiB repeating upload block is safe because browsers never
  compress request bodies. A native client must not send it through a
  compressing proxy.
