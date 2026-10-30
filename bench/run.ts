#!/usr/bin/env bun
// dyno runtime benchmark orchestrator. Run with Bun from this directory:
//
//   bun run.ts build        build every server + the load generator, record versions and sizes
//   bun run.ts xbuild       cross-compile every server for linux/arm64 (Raspberry Pi) and static x86_64
//   bun run.ts footprint    cold start (20x), idle RSS at 1 s and 60 s
//   bun run.ts perf         3 reps: dl/ul at 1/8/32 streams, paced 1 Gbit/s, ping idle + loaded, 10k churn
//   bun run.ts latency      only the "ping while 8 streams are paced to 1 Gbit/s" test (also part of perf)
//   bun run.ts soak         10 min mixed traffic per server (all servers in parallel, own cores), RSS every 5 s
//   bun run.ts hold         1000 stalled half-sent requests per server for 70 s: RSS, threads, timeouts
//   bun run.ts summary      print markdown tables from results/*.jsonl
//   bun run.ts all          everything above in order
//
// Options: --only=go,zig   restrict servers;  --reps=3;  --soak=600 (seconds);  --dur=6 (perf window seconds)
//
// CPU layout (10 cores): server on 0-3, load generator on 4-9. While measuring ping under load,
// the background download generator runs on 6-9 and the ping client on 4-5.

import { $ } from "bun";
import { mkdirSync, appendFileSync, existsSync, readFileSync, statSync, copyFileSync } from "node:fs";

const HOME = process.env.HOME!;
const ROOT = import.meta.dir;
process.env.PATH = `${HOME}/.local/go/bin:${HOME}/.cargo/bin:${HOME}/.local/zig:${process.env.PATH}`;
// Keep build caches inside bench/.cache (gitignored) instead of ~/.cache, ~/go and ~/.config/go.
for (const [k, v] of Object.entries({ GOCACHE: "go-build", GOPATH: "gopath", ZIG_GLOBAL_CACHE_DIR: "zig", XDG_CONFIG_HOME: "config" }))
  process.env[k] ??= `${ROOT}/.cache/${v}`;
process.chdir(ROOT);
mkdirSync("bin", { recursive: true });
mkdirSync("results", { recursive: true });

const args = Object.fromEntries(
  process.argv.slice(3).map((a) => a.replace(/^--/, "").split("=") as [string, string]),
);
const REPS = Number(args.reps ?? 3);
const SOAK_S = Number(args.soak ?? 600);
const DUR = Number(args.dur ?? 6);

type Server = { name: string; bin: string; env: Record<string, string>; soakCpus: string };
const ALL: Server[] = [
  { name: "go", bin: "bin/go-server", env: {}, soakCpus: "0-1" },
  { name: "rust-mt", bin: "bin/rust-server", env: { RT: "mt" }, soakCpus: "2-3" },
  { name: "rust-ct", bin: "bin/rust-server", env: { RT: "ct" }, soakCpus: "4" },
  { name: "zig", bin: "bin/zig-server", env: {}, soakCpus: "5" },
  { name: "bun", bin: "bin/bun-server", env: {}, soakCpus: "6" },
];
const SERVERS = args.only ? ALL.filter((s) => args.only!.split(",").includes(s.name)) : ALL;
const SERVER_CPUS = "0-3";
const CLIENT_CPUS = "4-9";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const record = (file: string, row: object) => appendFileSync(`results/${file}.jsonl`, JSON.stringify({ at: new Date().toISOString(), ...row }) + "\n");
const log = (...a: unknown[]) => console.error(`[${new Date().toISOString().slice(11, 19)}]`, ...a);

function procStatus(pid: number): Record<string, number> {
  const out: Record<string, number> = {};
  for (const line of readFileSync(`/proc/${pid}/status`, "utf8").split("\n")) {
    const m = line.match(/^(VmRSS|RssAnon|RssFile|VmHWM|Threads):\s+(\d+)/);
    if (m) out[m[1]!] = Number(m[2]);
  }
  return out;
}

async function waitReady(port: number, timeoutMs = 10000) {
  const t0 = performance.now();
  while (performance.now() - t0 < timeoutMs) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/ping`);
      if (r.status === 204) return;
    } catch {}
    await sleep(5);
  }
  throw new Error(`server on ${port} not ready`);
}

async function startServer(s: Server, port: number, cpus = SERVER_CPUS) {
  const proc = Bun.spawn(["taskset", "-c", cpus, s.bin], {
    env: { ...process.env, ...s.env, PORT: String(port) },
    stdout: "ignore",
    stderr: "inherit",
  });
  await waitReady(port);
  const comm = readFileSync(`/proc/${proc.pid}/comm`, "utf8").trim();
  if (comm === "taskset") throw new Error("pid is taskset, not the server");
  return proc;
}

async function stopServer(proc: Bun.Subprocess) {
  proc.kill("SIGTERM");
  await Promise.race([proc.exited, sleep(2000)]);
  if (proc.exitCode === null) proc.kill("SIGKILL");
  await proc.exited;
}

async function loadgen(mode: string, flags: Record<string, string | number>, cpus = CLIENT_CPUS, extra: string[] = []) {
  const argv = ["taskset", "-c", cpus, "bin/loadgen", mode, ...Object.entries(flags).map(([k, v]) => `-${k}=${v}`), ...extra];
  const p = Bun.spawn(argv, { stdout: "pipe", stderr: "inherit" });
  const text = await new Response(p.stdout).text();
  if ((await p.exited) !== 0) throw new Error(`loadgen ${mode} failed: ${argv.join(" ")}`);
  return JSON.parse(text.trim().split("\n").pop()!);
}

// ---------------------------------------------------------------- build

async function build() {
  log("building");
  copyFileSync("static/index.html", "servers/go/index.html");
  copyFileSync("static/index.html", "servers/zig/src/index.html");
  await $`CGO_ENABLED=0 go build -trimpath ${"-ldflags=-s -w"} -o ../../bin/go-server .`.cwd("servers/go");
  await $`CGO_ENABLED=0 go build -trimpath -o ../bin/loadgen .`.cwd("loadgen");
  await $`cargo build --release --locked`.cwd("servers/rust").quiet();
  copyFileSync("servers/rust/target/release/dyno-bench-rust", "bin/rust-server");
  await $`zig build -Drelease=true`.cwd("servers/zig");
  copyFileSync("servers/zig/zig-out/bin/zig-server", "bin/zig-server");
  await $`bun build --compile --minify --bytecode server.ts --outfile ../../bin/bun-server`.cwd("servers/bun").quiet();

  const lock = readFileSync("servers/rust/Cargo.lock", "utf8");
  const crate = (n: string) => lock.match(new RegExp(`name = "${n}"\\nversion = "([^"]+)"`))?.[1];
  const versions = {
    kernel: (await $`uname -r`.text()).trim(),
    cpu: (await $`lscpu`.text()).match(/Model name:\s+(.*)/)?.[1],
    go: (await $`go version`.text()).trim(),
    rustc: (await $`rustc -V`.text()).trim(),
    hyper: crate("hyper"),
    tokio: crate("tokio"),
    "hyper-util": crate("hyper-util"),
    zig: (await $`zig version`.text()).trim(),
    bun: (await $`bun --version`.text()).trim(),
  };
  const sizes: Record<string, unknown> = {};
  for (const b of ["go-server", "rust-server", "zig-server", "bun-server"]) {
    sizes[b] = { bytes: statSync(`bin/${b}`).size, file: (await $`file -b bin/${b}`.text()).trim() };
  }
  record("build", { versions, sizes });
  console.log(JSON.stringify({ versions, sizes }, null, 2));
}

async function xbuild() {
  log("cross-compiling for linux/arm64 and static x86_64");
  mkdirSync("bin/x", { recursive: true });
  const res: Record<string, unknown> = {};
  const attempt = async (name: string, cmd: () => Promise<unknown>, outFile: string) => {
    const t0 = performance.now();
    try {
      await cmd();
      res[name] = { ok: true, secs: +((performance.now() - t0) / 1000).toFixed(1), bytes: statSync(outFile).size, file: (await $`file -b ${outFile}`.text()).trim() };
    } catch (e) {
      res[name] = { ok: false, error: String((e as { stderr?: Buffer }).stderr ?? e).slice(0, 600) };
    }
    log(name, JSON.stringify(res[name]).slice(0, 200));
  };
  await attempt("go-arm64", () => $`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath ${"-ldflags=-s -w"} -o ../../bin/x/go-server-arm64 .`.cwd("servers/go").quiet(), "bin/x/go-server-arm64");
  await $`rustup target add aarch64-unknown-linux-musl x86_64-unknown-linux-musl`.quiet();
  for (const [name, triple] of [["rust-arm64-musl", "aarch64-unknown-linux-musl"], ["rust-x86_64-musl", "x86_64-unknown-linux-musl"]] as const) {
    // musl targets ship their own CRT; rust-lld (bundled with rustup) links them without a C cross toolchain.
    await attempt(name, () => $`RUSTFLAGS="-C linker=rust-lld" cargo build --release --locked --target ${triple}`.cwd("servers/rust").quiet(), `servers/rust/target/${triple}/release/dyno-bench-rust`);
  }
  await attempt("zig-arm64", () => $`zig build -Drelease=true -Dtarget=aarch64-linux --prefix-exe-dir ${ROOT}/bin/x/zig-arm64`.cwd("servers/zig").quiet(), "bin/x/zig-arm64/zig-server");
  await attempt("bun-arm64", () => $`bun build --compile --minify --bytecode --target=bun-linux-arm64 server.ts --outfile ../../bin/x/bun-server-arm64`.cwd("servers/bun").quiet(), "bin/x/bun-server-arm64");
  await attempt("bun-x64-musl", () => $`bun build --compile --minify --bytecode --target=bun-linux-x64-musl server.ts --outfile ../../bin/x/bun-server-x64-musl`.cwd("servers/bun").quiet(), "bin/x/bun-server-x64-musl");
  record("xbuild", res);
  console.log(JSON.stringify(res, null, 2));
}

// ---------------------------------------------------------------- footprint

async function footprint() {
  for (const s of SERVERS) {
    log("cold start", s.name);
    // loadgen spawns the server itself, so pass env through a wrapper.
    const envArgs = Object.entries({ ...s.env, PORT: "8199" }).map(([k, v]) => `${k}=${v}`);
    const r = await loadgen("coldstart", { addr: "127.0.0.1:8199", n: 20 }, CLIENT_CPUS, ["--", "env", ...envArgs, "taskset", "-c", SERVER_CPUS, s.bin]);
    record("coldstart", { server: s.name, ...r });
  }
  log("idle RSS: starting all servers, sampling at 1 s and 60 s");
  const procs = await Promise.all(SERVERS.map((s, i) => startServer(s, 8200 + i)));
  await sleep(1000);
  const at1 = procs.map((p) => procStatus(p.pid));
  await sleep(59000);
  const at60 = procs.map((p) => procStatus(p.pid));
  SERVERS.forEach((s, i) => record("idle", { server: s.name, at1s: at1[i], at60s: at60[i] }));
  await Promise.all(procs.map(stopServer));
}

// ---------------------------------------------------------------- perf

async function perfRep(s: Server, rep: number) {
  const port = 8300;
  const proc = await startServer(s, port);
  const addr = `127.0.0.1:${port}`;
  const base = { server: s.name, rep };
  const w = { warmup: "2s", dur: `${DUR}s` };
  try {
    record("perf", { ...base, test: "rss_start", ...procStatus(proc.pid) });
    for (const mode of ["dl", "ul"]) {
      for (const c of [1, 8, 32]) {
        const r = await loadgen(mode, { addr, pid: proc.pid, c, bytes: 32 << 20, ...w });
        record("perf", { ...base, test: `${mode}_c${c}`, ...r });
        log(s.name, rep, `${mode} c=${c}`, `${r.gbit_s.toFixed(1)} Gbit/s`, `${r.cpu_s_per_gb.toFixed(3)} cpu-s/GB`, `peak ${r.peak_rss_kib} KiB`, `err ${r.errors}`);
      }
      const r = await loadgen(mode, { addr, pid: proc.pid, c: 8, bytes: 32 << 20, mbps: 1000, warmup: "2s", dur: "10s" });
      record("perf", { ...base, test: `${mode}_paced1g`, ...r });
      log(s.name, rep, `${mode} paced 1G`, `${r.gbit_s.toFixed(2)} Gbit/s`, `${r.cpu_util_pct.toFixed(1)}% cpu`);
    }
    await sleep(2000);
    const ping = await loadgen("ping", { addr, n: 5000 }, "4-5");
    record("perf", { ...base, test: "ping_idle", ...ping });
    // 8 background download streams on cores 6-9, ping client alone on 4-5
    const bg = loadgen("dl", { addr, pid: proc.pid, c: 8, bytes: 32 << 20, warmup: "1s", dur: "6s" }, "6-9");
    await sleep(1500);
    const pingLoaded = await loadgen("ping", { addr, n: 5000 }, "4-5");
    const bgr = await bg;
    record("perf", { ...base, test: "ping_loaded", ...pingLoaded, bg_gbit_s: bgr.gbit_s });
    log(s.name, rep, `ping p50/p99 idle ${ping.p50_us.toFixed(0)}/${ping.p99_us.toFixed(0)} us, loaded ${pingLoaded.p50_us.toFixed(0)}/${pingLoaded.p99_us.toFixed(0)} us`);
    await pingPaced(s, rep, proc.pid, port);
    await sleep(5000);
    record("perf", { ...base, test: "rss_after_load_5s", ...procStatus(proc.pid) });
    const ch = await loadgen("churn", { addr, pid: proc.pid, n: 10000, c: 16 });
    record("perf", { ...base, test: "churn", ...ch });
    log(s.name, rep, `churn ${ch.conn_per_s.toFixed(0)} conn/s, err ${ch.errors}, rss ${ch.rss_before_kib}->${ch.rss_settled_kib} KiB, threads ${ch.threads_before}->${ch.threads_after}`);
  } finally {
    await stopServer(proc);
  }
}

// Ping while 8 download streams are paced to 1 Gbit/s in total: the realistic "latency under
// load" case, where the server is NIC-bound rather than CPU-bound.
async function pingPaced(s: Server, rep: number, pid: number, port: number) {
  const addr = `127.0.0.1:${port}`;
  const bg = loadgen("dl", { addr, pid, c: 8, bytes: 32 << 20, mbps: 1000, warmup: "1s", dur: "6s" }, "6-9");
  await sleep(1500);
  const r = await loadgen("ping", { addr, n: 5000 }, "4-5");
  const bgr = await bg;
  record("perf", { server: s.name, rep, test: "ping_paced1g", ...r, bg_gbit_s: bgr.gbit_s });
  log(s.name, rep, `ping p50/p99 with 8 streams paced to 1 Gbit/s: ${r.p50_us.toFixed(0)}/${r.p99_us.toFixed(0)} us`);
}

// Only the paced-load ping test (perf includes it; this phase exists to run it on its own).
async function latency() {
  for (let rep = 1; rep <= REPS; rep++)
    for (const s of SERVERS) {
      const proc = await startServer(s, 8300);
      try {
        await pingPaced(s, rep, proc.pid, 8300);
      } finally {
        await stopServer(proc);
      }
    }
}

async function perf() {
  for (let rep = 1; rep <= REPS; rep++) for (const s of SERVERS) await perfRep(s, rep);
}

// ---------------------------------------------------------------- soak

async function soak() {
  log(`soak: ${SOAK_S}s, servers in parallel on their own cores, clients on 7-9`);
  const procs = await Promise.all(SERVERS.map((s, i) => startServer(s, 8400 + i, s.soakCpus)));
  try {
    const rows = await Promise.all(
      SERVERS.map((s, i) => loadgen("soak", { addr: `127.0.0.1:${8400 + i}`, pid: procs[i]!.pid, dur: `${SOAK_S}s`, sample: "5s" }, "7-9")),
    );
    SERVERS.forEach((s, i) => {
      record("soak", { server: s.name, cpus: s.soakCpus, ...rows[i] });
      log(s.name, `slope ${rows[i].slope_kib_per_min_all.toFixed(1)} KiB/min (after 2m ${rows[i].slope_kib_per_min_after_2m.toFixed(1)})`, `rss ${rows[i].rss_first_kib}->${rows[i].rss_last_kib}`, JSON.stringify(rows[i].errors));
    });
  } finally {
    await Promise.all(procs.map(stopServer));
  }
}

// ---------------------------------------------------------------- hold

async function hold() {
  log("hold: 1000 stalled connections per server for 70 s, all servers at once");
  const procs = await Promise.all(SERVERS.map((s, i) => startServer(s, 8500 + i)));
  try {
    const res = await Promise.all(SERVERS.map((s, i) => loadgen("hold", { addr: `127.0.0.1:${8500 + i}`, pid: procs[i]!.pid, n: 1000, dur: "70s" })));
    SERVERS.forEach((s, i) => {
      record("hold", { server: s.name, ...res[i] });
      log(s.name, JSON.stringify(res[i]));
    });
  } finally {
    await Promise.all(procs.map(stopServer));
  }
}

// ---------------------------------------------------------------- summary

function rows(file: string): any[] {
  const p = `results/${file}.jsonl`;
  return existsSync(p) ? readFileSync(p, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : [];
}
const median = (xs: number[]) => { const s = [...xs].sort((a, b) => a - b); const m = s.length >> 1; return s.length % 2 ? s[m]! : (s[m - 1]! + s[m]!) / 2; };
function stat(xs: number[], digits = 2) {
  if (!xs.length) return "n/a";
  const med = median(xs).toFixed(digits);
  return xs.length > 1 ? `${med} (${Math.min(...xs).toFixed(digits)}–${Math.max(...xs).toFixed(digits)})` : med;
}
const mib = (kib: number) => kib / 1024;

function summary() {
  const names = ALL.map((s) => s.name);
  const perfRows = rows("perf");
  const get = (server: string, test: string, key: string) => perfRows.filter((r) => r.server === server && r.test === test).map((r) => r[key] as number);
  const table = (title: string, cols: string[], cell: (server: string, col: string) => string) => {
    console.log(`\n### ${title}\n\n| server | ${cols.join(" | ")} |\n|---|${cols.map(() => "---").join("|")}|`);
    for (const n of names) console.log(`| ${n} | ${cols.map((c) => cell(n, c)).join(" | ")} |`);
  };
  const build = rows("build").pop();
  const binOf: Record<string, string> = { go: "go-server", "rust-mt": "rust-server", "rust-ct": "rust-server", zig: "zig-server", bun: "bun-server" };
  const cold = rows("coldstart"), idle = rows("idle");
  table("Footprint", ["binary MiB", "cold start ms (median of 20)", "idle RSS 1 s MiB", "idle RSS 60 s MiB", "idle threads"], (n, c) => {
    const i = idle.filter((r) => r.server === n).pop();
    switch (c) {
      case "binary MiB": return build ? (build.sizes[binOf[n]!].bytes / 2 ** 20).toFixed(2) : "n/a";
      case "cold start ms (median of 20)": { const r = cold.filter((r) => r.server === n).pop(); return r ? `${r.median_ms.toFixed(1)} (${r.min_ms.toFixed(1)}–${r.max_ms.toFixed(1)})` : "n/a"; }
      case "idle RSS 1 s MiB": return i ? mib(i.at1s.VmRSS).toFixed(1) : "n/a";
      case "idle RSS 60 s MiB": return i ? mib(i.at60s.VmRSS).toFixed(1) : "n/a";
      default: return i ? String(i.at60s.Threads) : "n/a";
    }
  });
  for (const mode of ["dl", "ul"]) {
    table(`${mode === "dl" ? "Download" : "Upload"}: Gbit/s, server CPU-seconds per GB (median, min–max of reps)`,
      [1, 8, 32].flatMap((c) => [`c${c} Gbit/s`, `c${c} cpu-s/GB`]).concat(["paced 1 Gbit/s: server CPU %"]),
      (n, col) => {
        if (col.startsWith("paced")) return stat(get(n, `${mode}_paced1g`, "cpu_util_pct"), 1);
        const [c, what] = col.split(" ");
        return what === "Gbit/s" ? stat(get(n, `${mode}_${c}`, "gbit_s"), 1) : stat(get(n, `${mode}_${c}`, "cpu_s_per_gb"), 3);
      });
  }
  table("Ping latency µs (median of reps)", ["idle p50", "idle p99", "8 dl streams flat out p50", "8 dl streams flat out p99", "8 dl streams at 1 Gbit/s p50", "8 dl streams at 1 Gbit/s p99"], (n, c) => {
    const t = c.startsWith("idle") ? "ping_idle" : c.includes("1 Gbit") ? "ping_paced1g" : "ping_loaded";
    return stat(get(n, t, c.endsWith("p50") ? "p50_us" : "p99_us"), 0);
  });
  table("Memory under load (MiB, median of reps)", ["start RSS", "peak RSS (any test)", "RSS 5 s after load", "threads under load", "10k churn conn/s", "churn errors", "RSS after churn", "threads after churn"], (n, c) => {
    const reps = [...new Set(perfRows.filter((r) => r.server === n).map((r) => r.rep))];
    const peak = reps.map((rep) => Math.max(...perfRows.filter((r) => r.server === n && r.rep === rep && r.peak_rss_kib).map((r) => r.peak_rss_kib)));
    switch (c) {
      case "start RSS": return stat(get(n, "rss_start", "VmRSS").map(mib), 1);
      case "peak RSS (any test)": return stat(peak.map(mib), 1);
      case "RSS 5 s after load": return stat(get(n, "rss_after_load_5s", "VmRSS").map(mib), 1);
      case "threads under load": return stat(get(n, "dl_c32", "threads"), 0);
      case "10k churn conn/s": return stat(get(n, "churn", "conn_per_s"), 0);
      case "churn errors": return stat(get(n, "churn", "errors"), 0);
      case "RSS after churn": return stat(get(n, "churn", "rss_settled_kib").map(mib), 1);
      default: return stat(get(n, "churn", "threads_after"), 0);
    }
  });
  const holdRows = rows("hold");
  table("1000 stalled connections (half-sent header)", ["RSS idle→held MiB", "threads held", "closed by server after 70 s", "new request served", "RSS after release MiB"], (n, c) => {
    const r = holdRows.filter((r) => r.server === n).pop();
    if (!r) return "n/a";
    switch (c) {
      case "RSS idle→held MiB": return `${mib(r.rss_before_kib).toFixed(1)}→${mib(r.rss_held_kib).toFixed(1)}`;
      case "threads held": return String(r.threads_held);
      case "closed by server after 70 s": return String(r.closed_by_server);
      case "new request served": return r.fresh_request_ok ? "yes" : "no";
      default: return mib(r.rss_released_kib).toFixed(1);
    }
  });
  const soakRows = rows("soak");
  table("Soak (mixed traffic incl. aborted transfers)", ["minutes", "cpus", "RSS first→last MiB", "max MiB", "slope KiB/min (all)", "slope KiB/min (after 2 min)", "threads end", "ops", "errors"], (n, c) => {
    const r = soakRows.filter((r) => r.server === n).pop();
    if (!r) return "n/a";
    const ops = Object.values(r.ops as Record<string, number>).reduce((a, b) => a + b, 0);
    switch (c) {
      case "minutes": return (r.secs / 60).toFixed(0);
      case "cpus": return r.cpus;
      case "RSS first→last MiB": return `${mib(r.rss_first_kib).toFixed(1)}→${mib(r.rss_last_kib).toFixed(1)}`;
      case "max MiB": return mib(r.rss_max_kib).toFixed(1);
      case "slope KiB/min (all)": return r.slope_kib_per_min_all.toFixed(1);
      case "slope KiB/min (after 2 min)": return r.slope_kib_per_min_after_2m.toFixed(1);
      case "threads end": return String(r.threads_after_stop);
      case "ops": return String(ops);
      default: return JSON.stringify(r.errors);
    }
  });
}

// ---------------------------------------------------------------- main

const phases: Record<string, () => Promise<void> | void> = { build, xbuild, footprint, perf, latency, soak, hold, summary };
const want = process.argv[2] ?? "summary";
const order = want === "all" ? ["build", "xbuild", "footprint", "perf", "soak", "hold", "summary"] : [want];
for (const p of order) {
  if (!phases[p]) throw new Error(`unknown phase ${p}; use one of ${Object.keys(phases).join(", ")}, all`);
  await phases[p]!();
}
