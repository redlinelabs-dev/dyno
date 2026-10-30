# ADR-0003: How dyno measures

Status: accepted 2026-10-30.
Evidence: `docs/research/methodology-and-architecture.md` (§6 lists the reasoning).

## Decision

1. Bulk transfer uses HTTP/1.1 with up to 5 load streams per origin. That
   leaves one of Chrome's six per-host connections for latency probes. HTTP/2
   and HTTP/3 stay off the measurement paths.
2. Browser transfers use XHR or fetch with Blob bodies. Fetch upload streaming
   is out: Safari lacks it, and Chrome needs HTTP/2 for it.
3. Upload speed is counted from bytes the server received. Client progress
   events only move the gauge.
4. Payloads are incompressible, compression is off and responses are uncached.
5. The headline is download, upload, idle latency and the loaded-latency
   increase. Per-direction loaded latency, jitter, percentiles, RPM, TCP
   retransmits (from server TCP_INFO) and per-stream data sit behind disclosure.
6. Bufferbloat is graded on loaded minus idle latency, using Cloudflare's
   published thresholds and the IETF RPM bands.
7. Accuracy promise: 1 Gb/s in any browser, about 2.5 Gb/s in Chromium. The UI
   detects and labels a result limited by the client's CPU. 10 Gb/s is for a
   native client and node-to-node tests.
8. A self-check warns when compression, a CDN or tunnel, a small proxy body
   limit, or client CPU saturation could make the result wrong. It also
   flags loaded latency as unreliable when the server itself was CPU-bound
   during the test (ADR-0006).
9. The compose file defaults to host networking and plain HTTP on the LAN,
   with optional TLS. The docs cover reverse-proxy buffering settings and say
   that tests through a Cloudflare tunnel are unsupported.

Packet loss is open (decisions-pending #5).
