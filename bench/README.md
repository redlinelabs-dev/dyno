# Runtime benchmark

The same small HTTP/1.1 server written for four runtimes, plus a load generator and a Bun
script that measures them. Results and the recommendation are in
`../docs/research/runtime-benchmark.md`.

| dir | server |
|---|---|
| `servers/go` | Go `net/http`, std only |
| `servers/rust` | Rust hyper 1.x on tokio; `RT=mt` (multi-thread, default) or `RT=ct` (current-thread) |
| `servers/zig` | Zig `std.http.Server` on `std.Io.Threaded`, std only |
| `servers/bun` | `Bun.serve`, no dependencies |
| `loadgen` | Go, std only: raw keep-alive HTTP/1.1 client that also samples the server's `/proc` |
| `static/index.html` | the page every server embeds and serves at `/` |

Every server exposes the same endpoints on `127.0.0.1:$PORT`:

- `GET /dl?bytes=N` streams N bytes from a 1 MiB random buffer generated at startup, in 256 KiB writes
- `POST /ul` reads and discards the body and returns the byte count
- `GET /ping` returns 204 with no body
- `GET /` returns the embedded `index.html`

## Toolchains

Installed user-locally, no root needed:

- Go: tarball from go.dev unpacked to `~/.local/go`
- Rust: `rustup` with `--profile minimal` (`~/.rustup`, `~/.cargo`); `xbuild` adds the
  `aarch64-unknown-linux-musl` and `x86_64-unknown-linux-musl` targets
- Zig: tarball from ziglang.org unpacked to `~/.local/zig`
- Bun: already installed

`run.ts` adds those to `PATH` itself and points the Go and Zig caches at `bench/.cache`.

## Running it

```sh
cd bench
bun run.ts build       # build all servers and loadgen into bin/, record versions and sizes
bun run.ts xbuild      # cross-compile for linux/arm64 (Raspberry Pi) and static x86_64
bun run.ts footprint   # cold start x20, idle RSS at 1 s and 60 s  (~1.5 min)
bun run.ts perf        # 3 reps of throughput, CPU, ping, churn     (~35 min)
bun run.ts latency     # only the ping-under-1-Gbit/s-load test   (~2 min)
bun run.ts soak        # 10 min mixed traffic, all servers at once  (~11 min)
bun run.ts hold        # 1000 stalled connections per server        (~1.5 min)
bun run.ts summary     # markdown tables from results/*.jsonl
bun run.ts all         # all of the above in order
```

Options: `--only=go,zig` restricts the servers, `--reps=3`, `--dur=6` sets the perf window
in seconds, `--soak=600` sets the soak length in seconds.

Each phase appends to `results/<phase>.jsonl`, one JSON object per measurement, with a
timestamp. Delete the file before a phase to start fresh. `summary` uses every row it finds.

## What gets measured

- CPU pinning: the server runs on cores 0-3 (`taskset`), the load generator on 4-9.
  During the loaded ping test the 8 download streams run on 6-9 and the ping client on 4-5.
  During the soak each server gets its own cores (`soakCpus` in `run.ts`) and the
  clients share 7-9.
- Server CPU is `utime+stime` from `/proc/<pid>/stat`, read at the start and end of each
  measured window, after a 2 s warmup.
- Peak RSS is `VmHWM`, reset by writing `5` to `/proc/<pid>/clear_refs` at the start of
  each window.
- `paced1g` limits the client to 1 Gbit/s in total across 8 streams. It answers "how much
  CPU does it take to fill a gigabit link".
- `ping_paced1g` measures ping latency while 8 download streams are held to 1 Gbit/s in
  total, which is the case a real speed test sees: the link, not the CPU, is the limit.
  `ping_loaded` does the same with the streams unpaced, which saturates the server cores.
- `churn` opens 10,000 connections (16 at a time), does one `GET /ping` on each, and closes it.
- `soak` runs rate-limited mixed traffic: keep-alive 4 MiB downloads and uploads, pings,
  short connections, the index page, malformed requests, downloads and uploads the client
  abandons partway, and connections that send nothing. RSS is sampled every 5 s and the
  script reports a least-squares slope.
- `hold` opens 1000 connections per server that send half a request header and then stall.
  It records RSS and threads while they are held, how many the server has closed after 70 s
  (header or idle timeouts), and whether a new request is still served.

The load generator can also be run by hand, e.g.
`taskset -c 4-9 bin/loadgen dl -addr 127.0.0.1:8080 -pid $PID -c 8 -bytes 33554432`.
