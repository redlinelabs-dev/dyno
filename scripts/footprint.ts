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
// The soak slope budget is defined over a 10-minute soak, so the quick mode
// skips it rather than guess from a short run.

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

// ADR-0006 budgets. Change them there first, with measurements.
const BUDGET = {
  idleRssMiB: 24,
  peakRssMiB: 48,
  soakSlopeKiBPerMin: 16,
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
    env: { ...process.env, DYNO_LISTEN: ADDR, DYNO_DATA: DATA, GOMEMLIMIT: "64MiB" },
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
    const dl = await loadgen("dl", { addr: ADDR, ...w });
    const ul = await loadgen("ul", { addr: ADDR, ...w });
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

// 3. Soak: 12 minutes of mixed traffic, slope fitted after the 2-minute warmup.
if (FULL) {
  const proc = await start();
  try {
    log("soak: 12 minutes of mixed traffic");
    const s = await loadgen("soak", { addr: ADDR, pid: proc.pid, dur: "720s", sample: "5s" });
    gate("soak RSS slope after 2 min warmup", s.slope_kib_per_min_after_2m, BUDGET.soakSlopeKiBPerMin, "KiB/min");
    const errs = Object.entries(s.errors as Record<string, number>).map(([k, v]) => `${k} ${v}`).join(", ") || "none";
    info("soak RSS first → last, max", `${mib(s.rss_first_kib).toFixed(1)} → ${mib(s.rss_last_kib).toFixed(1)} MiB, max ${mib(s.rss_max_kib).toFixed(1)} MiB; errors: ${errs}`);
    const { samples: _, ...soak } = s;
    measured.soak = soak;
  } finally {
    await stop(proc);
  }
} else {
  info("soak RSS slope", "skipped in quick mode; `make bench` runs it");
}

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
