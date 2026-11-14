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
  buckets. The result is the rate over the whole buckets after warmup, empty
  ones included: amended 2026-11-01, when buckets after the last byte were
  dropped, so a stall at the end read as the speed before it (93 Mbps
  reported against 42.5 delivered). The live gauge shows the last 0.5 s.
- **Upload** is sent with XHR and a `Blob` body, sliced from one random 1 MiB
  block repeated up to the plan's 32 MiB cap. Everything shown or stored
  comes from the server's replies, with each request's bytes taken as spread
  evenly from opening it to its reply. The count is complete up to the
  earliest of the streams' last replies. The result is all streams' bytes from
  the end of warmup to that point, over that one window. If the window is
  under a second, or a request in it lasted over half of it, the result is
  every byte over the whole span instead, which reads low, not high. Even
  when spreading holds, streams whose shares shift within a request can move
  a few percent of bytes across a window edge, either way. The chart follows
  the result's choice: the last second of the server's count when the result
  spreads bytes, otherwise every byte confirmed so far over the time since the
  first request, so it never shows more than the result's method allows. Its
  Peak counts only samples after warmup. The live gauge shows the last second
  of the count and trails real time by about a request. Each request is sized to take `target_request_ms` at the
  previous one's rate (64 KiB to 32 MiB), starting from the plan's 256 KiB.
  If no request finished at all, the browser's progress count is used and the
  result's detail says `upload_from_client`.

  Amended twice. 2026-10-31: the first version divided by the server's
  `elapsed_us`, which skips the gaps between requests and the bytes buffered
  before the headers are read; on a gigabit link it reported 1,234 Mbps from
  1.69 GB in 15 s. 2026-11-01: the second added per-stream rates timed over
  different windows and fell back to warmup requests when 1 MiB requests
  outlasted the window, reading 22.5 to 27.7 on a 20 Mbit/s uplink and 6.1 on
  5. Its chart and gauge used XHR progress events, which count bytes handed to
  the socket, not sent, and peaked at 3,355 on a 500 Mbit/s link. Through a
  shaping proxy the current method reads 19.8 and 20.0 on 20, 4.9 on 5 and
  499.7 on 500 (Standard), and 2 to 8% low on Quick, never above the link.
- **A failed stream fails the test.** If any load stream errors (the server
  went away, a proxy answered 502, the server timed out a stalled upload), the
  direction stops and the page shows which part lost the server. No result is
  shown, graded or stored. Amended 2026-11-01: failed streams were ignored,
  so a server killed mid-test still produced an A+ result. A hung latency
  probe no longer holds the error back: probes stop with the load streams, and
  idle-latency probes give up after 10 s.
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
