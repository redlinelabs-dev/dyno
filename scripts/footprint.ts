#!/usr/bin/env bun
// Footprint check: runs bin/dyno the way the image does (GOMEMLIMIT=64MiB) and
// fails when any ADR-0006 budget is exceeded. Traffic comes from bench/loadgen,
// built to bin/loadgen by `make loadgen`. Linux only: it samples /proc.
//
//   bun scripts/footprint.ts          quick, for `make check`: idle RSS, peak RSS
//                                     at 32 streams, stalled connections (~1.5 min)
//   bun scripts/footprint.ts --full   for `make bench`: idle RSS at 60 s, the
//                                     12-minute soak, CPU and ping at 1 Gbit/s,
//                                     and a row in bench/results/dyno-footprint.jsonl
//
// The leak budget is defined over a 10-minute soak, so the quick mode skips
// it rather than guess from a short run.

import { $ } from "bun";
import { appendFileSync, existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { availableParallelism, tmpdir } from "node:os";

const ROOT = `${import.meta.dir}/..`;
process.chdir(ROOT);
const FULL = process.argv.includes("--full");
const DYNO = "bin/dyno";
const LOADGEN = "bin/loadgen";
const PORT = 39660;
const ADDR = `127.0.0.1:${PORT}`;
// The metrics listener is on, as an operator with Prometheus runs it, and
// gets scraped during the load phases, so ADR-0006 covers that setup too.
const METRICS_ADDR = `127.0.0.1:${PORT + 1}`;

// ADR-0006 budgets. Change them there first, with measurements.
const BUDGET = {
  idleRssMiB: 24,
  peakRssMiB: 48,
  leakKiBPerMin: 16, // the soak's live-heap and outside-Go slopes
};

// Pin server and clients apart, as bench/ does, when there are cores to spare.
const cores = availableParallelism();
const pin = cores >= 8 && Bun.which("taskset") !== null;
const SERVER_CPUS = "0-3";
const CLIENT_CPUS = `4-${cores - 1}`;
const pinned = (cpus: string, argv: string[]) => (pin ? ["taskset", "-c", cpus, ...argv] : argv);

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const log = (...a: unknown[]) => console.error(`[${new Date().toISOString().slice(11, 19)}]`, ...a);
const mib = (kib: number) => kib / 1024;

for (const bin of [DYNO, LOADGEN]) {
  if (!existsSync(bin)) throw new Error(`${bin} missing; run \`make build loadgen\` first`);
}

function procStatus(pid: number): Record<string, number> {
  const out: Record<string, number> = {};
  for (const line of readFileSync(`/proc/${pid}/status`, "utf8").split("\n")) {
    const m = line.match(/^(VmRSS|VmHWM|Threads):\s+(\d+)/);
    if (m) out[m[1]!] = Number(m[2]);
  }
  return out;
}

// Each run gets an empty results database, as a fresh install would.
const DATA = mkdtempSync(`${tmpdir()}/dyno-footprint-`);
process.on("exit", () => rmSync(DATA, { recursive: true, force: true }));

async function start() {
  const proc = Bun.spawn(pinned(SERVER_CPUS, [DYNO]), {
    env: { ...process.env, DYNO_LISTEN: ADDR, DYNO_METRICS_LISTEN: METRICS_ADDR, DYNO_DATA: DATA, GOMEMLIMIT: "64MiB" },
    stdout: "ignore",
    stderr: "ignore",
  });
  for (const t0 = performance.now(); performance.now() - t0 < 10_000; await sleep(10)) {
    try {
      if ((await fetch(`http://${ADDR}/ping`)).status === 204) break;
    } catch {}
  }
  if (readFileSync(`/proc/${proc.pid}/comm`, "utf8").trim() !== "dyno") throw new Error("pid is not dyno");
  return proc;
}

async function stop(proc: Bun.Subprocess) {
  proc.kill("SIGTERM");
  await Promise.race([proc.exited, sleep(7000)]);
  if (proc.exitCode === null) proc.kill("SIGKILL");
  await proc.exited;
}

// scrape fetches /metrics every everyMs until the returned stop is awaited,
// counting scrapes and failures.
function scrape(everyMs: number) {
  let done = false;
  const n = { ok: 0, failed: 0 };
  const loop = (async () => {
    while (!done) {
      try {
        const r = await fetch(`http://${METRICS_ADDR}/metrics`);
        await r.arrayBuffer();
        if (r.ok) n.ok++;
        else n.failed++;
      } catch {
        n.failed++;
      }
      await sleep(everyMs);
    }
  })();
  return async () => {
    done = true;
    await loop;
    return n;
  };
}

// memSampler scrapes /metrics every everyMs and reads the server's RssAnon in
// the same moment, until the returned stop is awaited. It returns each
// minute's lowest live heap and lowest anonymous memory outside Go (RssAnon
// minus Go's mapped memory not yet returned to the OS), from minute 2 on, and
// the least-squares slope through each set of floors.
function memSampler(pid: number, everyMs: number) {
  let done = false;
  const n = { ok: 0, failed: 0 };
  const pts: { t: number; live: number; outside: number }[] = [];
  const t0 = performance.now();
  const gauge = (text: string, name: string) => Number(text.match(new RegExp(`^${name} (\\S+)$`, "m"))?.[1] ?? NaN);
  const loop = (async () => {
    while (!done) {
      try {
        const r = await fetch(`http://${METRICS_ADDR}/metrics`);
        const text = await r.text();
        const anonKiB = Number(readFileSync(`/proc/${pid}/status`, "utf8").match(/^RssAnon:\s+(\d+)/m)?.[1] ?? NaN);
        const live = gauge(text, "go_gc_heap_live_bytes");
        const goResident = gauge(text, "go_memory_classes_total_bytes") - gauge(text, "go_memory_classes_heap_released_bytes");
        if (!r.ok || [anonKiB, live, goResident].some(Number.isNaN)) throw new Error("bad scrape");
        n.ok++;
        pts.push({ t: (performance.now() - t0) / 1000, live: live / 1024, outside: anonKiB - goResident / 1024 });
      } catch {
        n.failed++;
      }
      await sleep(everyMs);
    }
  })();
  const floors = (key: "live" | "outside") => {
    const byMin = new Map<number, number[]>();
    for (const p of pts) {
      const m = Math.floor(p.t / 60);
      if (m >= 2) byMin.set(m, [...(byMin.get(m) ?? []), p[key]]);
    }
    const full = (60_000 / everyMs) / 2; // a minute counts once half its samples are in
    return [...byMin].filter(([, v]) => v.length >= full).map(([m, v]) => [m, Math.round(Math.min(...v))] as [number, number]);
  };
  const slope = (xy: [number, number][]) => {
    const k = xy.length;
    const sx = xy.reduce((s, [x]) => s + x, 0), sy = xy.reduce((s, [, y]) => s + y, 0);
    const sxx = xy.reduce((s, [x]) => s + x * x, 0), sxy = xy.reduce((s, [x, y]) => s + x * y, 0);
    return (k * sxy - sx * sy) / (k * sxx - sx * sx);
  };
  return async () => {
    done = true;
    await loop;
    const live = floors("live"), outside = floors("outside");
    return { scrapes: n, liveFloors: live, outsideFloors: outside, liveSlope: slope(live), outsideSlope: slope(outside) };
  };
}

const scrapes = { ok: 0, failed: 0 };
const addScrapes = (n: { ok: number; failed: number }) => {
  scrapes.ok += n.ok;
  scrapes.failed += n.failed;
};

async function loadgen(mode: string, flags: Record<string, string | number>, cpus = CLIENT_CPUS) {
  const argv = pinned(cpus, [LOADGEN, mode, ...Object.entries(flags).map(([k, v]) => `-${k}=${v}`)]);
  const p = Bun.spawn(argv, { stdout: "pipe", stderr: "inherit" });
  const text = await new Response(p.stdout).text();
  if ((await p.exited) !== 0) throw new Error(`loadgen ${mode} failed`);
  return JSON.parse(text.trim().split("\n").pop()!);
}

type Row = { check: string; value: string; budget: string; pass: boolean | null };
const report: Row[] = [];
const measured: Record<string, unknown> = {};
const gate = (check: string, value: number, limit: number, unit: string, digits = 1) =>
  report.push({ check, value: `${value.toFixed(digits)} ${unit}`, budget: `≤ ${limit} ${unit}`, pass: value <= limit });
const info = (check: string, value: string) => report.push({ check, value, budget: "", pass: null });

// 1. Idle RSS, then peak RSS with 32 download and then 32 upload streams.
{
  const proc = await start();
  const pid = proc.pid;
  try {
    const idleS = FULL ? 60 : 10;
    log(`idle RSS: waiting ${idleS} s`);
    await sleep(idleS * 1000);
    const idle = procStatus(pid);
    gate(`idle RSS after ${idleS} s`, mib(idle.VmRSS!), BUDGET.idleRssMiB, "MiB");
    measured.idle = idle;

    log("peak RSS: 32 download streams, then 32 upload streams");
    await Bun.write(`/proc/${pid}/clear_refs`, "5"); // reset VmHWM
    const w = { pid, c: 32, bytes: 32 << 20, warmup: "1s", dur: FULL ? "6s" : "4s" };
    const stopScrape = scrape(1000);
    const dl = await loadgen("dl", { addr: ADDR, ...w });
    const ul = await loadgen("ul", { addr: ADDR, ...w });
    addScrapes(await stopScrape());
    const peak = procStatus(pid).VmHWM!;
    gate("peak RSS, 32 streams", mib(peak), BUDGET.peakRssMiB, "MiB");
    info("download, 32 streams", `${dl.gbit_s.toFixed(1)} Gbit/s, ${dl.cpu_s_per_gb.toFixed(3)} CPU-s/GB, ${dl.errors} errors`);
    info("upload, 32 streams", `${ul.gbit_s.toFixed(1)} Gbit/s, ${ul.cpu_s_per_gb.toFixed(3)} CPU-s/GB, ${ul.errors} errors`);
    measured.peak_rss_kib = peak;
    measured.dl_c32 = dl;
    measured.ul_c32 = ul;

    if (FULL) {
      log("CPU and ping at 1 Gbit/s");
      const paced = { pid, c: 8, bytes: 32 << 20, mbps: 1000, warmup: "2s", dur: "10s" };
      const dl1g = await loadgen("dl", { addr: ADDR, ...paced });
      const ul1g = await loadgen("ul", { addr: ADDR, ...paced });
      const pingIdle = await loadgen("ping", { addr: ADDR, n: 5000 });
      const bg = loadgen("dl", { addr: ADDR, ...paced, dur: "6s", warmup: "1s" });
      await sleep(1500);
      const pingLoaded = await loadgen("ping", { addr: ADDR, n: 5000 });
      await bg;
      info("server CPU at 1 Gbit/s, down / up", `${dl1g.cpu_util_pct.toFixed(1)}% / ${ul1g.cpu_util_pct.toFixed(1)}%`);
      info("ping p50 / p99, idle", `${pingIdle.p50_us.toFixed(0)} / ${pingIdle.p99_us.toFixed(0)} µs`);
      info("ping p50 / p99, 8 streams at 1 Gbit/s", `${pingLoaded.p50_us.toFixed(0)} / ${pingLoaded.p99_us.toFixed(0)} µs`);
      Object.assign(measured, { dl_paced1g: dl1g, ul_paced1g: ul1g, ping_idle: pingIdle, ping_paced1g: pingLoaded });
    }
  } finally {
    await stop(proc);
  }
}

// 2. 1000 connections that send half a header and stall. The connection cap
// (ADR-0007) admits them in batches, so give the header timeout room to clear
// every batch.
{
  const proc = await start();
  try {
    log("stalled connections: 1000 half-sent headers, 50 s");
    const h = await loadgen("hold", { addr: ADDR, pid: proc.pid, n: 1000, dur: "50s" });
    report.push({
      check: "stalled connections closed by timeout",
      value: `${h.closed_by_server} of ${h.n}, fresh request ${h.fresh_request_ok ? "served" : "NOT served"}`,
      budget: "all closed, fresh request served",
      pass: h.closed_by_server === h.n && h.fresh_request_ok,
    });
    info("RSS with stalled connections held", `${mib(h.rss_held_kib).toFixed(1)} MiB, after release ${mib(h.rss_released_kib).toFixed(1)} MiB`);
    measured.hold = h;
  } finally {
    await stop(proc);
  }
}

// 3. Soak: 12 minutes of mixed traffic. A leak is growth without a ceiling, so
// the gate is on the two numbers a leak has to raise and nothing else does:
// Go's live heap after collection, and anonymous memory outside Go's own
// accounting (SQLite's allocator, say). Each minute contributes its lowest
// reading, so data in flight at a collection doesn't count, and the slope is
// fitted through those floors after the 2-minute warmup. RSS itself moves in
// steps as Go returns pages to the OS and takes them back (ADR-0006), so it
// has a ceiling, not a slope budget.
//
// SQLite's memory fills to a ceiling of about 4.5 MiB over the first ~6,000
// stored results, then grows a few bytes per result. The soak stores only 360,
// so the database is filled first and the soak measures the state a server
// reaches after its first weeks, not the fill.
const SEED_RESULTS = 8000;
const SEED_BODY = `{"test_id":"seed","client":"footprint","device":"footprint-seed","download_mbps":938.2,"upload_mbps":412.6,"idle_ms":8.4,"loaded_down_ms":21,"loaded_up_ms":39.5,"jitter_ms":0.8,"streams":5,"client_limited":false}`;
if (FULL) {
  const proc = await start();
  try {
    log(`soak: storing ${SEED_RESULTS} results first`);
    for (let i = 0; i < SEED_RESULTS; i++) {
      const r = await fetch(`http://${ADDR}/api/v1/results`, { method: "POST", body: SEED_BODY, headers: { "content-type": "application/json" } });
      await r.text();
      if (r.status !== 201) throw new Error(`seeding results: HTTP ${r.status}`);
      if (i % 4 === 0) await (await fetch(`http://${ADDR}/api/v1/results?limit=50`)).text();
    }
    log("soak: 12 minutes of mixed traffic");
    const mem = memSampler(proc.pid, 2000);
    const s = await loadgen("soak", { addr: ADDR, pid: proc.pid, dur: "720s", sample: "5s" });
    const m = await mem();
    addScrapes(m.scrapes);
    gate("soak live heap slope, per-minute floors", m.liveSlope, BUDGET.leakKiBPerMin, "KiB/min");
    gate("soak memory outside Go, slope", m.outsideSlope, BUDGET.leakKiBPerMin, "KiB/min");
    gate("soak RSS max", mib(s.rss_max_kib), BUDGET.peakRssMiB, "MiB");
    const errs = Object.entries(s.errors as Record<string, number>).map(([k, v]) => `${k} ${v}`).join(", ") || "none";
    info("soak RSS first → last; slope after 2 min", `${mib(s.rss_first_kib).toFixed(1)} → ${mib(s.rss_last_kib).toFixed(1)} MiB; ${s.slope_kib_per_min_after_2m.toFixed(0)} KiB/min; errors: ${errs}`);
    const { samples: _, ...soak } = s;
    measured.soak = { ...soak, live_heap_floor_kib_by_min: m.liveFloors, outside_go_floor_kib_by_min: m.outsideFloors, live_heap_slope_kib_per_min: m.liveSlope, outside_go_slope_kib_per_min: m.outsideSlope };
  } finally {
    await stop(proc);
  }
} else {
  info("soak leak check", "skipped in quick mode; `make bench` runs it");
}

report.push({
  check: "metrics scrapes during load",
  value: `${scrapes.ok} ok, ${scrapes.failed} failed`,
  budget: "none failed",
  pass: scrapes.ok > 0 && scrapes.failed === 0,
});
measured.metrics_scrapes = scrapes;
info("binary size", `${(statSync(DYNO).size / 2 ** 20).toFixed(2)} MiB`);
info("CPU pinning", pin ? `server ${SERVER_CPUS}, clients ${CLIENT_CPUS}` : "none (fewer than 8 cores or no taskset)");

console.log(`\n| check | measured | budget | |\n|---|---|---|---|`);
for (const r of report) console.log(`| ${r.check} | ${r.value} | ${r.budget} | ${r.pass === null ? "" : r.pass ? "pass" : "**FAIL**"} |`);

if (FULL) {
  const sha = (await $`git rev-parse --short HEAD`.text()).trim();
  const dirty = (await $`git status --porcelain`.text()).trim() !== "";
  const row = { at: new Date().toISOString(), commit: sha + (dirty ? "-dirty" : ""), version: (await $`${DYNO} -version`.text()).trim(), kernel: (await $`uname -r`.text()).trim(), cores, report, measured };
  appendFileSync("bench/results/dyno-footprint.jsonl", JSON.stringify(row) + "\n");
  log("recorded in bench/results/dyno-footprint.jsonl");
}

const failed = report.filter((r) => r.pass === false);
if (failed.length) {
  console.error(`\nfootprint: ${failed.length} budget(s) exceeded (ADR-0006)`);
  process.exit(1);
}
console.error("\nfootprint: every budget met");
