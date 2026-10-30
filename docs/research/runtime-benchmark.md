# Server runtime benchmark

Date: 2026-10-30. Question: which runtime should the dyno server use, given that the owner
wants the smallest memory and CPU footprint, a server that stays up, and no leaks?

The same small HTTP/1.1 server was written for Go, Rust, Zig and Bun and measured on one
machine with a purpose-built load generator. Sources, the load generator and the runner are
in `bench/` (see `bench/README.md` to rerun). Raw measurements are in `bench/results/*.jsonl`.

## Short answer

The owner chose Go on 2026-10-30 (ADR-0006): the gap below is small on a Docker
host and Go is easier to maintain. The recommendation as written follows.

**Use Rust: hyper 1.x on a current-thread tokio runtime** (axum on the same runtime is
fine if the routing gets bigger). Here is how it measured against the owner's criteria:

- **Memory:** 3.7 MiB idle RSS and 4.3 MiB after load.
- **Binary:** 0.69 MiB static arm64 build.
- **CPU at a real gigabit:** 2.6 % of a core for download and 2.5 % for upload, the lowest
  of all, both measured with the client paced to 1 Gbit/s.
- **Latency:** tied with Bun for the best ping under saturation, at 65 µs p99.
- **Stability:** a flat 10-minute soak (−4.7 KiB/min), zero errors, and all 1000 stalled
  connections closed by its header timeout.

**Go `net/http` is a close second.** It also passed every stability test. It costs about
7 MiB more RSS, a 5 MiB binary and 1–2 points more CPU at 1 Gbit/s. In return it has
simpler code, SQLite and cross-compiling with no C toolchain, and TLS in std. Pick Go if
development speed matters more than the last few MiB.

**Bun works only with care.** Its idiomatic-looking direct stream buffered entire
responses in memory, and even the fixed version spikes to 60–170 MiB during uploads. Its
idle RSS is 20 MiB and its binary 78 MiB.

**Zig is dropped.** `std.http` has no timeouts, needs a thread per connection, and
reached 584 MiB on 1000 stalled sockets without releasing it.

## Method

### Machine and versions

| item | value |
|---|---|
| host | Debian 13 VM, kernel 6.12.111+deb13-cloud-amd64, 10 vCPU AMD Ryzen 7 5825U, 22 GB RAM, headless |
| Go | go1.27.3 linux/amd64, `CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"` |
| Rust | rustc 1.99.1, hyper 1.12.0, hyper-util 0.1.21, tokio 1.54.1; release profile with `lto = true`, `codegen-units = 1`, `panic = "abort"`, `strip = true` |
| Zig | 0.17.0, `zig build -Drelease=true` (ReleaseFast), std only |
| Bun | 1.4.2, `bun build --compile --minify --bytecode` |

Go, Zig and the Rust toolchain were installed user-locally (`~/.local/go`, `~/.local/zig`,
rustup with `--profile minimal`). C was skipped for time, and Node was not needed as a
reference point.

### The server

Every implementation embeds `bench/static/index.html` (1008 bytes) into its binary and
serves, on `127.0.0.1:$PORT`:

- `GET /dl?bytes=N`: N bytes taken from a 1 MiB random buffer generated once at startup, written in 256 KiB slices of that buffer, so no payload is allocated per request. `Content-Length` is set (Bun sends chunked, see findings).
- `POST /ul`: reads the body in roughly 64 KiB reads, discards it, and returns the byte count.
- `GET /ping`: 204, no body.
- `GET /`: the embedded page.

Each one uses its standard library or the most common minimal library:

- **go**: `net/http`, `http.ServeMux` with method patterns, `ReadHeaderTimeout` 10 s, `IdleTimeout` 60 s. Default `GOMAXPROCS` (4 when pinned to 4 cores).
- **rust-mt / rust-ct**: hyper 1.x `server::conn::http1` on tokio with `TCP_NODELAY` and a 10 s header read timeout. The response body is a hand-written `http_body::Body` that hands out `Bytes::from_static` slices of the buffer. `RT=mt` is the default multi-thread runtime (4 workers when pinned to 4 cores). `RT=ct` is `new_current_thread`: one thread does everything.
- **zig**: `std.http.Server` on the default `std.Io.Threaded` from `std.process.Init`. One `Io.Group.concurrent` task per connection, which works out to one OS thread per connection (see findings for why not `group.async`).
- **bun**: `Bun.serve` with `routes`. The download is a pull-based `ReadableStream` with `highWaterMark: 0`, and the upload is consumed with `for await` over `req.body`.

### Load generator and measurements

`bench/loadgen` is a Go, std-only client. It writes requests on raw keep-alive TCP
connections and parses responses with `http.ReadResponse`, reading into 256 KiB buffers, so
the client stays cheap. It also reads the server's `/proc/<pid>/stat` and `/proc/<pid>/status`.

- **Pinning**: the server runs on cores 0-3 with `taskset`, which is roughly a Raspberry Pi's 4 cores. The load generator runs on 4-9. For ping under load, the 8 background download streams run on 6-9 and the ping client alone on 4-5.
- **Throughput and CPU**: 2 s warmup, then a 6 s window. Server CPU is `utime+stime` across the window. "cpu-s/GB" is server CPU-seconds per 10^9 bytes moved. Requests are 32 MiB each, back to back on keep-alive connections, at 1, 8 and 32 concurrent connections.
- **Paced 1 Gbit/s**: the same test, but the client holds the total across 8 streams to 1 Gbit/s over a 10 s window. The result is the server CPU % needed to fill a gigabit link, the case a Pi on gigabit Ethernet actually sees. 100 % = one core.
- **Ping**: 5000 sequential `GET /ping` on one keep-alive connection after 200 warmup requests. This was measured with the server idle, with 8 unpaced download streams (all 4 server cores saturated), and with 8 streams paced to 1 Gbit/s.
- **Memory**: `VmRSS`. Peak is `VmHWM`, reset through `/proc/<pid>/clear_refs` at the start of each window.
- **Cold start**: exec to the first successful `/ping`, polled every 0.2 ms, 20 runs.
- **Idle RSS**: measured 1 s and 60 s after startup.
- **Churn**: 10,000 connections opened, 16 at a time, each doing one `GET /ping` and then closing.
- **Soak**: 10 minutes of rate-limited mixed traffic against each server. The servers ran at the same time, each on its own cores: go 0-1, rust-mt 2-3, rust-ct 4, zig 5, bun 6, with clients on 7-9. The traffic was:
  - 2× keep-alive 4 MiB download loops and 2× upload loops at 10/s each
  - 100 pings/s
  - 50 short connections/s
  - 10 index fetches/s
  - 2/s each of malformed requests, downloads abandoned after 128 KiB, uploads abandoned after 1 MiB of a declared 256 MiB, and connections that send nothing

  RSS was sampled every 5 s, and the slope comes from a least-squares fit, both over all samples and over the samples after the first 2 minutes.
- **Stalled connections**: 1000 connections per server that send half a request header and stop. The test records RSS and threads while they are held, how many the server has closed after 70 s, and whether a new request is still served.

Perf runs were repeated 3 times, each with a freshly started server, with the server order
interleaved within each rep. Tables show the median and the min–max range.

Some runs happened after the main pass:

- **Bun.** Its rows come from a second pass, after the direct-stream problem described
  under findings was fixed, so its reps ran after the others rather than interleaved
  with them.
- **Ping with 8 streams paced to 1 Gbit/s.** This test was added after the main pass and
  run with `bun run.ts latency`, 3 reps for every server. Bun has 6 samples for it,
  because its perf rerun included the test too.

Commands: `cd bench && bun run.ts all`, or run the phases one at a time (`build`, `xbuild`,
`footprint`, `perf`, `latency`, `soak`, `hold`, `summary`). The tables below are the output
of `bun run.ts summary`, lightly trimmed.

## Results

All servers: 0 errors in every throughput, ping and churn run.

### Footprint

| server | binary MiB | cold start ms (median of 20) | idle RSS 1 s MiB | idle RSS 60 s MiB | idle threads |
|---|---|---|---|---|---|
| go | 5.57 | 6.6 (5.2–6.9) | 10.8 | 10.8 | 6 |
| rust-mt | 0.65 | 6.4 (6.0–7.8) | 4.0 | 4.0 | 5 |
| rust-ct | 0.65 | 6.4 (5.2–7.0) | 3.7 | 3.7 | 1 |
| zig | 0.27 | 4.3 (4.0–8.1) | 3.7 | 3.7 | 2 |
| bun | 77.56 | 7.7 (6.9–10.3) | 20.5 | 20.7 | 2 |

### Download: Gbit/s, server CPU-seconds per GB (median, min–max of reps)

| server | c1 Gbit/s | c1 cpu-s/GB | c8 Gbit/s | c8 cpu-s/GB | c32 Gbit/s | c32 cpu-s/GB | paced 1 Gbit/s: server CPU % |
|---|---|---|---|---|---|---|---|
| go | 32.3 (32.3–33.6) | 0.264 (0.255–0.266) | 88.9 (87.2–90.0) | 0.352 (0.348–0.356) | 90.7 (90.4–93.9) | 0.340 (0.333–0.344) | 3.6 (3.4–3.6) |
| rust-mt | 37.3 (37.2–39.0) | 0.214 (0.205–0.214) | 98.3 (96.2–106.5) | 0.325 (0.300–0.332) | 97.5 (93.7–98.6) | 0.328 (0.324–0.337) | 3.2 (3.2–3.2) |
| rust-ct | 37.1 (36.9–37.3) | 0.215 (0.214–0.216) | 37.0 (35.6–37.4) | 0.216 (0.213–0.225) | 37.1 (36.9–40.5) | 0.216 (0.198–0.217) | 2.6 (2.5–2.6) |
| zig | 34.8 (34.1–34.8) | 0.229 (0.229–0.234) | 93.8 (93.5–93.8) | 0.341 (0.341–0.341) | 93.3 (90.8–93.5) | 0.343 (0.337–0.352) | 3.5 (3.4–3.6) |
| bun | 25.8 (23.2–26.1) | 0.310 (0.306–0.345) | 23.8 (23.7–25.1) | 0.336 (0.318–0.337) | 24.0 (23.8–24.1) | 0.334 (0.331–0.336) | 3.8 (3.6–3.8) |

### Upload: Gbit/s, server CPU-seconds per GB (median, min–max of reps)

| server | c1 Gbit/s | c1 cpu-s/GB | c8 Gbit/s | c8 cpu-s/GB | c32 Gbit/s | c32 cpu-s/GB | paced 1 Gbit/s: server CPU % |
|---|---|---|---|---|---|---|---|
| go | 34.8 (34.3–34.9) | 0.191 (0.191–0.191) | 123.6 (110.9–129.9) | 0.182 (0.182–0.201) | 101.0 (93.3–101.3) | 0.236 (0.222–0.246) | 4.6 (4.5–4.7) |
| rust-mt | 36.1 (35.5–37.9) | 0.246 (0.235–0.248) | 116.0 (112.5–119.3) | 0.231 (0.222–0.235) | 60.5 (60.0–61.2) | 0.471 (0.443–0.488) | 5.1 (4.9–5.4) |
| rust-ct | 35.3 (35.1–35.6) | 0.146 (0.145–0.148) | 50.1 (49.6–52.2) | 0.158 (0.152–0.159) | 33.4 (31.1–33.5) | 0.240 (0.239–0.257) | 2.5 (2.4–2.5) |
| zig | 38.9 (38.6–40.5) | 0.116 (0.116–0.118) | 127.4 (122.1–133.2) | 0.181 (0.177–0.182) | 110.6 (109.6–118.7) | 0.186 (0.185–0.186) | 3.7 (3.4–3.7) |
| bun | 31.3 (31.1–35.1) | 0.290 (0.256–0.291) | 28.6 (26.7–28.8) | 0.304 (0.302–0.325) | 21.9 (21.5–22.4) | 0.396 (0.387–0.406) | 8.7 (8.6–9.1) |

### Ping latency µs (median of reps)

| server | idle p50 | idle p99 | 8 dl streams flat out p50 | 8 dl streams flat out p99 | 8 dl streams at 1 Gbit/s p50 | 8 dl streams at 1 Gbit/s p99 |
|---|---|---|---|---|---|---|
| go | 65 (65–69) | 120 (113–122) | 80 (76–85) | 15421 (15364–15653) | 66 (66–68) | 117 (115–128) |
| rust-mt | 46 (45–46) | 77 (65–79) | 46 (46–47) | 17027 (16769–17779) | 46 (46–52) | 73 (68–106) |
| rust-ct | 46 (46–47) | 77 (72–86) | 45 (45–46) | 65 (64–70) | 45 (45–46) | 73 (66–78) |
| zig | 39 (39–40) | 65 (59–72) | 82 (82–87) | 4191 (4065–4483) | 38 (38–40) | 60 (59–68) |
| bun | 45 (45–46) | 76 (71–77) | 45 (45–48) | 65 (63–75) | 46 (44–47) | 77 (68–87) |

### Memory under load (MiB, median of reps)

| server | start RSS | peak RSS (any test) | RSS 5 s after load | threads under load | 10k churn conn/s | churn errors | RSS after churn | threads after churn |
|---|---|---|---|---|---|---|---|---|
| go | 8.8 (8.8–10.8) | 18.6 (18.5–20.6) | 14.5 (14.4–15.2) | 21 (20–23) | 20155 (19673–20308) | 0 (0–0) | 14.6 (14.4–15.4) | 21 (20–23) |
| rust-mt | 3.9 (3.8–3.9) | 27.4 (26.3–29.8) | 6.6 (5.4–7.9) | 5 (5–5) | 21764 (21568–22043) | 0 (0–0) | 6.8 (5.7–8.0) | 5 (5–5) |
| rust-ct | 3.6 (3.6–3.7) | 19.1 (18.8–21.8) | 4.3 (4.3–4.7) | 1 (1–1) | 20365 (20167–20416) | 0 (0–0) | 4.3 (4.3–4.7) | 1 (1–1) |
| zig | 1.9 (1.9–1.9) | 21.4 (21.4–21.5) | 21.4 (21.4–21.5) | 34 (34–34) | 21414 (21035–21777) | 0 (0–0) | 21.6 (21.6–22.0) | 34 (34–34) |
| bun | 20.0 (19.9–20.1) | 169.2 (166.5–171.2) | 37.8 (37.6–37.8) | 6 (6–6) | 20787 (19537–21514) | 0 (0–0) | 37.8 (37.6–37.8) | 6 (6–6) |

### 1000 stalled connections (half-sent header)

| server | RSS idle→held MiB | threads held | closed by server after 70 s | new request served | RSS after release MiB |
|---|---|---|---|---|---|
| go | 10.8→26.8 | 7 | 1000 | yes | 27.7 |
| rust-mt | 4.1→18.0 | 5 | 1000 | yes | 16.9 |
| rust-ct | 3.6→17.4 | 1 | 1000 | yes | 17.4 |
| zig | 1.9→583.8 | 1002 | 0 | yes | 584.1 |
| bun | 19.9→20.9 | 5 | 1000 | yes | 21.0 |

### Soak (mixed traffic incl. aborted transfers)

| server | minutes | cpus | RSS first→last MiB | max MiB | slope KiB/min (all) | slope KiB/min (after 2 min) | threads end | ops | errors |
|---|---|---|---|---|---|---|---|---|---|
| go | 10 | 0-1 | 8.8→14.5 | 14.5 | 50.0 | -5.0 | 9 | 124813 | {} |
| rust-mt | 10 | 2-3 | 3.8→7.8 | 8.7 | 79.8 | 32.3 | 3 | 124817 | {} |
| rust-ct | 10 | 4 | 3.7→5.5 | 6.3 | 6.8 | -4.7 | 1 | 124822 | {} |
| zig | 10 | 5 | 1.8→7.5 | 7.5 | 38.5 | 10.7 | 14 | 124808 | {} |
| bun | 10 | 6 | 19.9→47.3 | 56.7 | 145.0 | -393.7 | 2 | 124803 | {"abort_ul":18} |

Notes on the tables:

- **rust-mt and rust-ct are one binary.** The 0.65 MiB in the footprint table is the
  glibc build that was benchmarked. Binary sizes are covered under cross-compiling below.
- **Bun's binary includes the runtime.** The 77.6 MiB is the `--compile` output, and most
  of its RSS is file-backed pages of that binary. Idle `RssAnon` was 3.0 MiB and
  `RssFile` 18.2 MiB.
- **Two soak slopes.** "slope (all)" includes the first two minutes, when buffers and
  pools fill. "After 2 min" is the leak signal. Every server stays within ±40 KiB/min,
  apart from Bun's −394 KiB/min, which is GC noise on a 40–57 MiB sawtooth. Nothing grew
  across the 10 minutes, and every server served the same ~124,800 operations, including
  1,201 abandoned downloads, 1,201 abandoned uploads and 1,201 malformed requests.
  Bun's 18 `abort_ul` errors are cases where Bun reset the connection before the client
  had finished writing the 1 MiB it planned to send before abandoning the upload. They
  are expected, not failures.
- **Paced 1 Gbit/s CPU % is measured with 100 Hz tick accounting** over 10 s, so it is
  accurate to about ±0.1 %.

### Cross-compiling (`bun run.ts xbuild`, all from this x86_64 host, nothing extra installed)

| target | result | size |
|---|---|---|
| Go linux/arm64 (`GOARCH=arm64`, `CGO_ENABLED=0`) | static, 7 s | 5.19 MiB |
| Rust `aarch64-unknown-linux-musl` (`-C linker=rust-lld`) | static, 10 s | 0.69 MiB |
| Rust `x86_64-unknown-linux-musl` | static-pie | 0.77 MiB |
| Zig `-Dtarget=aarch64-linux` | static, 7 s | 0.24 MiB |
| Bun `--target=bun-linux-arm64` | glibc-dynamic, 1 s | 77.55 MiB |
| Bun `--target=bun-linux-x64-musl` | musl-dynamic | 71.81 MiB |

The arm64 binaries were built but not run, because there is no arm64 machine or qemu here.

SQLite was also checked, because the product stores results:

- **Go**: `modernc.org/sqlite` v1.61.0 (pure Go, SQLite 3.54.1) cross-compiled to a
  static arm64 binary with `CGO_ENABLED=0` in 13 s, at 6.3 MiB.
- **Rust**: `rusqlite` 0.37 with `bundled` (SQLite 3.50.2) needs a C cross-compiler for
  arm64. It worked with `zig cc` as `CC_aarch64_unknown_linux_musl`, through a 4-line
  wrapper that drops the `--target=` flag cc-rs adds. That gave a static arm64 binary
  in 36 s, at 1.9 MiB.
- **Bun**: `bun:sqlite` is built in.
- **Zig**: std has no SQLite. You compile the amalgamation with the build system and
  write or adopt a wrapper.

## Findings the tables don't show

1. **Bun's `type: "direct"` stream buffers the whole response when the client is slow.**
   The first Bun implementation used a direct `ReadableStream` with
   `controller.write(chunk); await controller.flush()`. That looks like backpressure,
   but `flush()` resolves before the socket drains.
   - With 8 clients paced to 1 Gbit/s, peak RSS was 260–270 MiB, which is 8 × the full
     32 MiB response, and the server used 14–17 % CPU.
   - In the soak it grew by about 15 MiB/min, to 188 MiB, and could not keep up with the
     offered load on one core: 19,040 of 60,000 pings were served.
   - Switching to a plain pull stream with `highWaterMark: 0` gave a 35–38 MiB peak and
     3.8 % CPU at the same load. Flat-out throughput was the same either way.

   All tables above use the pull version. The direct-stream rows are kept in
   `bench/results/bun-direct-stream/`. Speed-test clients are always slower than the
   server over a real link, so this pattern would have been a production memory bomb.
2. **Bun buffers upload bodies too.** Peak RSS during uploads was:

   | upload test | peak RSS |
   |---|---|
   | 1 stream | 96–101 MiB |
   | 32 streams | 166–171 MiB |
   | 8 streams paced to 1 Gbit/s | 63–111 MiB |

   It drops back to ~38 MiB afterwards, so this is allocation and GC lag, not a leak.
   Every other runtime stayed under 28 MiB in every test.
3. **Bun ignores `Content-Length` on a streamed body** and always sends
   `Transfer-Encoding: chunked`. A browser's download progress then has no total
   unless the client already knows N, which it does here because it asked for it.
4. **Zig `std.http` is not fit to face clients yet.** I am dropping it, on this evidence:
   - *No timeouts at all.* `std/http/Server.zig` has no header, idle or read timeout,
     and the only way to serve connections concurrently is one OS thread each. 1000
     stalled half-headers produced 1002 threads and 584 MiB RSS. After 70 s it had
     closed 0 of them. The RSS stayed at 584 MiB after the clients went away, because
     `Io.Threaded` keeps its worker threads and their touched stacks. A few thousand
     slow or malicious sockets would push a 1 GB Pi into OOM. Go, Rust and Bun closed
     all 1000 through their header or idle timeouts.
   - *The obvious concurrency call deadlocks accept.* The pattern in Zig's own
     `lib/compiler/std-docs.zig` (`group.async(io, accept, …)`) runs the handler inline
     on the accept thread once `cpu_count − 1` tasks are busy. With the server pinned to
     4 cores and 8 keep-alive downloads running, a new `curl /ping` timed out after 3 s.
     With `group.concurrent` it answered in 2.6 ms. That was a one-line change, tested by
     hand rather than through `run.ts`.
   - *Smaller gaps:*
     - There is no TLS server in std; `std/crypto/tls/` has only `Client.zig`.
     - There is no `sendfile`, which the std-docs source itself notes.
     - Every 204 response carries `content-length: 0`, which RFC 9110 forbids.
     - 0.17 moved the optimize mode to `std.lang.Optimize`, renamed its members
       (`.Debug` became `.debug`) and replaced `-Doptimize=` with `-Drelease`, which broke
       a two-line `build.zig` I wrote from 0.15-era knowledge.

   It was the fastest at upload (0.116 cpu-s/GB at 1 stream) and the smallest binary, and
   it was flat in the soak. But a thread per connection with no timeouts can't be fixed
   from outside the library.
5. **Multi-threaded schedulers stall pings when every core is busy.** With 8 unpaced
   downloads saturating all 4 server cores, ping p99 was:

   | server | ping p99, all 4 cores saturated |
   |---|---|
   | go | 15 ms |
   | rust-mt | 17 ms |
   | zig | 4 ms |
   | rust-ct, bun | 65 µs |

   The likely cause is that Go and tokio's multi-thread schedulers only poll for new
   socket readiness every few milliseconds when all workers are busy. Single-threaded
   event loops interleave the ping with the downloads, and they only use one of the four
   cores. At a realistic 1 Gbit/s every server stays under 130 µs p99. This matters
   only if dyno ever measures latency under load while CPU-bound, for example on a slow
   Pi at multi-gigabit.
6. **Allocators keep memory after bursts.** After the 1000 stalled connections were
   released, RSS stayed at the high-water mark:

   | server | RSS before | RSS after release |
   |---|---|---|
   | Go | 10.8 MiB | 27.7 MiB |
   | Rust (glibc malloc) | 3.6–4.1 MiB | 17 MiB |
   | Bun | 19.9 MiB | 21.0 MiB |

   Bun's per-socket state is tiny. This is retention, not leakage: in the soak the same
   code paths ran 30,000+ times with flat RSS. If it matters for Rust, switching the
   allocator or calling `malloc_trim` is a known remedy, not measured here.
7. **rust-mt upload at 32 streams is an outlier**: 60 Gbit/s and 0.47 cpu-s/GB, against
   116 Gbit/s and 0.23 at 8 streams. All three reps agree, and I have not explained it.
   rust-ct doesn't show it.

## Caveats

- **Localhost is not a NIC.** Loopback runs at 20–130 Gbit/s with no checksum offload,
  interrupts or driver. The flat-out Gbit/s columns rank the runtimes' per-byte cost.
  They are not a forecast of real throughput, which the link will cap at 1 or 2.5
  Gbit/s. The per-GB CPU and the paced 1 Gbit/s rows are the numbers that carry over.
- **This is an x86 VM, not a Pi.** Server cores 0-3 are vCPUs of a Ryzen 5825U. A
  Raspberry Pi core is much slower, so expect several times the CPU per GB. The ratios
  between runtimes should hold roughly, but I have not measured on arm64.
- **TLS was not measured.** Everything is plain HTTP/1.1. TLS (needed for HTTPS or
  HTTP/2 to browsers) adds per-byte encryption cost that would likely dominate these
  numbers on a Pi without AES instructions (Pi 4). Support differs:
  - Go has `crypto/tls` in std.
  - Rust would use rustls.
  - Bun has it built in.
  - Zig std has no TLS server.
- **Same machine, both sides.** The client and server share L3 cache and memory bandwidth
  even when pinned apart. Results for different servers are comparable with each other;
  the absolute values are optimistic.
- **The soak ran all servers at once,** each on 1–2 cores, at a modest rate: about
  170 MB/s per server plus about 200 requests/s. It shows the absence of growth over 10
  minutes and about 125k operations each. It cannot rule out a leak slower than about
  10 KiB/min, or one on a path the soak never exercised.
- **Each implementation is idiomatic but small.** Real dyno code will add routing,
  JSON, SQLite and scheduling. Their footprint (axum versus raw hyper, for example) is
  not measured here.

## Ecosystem for the rest of the product

| need | Go | Rust | Bun |
|---|---|---|---|
| SQLite | `modernc.org/sqlite`, pure Go, cross-compiles with no C toolchain (checked) | `rusqlite` + `bundled`; arm64 needs a C cross-compiler (`zig cc` worked, checked) | `bun:sqlite` built in |
| Scheduling (periodic tests) | `time.Ticker` and goroutines, std | `tokio::time::interval` | `setInterval` |
| Embed the web UI in one binary | `embed` (std), used here | `include_bytes!` used here; `rust-embed` for directories | `bun build --compile` embeds imports, used here |
| arm64 build from x86 | `GOARCH=arm64`, nothing else needed | `rustup target add` + `rust-lld`; plus `zig cc` once C is involved | `--target=bun-linux-arm64` |
| Container | `FROM scratch` + 5–6 MiB static binary | `FROM scratch` + <1 MiB (2 MiB with SQLite) static musl binary | needs a libc base layer + 72–78 MiB binary |
| TLS | std | rustls (crate) | built in |
| HTTP server | std, with timeouts built in | hyper is low level: I hand-wrote a `Body` impl, a timer, and a query parser. axum adds routing and extractors | built in |

## Maintainability for one developer working with an LLM

- **Go** needed the least code and the least thought: about 110 lines, all std, no build
  configuration. Go 1 compatibility means today's code and today's LLM training data stay
  valid.
- **Rust** was about 150 lines plus Cargo config. The hyper body and service types are
  where an LLM, or a person, spends time. axum would hide most of that.
  Compiling takes 11 s clean and the dependency tree is 20 crates, against Go's 2 s
  and zero.
- **Bun** was the shortest and in the owner's preferred language. But the one subtle
  thing in it, stream backpressure, was a trap that type-checks and passes casual
  testing. That is exactly the failure mode LLM-written code is prone to.
- **Zig** changes its std API every release, and LLM knowledge of 0.16/0.17 `std.Io` is
  thin. I had to read the std source to write 100 lines.

## Recommendation

**Rust, hyper on `tokio` `current_thread`,** for the dyno server.

Weighing the four things that were asked for:

- **Footprint.** rust-ct and Zig tie for the smallest idle RSS (3.7 MiB). rust-ct also
  has the lowest RSS after load (4.3 MiB) and after churn, and the lowest CPU at
  1 Gbit/s in both directions. Go is about 3× the RSS (10.8 MiB idle, 14.5 MiB after
  load), and Bun about 5× (20 MiB idle, 38 MiB after load). In absolute terms all of
  these except Bun's upload spikes are small next to a Pi's 1–8 GB. The owner asked for
  the smallest possible, though, and Rust is the smallest that is also robust.
- **Stability and leaks.** Go and both Rust variants passed everything: zero errors,
  flat soak slopes (−5 to +32 KiB/min after warmup), and stalled connections closed by
  timeout. Bun passed the soak once its stream was fixed, but its memory under upload is
  the least predictable. Zig fails on stalled connections.
- **Ecosystem.** Go is strongest for this product: SQLite, TLS, embedding and
  cross-compiling all work with nothing but the Go toolchain. Rust gets the same features
  with one extra piece, `zig cc` as the C cross-compiler for bundled SQLite. That was
  checked and builds a 1.9 MiB static arm64 binary. A static musl binary means the
  container is `FROM scratch` at under 2 MiB.
- **Maintainability by one person with an LLM.** Go is better, and that is the real cost
  of choosing Rust. Use axum rather than raw hyper once there are more than a handful
  of routes. Keep the hot path (the download `Body` and upload drain) as small,
  isolated, tested modules.

Why current-thread rather than multi-thread tokio:

- It used the same CPU per GB at 1 stream, and less at 8 and 32 streams.
- Its ping p99 under saturation was 65 µs against 17 ms.
- It holds 1 thread instead of 5.
- One core moved 37 Gbit/s on loopback, more than any NIC a Pi has.

If a Pi core turns out unable to fill 2.5 Gbit/s, `RT_WORKERS=2` on the multi-thread
runtime is a one-line change.

Before committing, rerun `bench/` on the actual Pi (`bun run.ts xbuild` already
produces the arm64 binaries). Also add a TLS variant if HTTPS will be terminated by dyno
itself rather than by a reverse proxy.

