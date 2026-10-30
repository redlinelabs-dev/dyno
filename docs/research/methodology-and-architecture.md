# Speed test methodology and server architecture

Research for dyno, gathered 2026-10-30. The question is how the established speed tests measure, what a browser can and cannot measure at multi-gigabit LAN speeds, and what server shape that implies. Language choice is out of scope here. A separate benchmark covers it.

Where I read source code directly, the citation points at the file. Two primary sources were unreachable from this environment: ookla.com and speedtest.net refuse this agent's fetcher, and waveform.com sits behind a Cloudflare challenge. Claims about Ookla and Waveform are marked as secondary.

## 1. How the established tests measure

### Summary table

| Test | Transport | Streams | Throughput statistic | Latency statistic | Extras |
|---|---|---|---|---|---|
| fast.com (Netflix) | XHR range GETs to Netflix OCAs | 1 to 8, adaptive | Moving average; stops when stable | Unloaded: min over servers of p10. Loaded: min over servers of p75 | Upload and loaded latency behind "more info" |
| speed.cloudflare.com | Sequential fetch, Resource Timing | 1, sequential | p90 of per-request rates, requests over 10 ms only | p50; jitter = mean abs delta of consecutive samples | Loaded latency per direction, packet loss via TURN, AIM scores |
| M-Lab ndt7 | WebSocket over TLS, one TCP connection | 1 | Application goodput, cumulative | Server TCP_INFO MinRTT/RTT | BBR where available, server kernel stats |
| LibreSpeed | XHR, PHP or Go backend | 6 down, 3 up | Bytes since grace period / time, x1.06 overhead factor | Weighted ping series, Resource Timing where possible | Telemetry DB, multi-server |
| OpenSpeedTest | XHR against static nginx | 6 down, 6 up | Live rate, x1.04 overhead factor | 10 samples | Stress mode up to a year |
| Apple networkQuality / RPM draft | HTTP/2 over TLS 1.3, optional HTTP/3 | 1 up to 16, ramped | Goodput until stable | Trimmed mean (worst 5% dropped), expressed as RPM | Foreign vs self probes, `/.well-known/nq` |
| Ookla Speedtest (secondary) | Proprietary TCP, multi-server | Dynamic, multi-server | Not verified | Idle, download-loaded, upload-loaded since 2022 | Native apps for 10 Gb/s |

### fast.com

The live client bundle ([fast.com/app-0bffe1.js](https://fast.com/app-0bffe1.js), fetched 2026-10-30) is readable enough to extract parameters. It asks `api.fast.com/netflix/speedtest/v2` for 5 Netflix Open Connect URLs. It then downloads byte ranges with XMLHttpRequest, starting at 1 connection and allowing up to 8. Request sizes adapt so each range takes about 300 ms, growing in 200 ms steps up to 1 s, with at most 75 MiB in flight. The test runs 7 to 30 seconds and ends when the last 6 moving-average samples sit within the stability delta of 2.

Latency probes are one-byte `range/0-0` requests. Unloaded latency is the minimum across servers of each server's p10, loaded latency the minimum of each server's p75. Netflix framed the gap between them as the user-facing number ([Netflix, 2018](https://about.netflix.com/en/news/fast-com-now-measures-latency-and-upload-speed)). One headline number with everything else a click away is the progressive-disclosure model dyno wants.

### Cloudflare speed test

The engine is open source as [`@cloudflare/speedtest`](https://github.com/cloudflare/speedtest). It runs one request at a time through a fixed ladder: downloads of 100 kB to 250 MB and uploads of 100 kB to 50 MB. A direction stops climbing once a request takes 1000 ms. [`BandwidthEngine.ts`](https://github.com/cloudflare/speedtest/blob/main/src/engines/BandwidthEngine/BandwidthEngine.ts) times downloads from Resource Timing (TTFB plus body time, minus server time read from a `Server-Timing` header) and counts bits from `transferSize`. Upload duration is `requestStart` to `responseStart`. The server replies only after reading the whole body, so the timer stops on server receipt rather than on the browser's send buffer. The upload body is `'0'.repeat(n)`. Browsers do not compress request bodies, so this works, but dyno should not copy it.

[`MeasurementCalculations.ts`](https://github.com/cloudflare/speedtest/blob/main/src/Results/MeasurementCalculations.ts) reports bandwidth as the p90 of per-request rates, ignoring requests under 10 ms. Latency is the median. Jitter is the mean absolute difference between consecutive samples. Loaded latency comes from zero-byte side requests every 400 ms, kept only for sizes whose shortest request lasted 250 ms or more, and only the last 20.

AIM scores are in [`internalConfig.ts`](https://github.com/cloudflare/speedtest/blob/main/src/config/internalConfig.ts). Each metric maps to points by threshold, and each use case sums its inputs:

| Metric | Thresholds | Points |
|---|---|---|
| Packet loss | 1%, 5%, 25%, 50% | 10, 5, 0, -10, -20 |
| Idle latency, ms | 10, 20, 50, 100, 500 | 20, 10, 5, 0, -10, -20 |
| Loaded latency increase, ms | 10, 20, 50, 100, 500 | 20, 10, 5, 0, -10, -20 |
| Jitter, ms | 10, 20, 100, 500 | 10, 5, 0, -10, -20 |
| Download / upload | 1, 10, 50, 100 Mb/s | 0, 5, 10, 20, 30 |

Streaming uses latency, loss, download and loaded-latency increase, with class cut points 15/20/40/60 for bad/poor/average/good/great. Gaming uses latency, loss and loaded increase at 5/15/25/30. RTC adds jitter at 5/15/25/40. Upload is defined but not used by any use case. A missing loss measurement scores 0 points rather than being skipped. On a LAN every dyno result would hit the top of the bandwidth scale, so these thresholds suit WAN tests better than LAN tests.

Packet loss is opt-in. [`SelfWebRtcDataConnection.ts`](https://github.com/cloudflare/speedtest/blob/main/src/engines/PacketLossEngine/SelfWebRtcDataConnection.ts) creates two peer connections in the same page, forces relay-only candidates through TURN, and sends 100 packets over a data channel with `ordered: false, maxRetransmits: 0`. The browser talks to itself through the relay, so the server side is only a TURN server.

Cloudflare says the test estimates what you reliably get per transfer size rather than saturating the link ([Cloudflare blog](https://blog.cloudflare.com/how-does-cloudflares-speed-test-really-work/)). That answers "how will browsing feel", not "is my 10 GbE link healthy". Dyno needs both.

### M-Lab ndt7 and msak

The [ndt7 spec](https://github.com/m-lab/ndt-server/blob/main/spec/ndt7-protocol.md) is the cleanest public protocol. It uses one WebSocket per direction (`/ndt/v7/download`, `/ndt/v7/upload`). Binary messages start at 8 KiB and double while under 1/16 of bytes sent, up to 16 MiB. Tests run up to 10 s. About 10 times a second the server sends JSON with application bytes and kernel `TCPInfo`: BytesAcked, BytesRetrans, MinRTT, RTT, RTTVar, RWndLimited, SndBufLimited. The headline is application goodput. BBR is used where available. The idea worth taking is that the server holds ground truth in TCP_INFO and should report it. M-Lab's newer [msak](https://github.com/m-lab/msak) adds multi-stream throughput in the same style.

### LibreSpeed and OpenSpeedTest

[LibreSpeed's worker](https://github.com/librespeed/speedtest/blob/master/speedtest_worker.js) opens 6 download and 3 upload XHR streams, staggered by 300 ms. It discards the first 1.5 s of download ("wait for TCP window to increase") and 3 s of upload ("wait for buffers to fill"), then multiplies by 1.06 for overhead. Ping prefers Resource Timing and falls back to `Date` deltas. Its PHP backend ([`garbage.php`](https://github.com/librespeed/speedtest/blob/master/backend/garbage.php)) generates one 1 MiB random buffer and writes it 100 times. That buffer reuse is the pattern dyno should copy.

[OpenSpeedTest](https://github.com/openspeedtest/Speed-Test) is static files on nginx: 6 XHR streams each way for 12 s, 30 MB upload blobs, a 1.04 overhead factor. Its [nginx config](https://github.com/openspeedtest/Docker-Image/blob/main/files/OpenSpeedTest-Server.conf) reads as a checklist of gotchas: `gzip off`, `Cache-Control: no-store ... no-transform`, `sendfile on`, no access log. It also warns that under HTTP/2 or HTTP/3 nginx returns 200 without waiting for the POST body, giving "abnormal upload speed". The README recommends HTTP/1.1.

### Apple networkQuality and the IETF responsiveness draft

[draft-ietf-ippm-responsiveness-09](https://datatracker.ietf.org/doc/draft-ietf-ippm-responsiveness/) (6 July 2026, Standards Track) defines RPM as 60 / RTT in seconds. The client starts with 1 load connection and adds 1 every 5 s, up to 16, until the moving-average goodput over the last 4 intervals varies by under 5%. RPM then runs until it is stable by the same rule. Foreign probes open a new connection and time the TCP handshake, TLS handshake and a 1-byte GET. Self probes send a 1-byte GET on a load connection. Each series is reduced with a 95% one-sided trimmed mean, and final RPM averages foreign and loaded RPM. Results carry a low, medium or high confidence. Bands: poor at 300 RPM or less (about 200 ms), fair near 1000 (60 ms), good near 6000 (10 ms).

The server needs a 1-byte object, an object of at least 8 GB the client abandons, a POST sink, and a JSON config at `/.well-known/nq`. No content encoding, no redirects, TLS 1.3 over HTTP/2 ([SERVER_SPEC](https://github.com/network-quality/server/blob/main/SERVER_SPEC.md)). macOS ships the client as `networkQuality -C <config-url>` ([Apple](https://support.apple.com/en-us/101942)). If dyno exposes this config, every Mac can test against it with nothing installed.

### Ookla (secondary sources only)

Ookla's pages were unreachable. Secondary sources agree on three points. Speedtest scales connection count dynamically across several servers to reach 10 Gb/s. Since 2022 it reports idle, download-loaded and upload-loaded latency. A third-party port claims it keeps the fastest half of post-warmup samples. I could not verify any aggregation rule, and I would not design around one.

## 2. Browser constraints

### Transport choice

| API | Download | Upload | Notes |
|---|---|---|---|
| XHR | `onprogress` | `upload.onprogress` | Works everywhere. Every test that reaches multi-gigabit uses it. |
| fetch + ReadableStream | `response.body` reader | Streaming bodies need `duplex: 'half'` and HTTP/2 | Streaming request bodies: Chrome 105+, Firefox preview only, Safari none ([MDN BCD](https://github.com/mdn/browser-compat-data/blob/main/api/Request.json), [WebKit 245671](https://webkit.org/b/245671)). Chrome rejects them on HTTP/1.x ([Chrome docs](https://developer.chrome.com/docs/capabilities/web-apis/fetch-streaming-requests)). |
| WebSocket | Binary messages | Binary messages | Up to 255 sockets per host in Chrome. Client frames must be masked ([RFC 6455 §5.3](https://www.rfc-editor.org/rfc/rfc6455#section-5.3)), which costs CPU on upload. Lets the server push progress mid-upload. |
| WebTransport | Streams or datagrams over HTTP/3 | Same | Chrome 97, Firefox 114, Safari 26.4. Baseline since March 2026 ([MDN](https://developer.mozilla.org/en-US/docs/Web/API/WebTransport_API)). Secure context and an HTTP/3 server required. |

For uploads, a fetch with a `Blob` body works in every browser. The body does not need to stream if the client sends many medium-sized requests. Most of the field already works this way.

### Parallel streams and the per-host cap

Chromium allows 6 HTTP connections per host/port group and 255 WebSockets ([`client_socket_pool_manager.cc`](https://chromium.googlesource.com/chromium/src/+/main/net/socket/client_socket_pool_manager.cc)). A test with 6 HTTP/1.1 load streams leaves no connection for latency probes, so probes queue behind the load and loaded latency becomes garbage. Cap load at 5 per origin, or serve probes on a second port, which is a separate group.

HTTP/2 multiplexes every request to an origin over one TCP connection. Parallel fetches then share one congestion window and TCP head-of-line blocking, which defeats the point of parallel streams. Browsers only speak HTTP/2 over TLS, so plain-HTTP LAN deployments get HTTP/1.1 for free. Behind a TLS proxy, bulk endpoints should offer `http/1.1` only. HTTP/3 is worse for bulk: Zhang et al. measured it up to 45.2% slower than TCP+TLS+HTTP/2 in browsers on fast links, mostly from userspace packet and ACK processing ([arXiv 2310.09423](https://arxiv.org/abs/2310.09423)).

### CPU ceiling in the browser

Huber, Haeberle, Steinert and Menth tested LibreSpeed and OpenSpeedTest over a 100 Gb/s lab link where iperf3 measured 94.1 Gb/s ([Tübingen 2025](https://publikationen.uni-tuebingen.de/xmlui/handle/10900/163781)):

| Tool / browser | Download Gb/s | Upload Gb/s |
|---|---|---|
| OpenSpeedTest, Firefox 133 | 3.41 | 2.46 |
| OpenSpeedTest, Chrome 131 | 9.46 | 9.70 |
| LibreSpeed, Firefox 133 | 2.98 | 2.47 |
| LibreSpeed, Chrome 131 | 9.97 | 10.64 |
| LibreSpeed CLI, same server | 43.33 | 49.45 |

Firefox pegged every core. Chrome saturated a few and looked single-core bound. The authors conclude Firefox fails to saturate 2.5 Gb/s and Chrome cannot measure 10 Gb/s. LibreSpeed's Chrome upload beating its download is a hint that its 1.06 multiplier and progress-event counting inflate uploads.

So for dyno: 1 Gb/s works in any modern browser, 2.5 Gb/s needs Chromium, and 10 Gb/s needs the native client or a node-to-node test. The UI should flag client-bound results, for example when the rate plateaus while server TCP_INFO shows the client's receive window limiting. Count bytes in a Web Worker. Background tabs throttle timers, so abort when `document.hidden` becomes true.

### Resource Timing

Resource Timing gives network-stack timestamps free of JS scheduling delay. The buffer holds 250 entries by default ([W3C](https://w3c.github.io/resource-timing/)), so long tests must call `setResourceTimingBufferSize` or `clearResourceTimings`, as fast.com does. Cross-origin, `transferSize` is 0 and detailed timings are zeroed unless the response sends `Timing-Allow-Origin` ([MDN](https://developer.mozilla.org/en-US/docs/Web/API/PerformanceResourceTiming/transferSize)). That matters for multi-node. `transferSize` adds a fixed 300 bytes for headers rather than the real count. Waveform's developers reported in 2021 that iOS Safari's timing values were unusable ([bloat list](https://www.mail-archive.com/bloat@lists.bufferbloat.net/msg06423.html)). Recheck that on current Safari.

### Compression, caching, Service Workers

Payloads must be random bytes with `Content-Type: application/octet-stream`, `Cache-Control: no-store, no-transform` and no `Content-Encoding`. Add a cache-busting query parameter anyway, as all three open tests do. If the UI ever ships a Service Worker, scope it away from measurement paths, or its `fetch` handler touches every body.

### Upload accuracy

Upload progress events count bytes handed to the OS send buffer, not bytes delivered ([whatwg/xhr#361](https://github.com/whatwg/xhr/issues/361)). Send buffers hold megabytes, so the first few hundred milliseconds look absurdly fast. LibreSpeed hides it with a 3 s grace period, and Cloudflare times to the server's response. The robust answer is for the server to count. It timestamps bytes as it reads them and reports steady-state goodput, plus TCP_INFO as ndt7 does. Client progress events drive only the live gauge.

## 3. Latency, jitter, loss and bufferbloat

**Idle latency.** Send 10 to 20 sequential tiny GETs on a warm, idle keep-alive connection and take the median (fast.com uses p10 as a floor). Read RTT from Resource Timing `responseStart - requestStart`, falling back to `performance.now()` deltas. Report a fresh connection's TCP and TLS handshake times separately, like the RPM draft's foreign probe.

**Loaded latency.** Send the same probe every 100 to 400 ms during each load phase, on its own connection. Keep only samples from the saturated phase, as Cloudflare's 250 ms rule does. Report download-loaded and upload-loaded separately. Upload bloat is usually worse and has a different cause.

**Jitter.** Mean absolute difference of consecutive samples, Cloudflare's definition. Keep the raw samples so the advanced view can show p50, p90 and p99.

**Packet loss.** TCP hides loss from the browser, so measuring it needs an unreliable transport. There are three options:

1. A WebRTC data channel with `ordered: false, maxRetransmits: 0`, which is SCTP over DTLS over ICE/UDP ([RFC 8831](https://www.rfc-editor.org/rfc/rfc8831), [RFC 8261](https://www.rfc-editor.org/rfc/rfc8261)). Either run a TURN relay and copy Cloudflare's in-page loopback, or make the server an echoing peer. OpenPacketLoss does the latter with `webrtc-rs`, a built-in STUN server and a UDP port range ([OpenPacketLoss-Server](https://github.com/openpacketloss/OpenPacketLoss-Server)). ICE-lite ([RFC 8445 §2.5](https://www.rfc-editor.org/rfc/rfc8445#section-2.5)) spares a server with a known address from gathering candidates. It still means DTLS, SCTP, SDP signaling and UDP ports in compose. That would be the largest dependency in the project.
2. WebTransport datagrams: unreliable QUIC datagrams with no SCTP or SDP, now in all three engines. They need HTTP/3, a UDP port and a secure context. On a LAN with no trusted certificate, the client pins short-lived certificates via `serverCertificateHashes`.
3. TCP retransmits from server TCP_INFO. Free with ndt7-style reporting, but loss-based congestion control drops packets by design, so show this as a diagnostic and never label it "packet loss".

Ship v1 without loss and show retransmits in the advanced view. If loss arrives later, WebTransport is the lighter path. Cloudflare's AIM scores a missing loss value as 0 points, so a dyno port of AIM should drop it from the inputs instead.

**Bufferbloat grade.** Waveform's table was unreachable, and secondary sources disagree. One lists A under 30 ms of added latency, then 60, 200 and 400 ms. Others put A+ under 5 ms. Dyno should publish its own scale from citeable numbers. Grade the loaded-minus-idle increase on Cloudflare's thresholds (10/20/50/100/500 ms) and show RPM with the IETF bands beside it.

## 4. Deployment gotchas

**Reverse proxies.** nginx buffers responses and request bodies by default ([nginx](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)). With request buffering on, nginx swallows the whole upload before dyno sees a byte, so server-side counting would time the proxy's loopback. Dyno should send `X-Accel-Buffering: no` and document `proxy_request_buffering off`. Caddy partially buffers responses until `flush_interval -1`, and request buffering is opt-in ([Caddy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)). Traefik buffers only with the `buffering` middleware ([Traefik](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/buffering/)). All three negotiate HTTP/2 with browsers over TLS, so the h2 problem applies to any proxied setup. Recommend exposing dyno's port directly on the LAN.

**Cloudflare Tunnel.** Free and Pro plans cap request bodies at 100 MB ([limits](https://developers.cloudflare.com/workers/platform/limits/)). The self-serve terms bar serving "a disproportionate percentage of ... large files" ([terms](https://www.cloudflare.com/service-specific-terms-application-services/)). The test would also measure the path to Cloudflare's edge rather than to dyno. Document tunnelled tests as unsupported.

**TLS at 10 Gb/s.** Userspace TLS breaks `sendfile`, because bytes must pass through the process for encryption. Linux kTLS moves record encryption into the kernel so `sendfile()` emits TLS records directly ([kernel docs](https://docs.kernel.org/networking/tls.html)). Netflix moved TLS into the FreeBSD kernel for the same reason. Gallatin's slides report about 90 Gb/s of TLS at 68% CPU in software ([EuroBSDCon 2019](https://people.freebsd.org/~gallatin/talks/euro2019-ktls.pdf); the PDF would not load here, so these figures come via the FreeBSD Foundation's summary). Plain HTTP on a LAN avoids the cost. Make TLS optional and measure it in the runtime benchmark.

**Docker networking.** Host networking skips NAT and the per-port userland proxy ([Docker](https://docs.docker.com/engine/network/drivers/host/)). It works on Linux and on Docker Desktop 4.34+ when enabled. Bridge adds veth plus NAT, and the userland proxy is still the default for some IPv6-to-IPv4 mappings ([port publishing](https://docs.docker.com/engine/network/port-publishing/)). Default compose to `network_mode: host` and benchmark bridge as the fallback.

**MTU.** The bridge MTU option defaults to 0, meaning unset ([Docker bridge](https://docs.docker.com/engine/network/drivers/bridge/)). A 1500-byte bridge on a 9000-byte jumbo-frame host means six times the packets at 10 Gb/s. Report the interface MTU in `/config` so the advanced view can flag mismatches.

## 5. Server architecture recommendation

### API

HTTP first. It is what browsers, URLSession and OkHttp all speak, it survives proxies, and it is what the RPM draft and Apple's client expect. One test session ties requests together through a header, so the server can aggregate across streams.

| Method and path | Purpose |
|---|---|
| `GET /api/v1/config` | Server id, version, node name, interface speed and MTU, limits (max streams, max bytes), capabilities (ws, tcpinfo, loss), peer nodes |
| `GET /.well-known/nq` | RPM-draft config pointing at the endpoints below, for `networkQuality` |
| `POST /api/v1/sessions` | Start a test session. Returns id and the recommended plan (streams, durations). Bounds concurrency: 429 with `Retry-After` when busy, per the nq spec. |
| `GET /api/v1/down?bytes=N&s=<session>` | Random bytes from a shared buffer. `bytes` is capped. Omitting it streams until the client disconnects, which covers the 8 GB "large" object. |
| `POST /api/v1/up?s=<session>` | Discard the body into a fixed scratch buffer. Count bytes with server timestamps. Reply after the full read with `{bytes, first_byte_us, last_byte_us, tcp_info}` and `Server-Timing`. |
| `GET /api/v1/ping` | 204, or 1 byte for the nq "small" object. `Server-Timing` set. |
| `GET /api/v1/sessions/{id}` | Server-side view: per-100 ms byte buckets per direction and TCP_INFO snapshots. The client polls it for live upload progress and uses it for the final upload figure. |
| `POST /api/v1/results`, `GET /api/v1/results`, `GET /api/v1/results/{id}` | Store and list results |
| `GET/PUT /api/v1/schedules` | Server-side scheduled tests |

Every measurement response carries `Cache-Control: no-store, no-transform`, `Content-Type: application/octet-stream`, `Timing-Allow-Origin`, CORS for configured UI origins, and `X-Accel-Buffering: no`.

**WebSocket.** Not needed for v1. It does two things better: live upload progress pushed from the server, and higher per-host connection limits. Polling `/sessions/{id}` at 4 to 10 Hz covers the first. Five HTTP/1.1 streams plus a second port cover the second up to the browser's CPU ceiling. Client-side masking also makes WebSocket uploads cost more CPU in an already CPU-bound browser. An ndt7-compatible endpoint is worth adding later so existing ndt7 clients can test against dyno.

### Zero-copy and flat memory

- At startup, fill one 4 to 16 MiB buffer from a CSPRNG and never write it again. Every download writes slices of it. The bytes need to be incompressible, not fresh per request. Make the buffer larger than any compression window or WAN-optimizer dedup block.
- For plain HTTP on Linux, zero-copy means `sendfile()` from a memfd holding that buffer, or `splice` from a pipe. `sendfile` still works with kTLS. `MSG_ZEROCOPY` pays off only above about 10 KB per write, and loopback always forces a deferred copy, so same-host tests will not show its benefit ([kernel docs](https://docs.kernel.org/networking/msg_zerocopy.html)). Large plain `write()` calls may be enough. Let the benchmark decide.
- Uploads read into a fixed scratch buffer per connection, never into a growing body.
- Per-session state is fixed size: a ring of byte buckets (30 s at 10 Hz is 300 counters per direction) and a capped list of TCP_INFO snapshots. With hard caps on sessions and connections, peak memory is a constant: buffer + connections x scratch + sessions x ring.
- Put timeouts on every phase, a byte ceiling on every request, and an idle timeout on keep-alives. Turn access logs off on measurement paths.

### Native clients

Native code gets explicit connection counts and no JS overhead. On iOS, `httpMaximumConnectionsPerHost` sets parallelism and `URLSessionTaskMetrics` gives per-request timing. On Android, OkHttp's dispatcher caps requests per host at 5 by default, so the client must raise it. Neither needs anything beyond the endpoints above. That is why the API should stay plain HTTP, JSON and raw bytes, authenticate with bearer tokens instead of cookies, and have the server issue the test plan. One plan then drives every client.

### Results, history, schedules

Use SQLite in WAL mode: one file on a volume, readers and writers in parallel, a small page cache. WAL needs a local filesystem, not NFS or SMB ([SQLite](https://www.sqlite.org/wal.html)), which matters on a NAS. One `results` table holds indexed summary columns (time, node, client, down, up, idle and loaded latency, jitter, RPM) and a compressed blob of raw samples. Retention prunes blobs after N days and keeps summaries. Append-only JSONL is the fallback if the runtime lacks a good SQLite binding, at the cost of full scans for history.

The server cannot measure a browser's link by itself, so a scheduled test makes the server the client. It runs the native test against a peer node, or optionally a public ndt7 server, and stores the result with `source = "schedule"`.

### Multi-node

Every node runs the same binary. A role setting decides whether it also serves the UI and stores results. The home node's `/config` lists peers. The browser tests each peer directly, which needs CORS and `Timing-Allow-Origin` everywhere, and posts results home. Peers push scheduled results home with a shared token. Node-to-node tests are the only way dyno can check a 10 GbE link between servers.

## 6. Decisions this implies

1. Use HTTP/1.1 for bulk transfer, with 5 load streams per origin and latency probes on a separate connection or port. Keep HTTP/2 and HTTP/3 off the measurement paths.
2. Use XHR, or fetch with Blob bodies, for transfers. Skip fetch upload streaming, since Safari lacks it and Chrome requires HTTP/2.
3. Measure upload on the server. Client progress events only drive the gauge.
4. Expose server TCP_INFO per session and show it in the advanced view.
5. Promise accurate results up to 1 Gb/s in any browser and about 2.5 Gb/s in Chromium. Detect and label client-bound results. Leave 10 Gb/s to the native client and node-to-node tests.
6. Serve downloads from one pre-generated random buffer, read uploads into fixed scratch buffers, use fixed-size session state and hard concurrency caps.
7. Implement the RPM draft's `/.well-known/nq` so macOS `networkQuality` works against dyno without extra code.
8. Make the headline down, up, idle latency and loaded-latency increase. Put per-direction loaded latency, jitter, percentiles, RPM, TCP retransmits and per-stream data behind disclosure.
9. Grade bufferbloat on loaded-minus-idle latency with Cloudflare's published thresholds and the IETF RPM bands, not Waveform's unverifiable table.
10. Leave packet loss out of v1. When it arrives, prefer WebTransport datagrams over WebRTC.
11. Default the compose file to host networking, plain HTTP on the LAN and optional TLS. Document proxy buffering settings and say that Cloudflare-tunnelled tests are unsupported.
12. Store results in SQLite with WAL on a local volume. Server-side schedules run node-to-node.
13. Keep the API plain HTTP plus JSON with a server-issued test plan, so web, iOS and Android clients drive the same endpoints.

## Notes on sources

- No fetched content contained instructions aimed at the agent. One search result set, on the Netflix kTLS work, was flagged for hidden zero-width or bidirectional characters. Nothing in it was acted on, and none of its claims are used here except the Netflix kTLS figures, which are attributed to Gallatin's slides.
- The fast.com parameters come from a minified production bundle and may change without notice.
- The Ookla and Waveform sections rely on secondary sources because the primary sites refused access.
