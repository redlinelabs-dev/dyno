// The web UI: prototypes/dyno-ui.html's Fast lane with Cluster's latency gauge
// (ADR-0004), driving the measurement worker (engine.ts). It talks to the
// server only through the public API in docs/api.md (ADR-0002).

import { ARC, CL, P, faceSVG, gaugeG, logMap, setNeedle } from "./gauge";
import type { Detail, Phase, Plan, StartMsg, StoredResult, Summary, WorkerMsg } from "./types";

// ---------- settings ----------
const PROFILES = {
  quick: { label: "Quick", scale: 0.4 },
  standard: { label: "Standard", scale: 1 },
  thorough: { label: "Thorough", scale: 1.5 },
};
type Profile = keyof typeof PROFILES;
interface Settings {
  profile: Profile;
  streams: string; // "auto" or a count
  planDown: number; // Mbps; the dial's red zone starts here
  unit: "Mbps" | "MBps";
}
const KEY = "dyno.settings";
const DEFAULTS: Settings = { profile: "standard", streams: "auto", planDown: 1000, unit: "Mbps" };
const S: Settings = loadSettings();

function loadSettings(): Settings {
  try {
    const s = { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) ?? "{}") };
    if (!(s.profile in PROFILES)) s.profile = DEFAULTS.profile;
    if (!(s.planDown > 0)) s.planDown = DEFAULTS.planDown;
    return s;
  } catch {
    return { ...DEFAULTS };
  }
}
function saveSettings() {
  try {
    localStorage.setItem(KEY, JSON.stringify(S));
  } catch {
    // Private mode or storage off: settings last for this visit only.
  }
}

// ---------- state ----------
type Status = "ready" | "running" | "done" | "failed";
type Tab = "latency" | "speed" | "setup" | "history";

interface Run {
  phase: Phase | "done";
  streams: number;
  auto: boolean;
  expected: number; // seconds the whole test should take
  idleMs: number | null;
  jitterMs: number | null;
  probeMs: number | null; // latest probe
  busyMax: number | null; // highest probe while loaded
  live: number; // current direction's rate, Mbps
  down: number | null;
  up: number | null;
  loadedDown: number | null;
  loadedUp: number | null;
  detail: Detail;
  summary: Summary | null;
  saved: StoredResult | null;
}

const A = {
  status: "ready" as Status,
  run: null as Run | null,
  worker: null as Worker | null,
  sheet: null as Tab | null,
  opener: null as HTMLElement | null,
  msg: "",
  warn: false,
  dial: 0, // displayed dial position, 0..1, eased toward the value
};

const newRun = (): Run => ({
  phase: "latency",
  streams: 0,
  auto: S.streams === "auto",
  expected: 0,
  idleMs: null,
  jitterMs: null,
  probeMs: null,
  busyMax: null,
  live: 0,
  down: null,
  up: null,
  loadedDown: null,
  loadedUp: null,
  detail: { idle: [], rate: [], probes: [] },
  summary: null,
  saved: null,
});

// ---------- test control ----------
async function start() {
  if (A.status === "running") return;
  const run = newRun();
  A.run = run;
  A.status = "running";
  note("");
  update();

  let plan: Plan;
  try {
    plan = await getJSON<Plan>("api/v1/plan");
  } catch (err) {
    return fail(`Couldn't reach the server: ${(err as Error).message}`);
  }
  if (A.run !== run || A.status !== "running") return;

  const want = S.streams === "auto" ? Infinity : Number(S.streams) || Infinity;
  run.streams = Math.max(1, Math.min(want, plan.download.streams, plan.upload.streams));
  const scale = PROFILES[S.profile].scale;
  run.expected = 1 + ((plan.download.duration_ms + plan.upload.duration_ms) * scale) / 1000;

  const w = new Worker("engine.js");
  A.worker = w;
  w.onmessage = (e: MessageEvent<WorkerMsg>) => {
    if (A.worker === w) onWorker(run, plan, e.data);
  };
  w.onerror = (e) => {
    e.preventDefault();
    if (A.worker === w) fail(e.message || "The test couldn't run in this browser.");
  };
  const msg: StartMsg = { type: "start", plan, origin: location.origin, streams: run.streams, scale };
  w.postMessage(msg);
}

function onWorker(run: Run, plan: Plan, m: WorkerMsg) {
  const d = run.detail;
  switch (m.type) {
    case "phase":
      run.phase = m.phase;
      run.live = 0;
      break;
    case "idle":
      run.idleMs = m.median;
      run.jitterMs = m.jitter;
      run.probeMs = m.ms;
      d.idle.push([m.t, m.ms]);
      break;
    case "rate":
      run.live = m.mbps;
      d.rate.push([m.t, m.mbps, m.dir === "download" ? 0 : 1]);
      break;
    case "probe":
      run.probeMs = m.ms;
      run.busyMax = Math.max(run.busyMax ?? 0, m.ms);
      d.probes.push([m.t, m.ms, m.dir === "download" ? 0 : 1]);
      break;
    case "direction-done":
      if (m.dir === "download") [run.down, run.loadedDown] = [m.mbps, m.loaded_ms];
      else [run.up, run.loadedUp] = [m.mbps, m.loaded_ms];
      break;
    case "done":
      finish(run, plan, m.summary, m.detail);
      return;
    case "error":
      fail(m.message);
      return;
  }
  update();
}

async function finish(run: Run, plan: Plan, summary: Summary, detail: Detail) {
  stopWorker();
  Object.assign(run, {
    phase: "done",
    summary,
    detail,
    down: summary.download_mbps,
    up: summary.upload_mbps,
    idleMs: summary.idle_ms,
    jitterMs: summary.jitter_ms,
    loadedDown: summary.loaded_down_ms,
    loadedUp: summary.loaded_up_ms,
  });
  A.status = "done";
  if (summary.client_limited) note("This device was working hard during the test and may have held the result back. A faster device may measure higher.", true);
  announce(
    `Test done. Download ${fmt(summary.download_mbps)} ${unitWords()}, upload ${fmt(summary.upload_mbps)}. ` +
      `Latency ${fms(summary.idle_ms)} milliseconds idle, ${fms(busyOf(run))} when busy.`,
  );
  update(true);
  try {
    run.saved = await getJSON<StoredResult>(plan.endpoints.results.replace(/^\//, ""), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ test_id: plan.test_id, client: clientLabel(), ...summary, detail }),
    });
  } catch (err) {
    note(`The result wasn't saved: ${(err as Error).message}`, true);
  }
  if (A.run === run) update(true);
}

function fail(message: string) {
  stopWorker();
  if (A.run) A.run.live = 0;
  A.status = "failed";
  note(message, true);
  announce(message);
  update(true);
}

function stopWorker() {
  A.worker?.terminate();
  A.worker = null;
}

// Background tabs get their timers throttled, which wrecks a measurement, so
// a test only starts while the page is visible and stops if it goes hidden.
let autoStart = true;
function maybeAutoStart() {
  if (!autoStart || document.hidden) return;
  autoStart = false;
  setTimeout(() => {
    if (document.hidden) autoStart = true;
    else if (A.status === "ready") start();
  }, 600);
}
document.addEventListener("visibilitychange", () => {
  if (document.hidden && A.status === "running")
    fail("The test stopped because this tab went to the background, where browsers slow it down. Press Test again.");
  else maybeAutoStart();
});

// ---------- formatting ----------
const unit = () => (S.unit === "MBps" ? "MB/s" : "Mbps");
const unitWords = () => (S.unit === "MBps" ? "megabytes per second" : "megabits per second");
function fmt(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return "–";
  if (S.unit === "MBps") v /= 8;
  return v >= 100 ? Math.round(v).toLocaleString("en-US") : v >= 10 ? v.toFixed(0) : v.toFixed(1);
}
const fms = (v: number | null | undefined) => (v == null || Number.isNaN(v) ? "–" : v < 10 ? v.toFixed(1) : String(Math.round(v)));
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

/** Latency under load: the worse direction, as the server grades it. */
function busyOf(run: Run): number | null {
  const v = Math.max(run.loadedDown ?? 0, run.loadedUp ?? 0);
  return v > 0 ? v : null;
}

/** "Chrome on Android" and the like, for the History tab's From column. */
function clientLabel(): string {
  const ua = navigator.userAgent;
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /OPR\//.test(ua)
      ? "Opera"
      : /Firefox\/|FxiOS/.test(ua)
        ? "Firefox"
        : /Chrome\/|CriOS/.test(ua)
          ? "Chrome"
          : /Safari\//.test(ua)
            ? "Safari"
            : "Browser";
  const os = /Android/.test(ua)
    ? "Android"
    : /iPhone|iPod/.test(ua)
      ? "iPhone"
      : /iPad/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)
        ? "iPad"
        : /CrOS/.test(ua)
          ? "ChromeOS"
          : /Windows/.test(ua)
            ? "Windows"
            : /Macintosh/.test(ua)
              ? "Mac"
              : /Linux/.test(ua)
                ? "Linux"
                : "";
  return os ? `${browser} on ${os}` : browser;
}

async function getJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, { cache: "no-store", ...init });
  const body = await res.json().catch(() => null);
  if (!res.ok) throw new Error(body?.error ?? `HTTP ${res.status}`);
  return body as T;
}

// ---------- fast lane ----------
const FR = 170;
const FCX = 200;
const FCY = 200;
const fAng = (v: number) => -120 + 240 * logMap(v);
const TRACK = ARC(FCX, FCY, FR, -120, 120);
const zone = () => ARC(FCX, FCY, FR + 19, fAng(S.planDown), 120);

const GRADE_TEXT: Record<string, string> = {
  "A+": "No slowdown when the line is busy. Calls and games stay smooth during big downloads.",
  A: "Barely any slowdown when the line is busy. Calls and games stay smooth.",
  B: "Some slowdown when the line is busy. A video call may stutter while someone uploads a big file.",
  C: "Noticeable lag when the line is busy. Turning on smart queue management (SQM) in your router usually fixes this.",
  D: "Heavy lag when the line is busy. Turn on smart queue management (SQM) in your router.",
  F: "Severe lag when the line is busy. Calls and games will suffer. Turn on SQM in your router.",
};

const TABS: [Tab, string, string][] = [
  ["latency", "Latency", "Latency"],
  ["speed", "Speed over time", "Speed"],
  ["setup", "Setup", "Setup"],
  ["history", "History", "History"],
];

const stage = document.getElementById("stage")!;
stage.innerHTML = `<div class="v" id="view">
  <header class="v-top"><span class="v-logo"><span class="v-brand">DYNO<b>/</b></span><span class="v-by">by Redline Labs</span></span><span class="v-host">This server (${esc(location.host)})</span></header>
  <div class="f-dial"><svg viewBox="0 0 400 310" aria-hidden="true">
    <path class="f-zone" id="f-zone" d="${zone()}"/><path class="f-track" d="${TRACK}"/>
    <path class="f-fill" id="f-fill" d="${TRACK}" pathLength="1" stroke-dasharray="0 1"/>
    <path class="f-over" id="f-over" d="${TRACK}" pathLength="1" stroke-dasharray="0 1"/>
    ${[1, 10, 100, 1000, 10000]
      .map((v, i) => {
        const [x, y] = P(FCX, FCY, FR - 34, fAng(v));
        return `<text class="f-lab" x="${x.toFixed(1)}" y="${y.toFixed(1)}">${["1", "10", "100", "1G", "10G"][i]}</text>`;
      })
      .join("")}
    <circle class="f-tip" id="f-tip" r="10" cx="0" cy="0"/></svg>
    <div class="f-center"><span class="f-phase"><i></i><span id="f-ph">Download speed</span></span><span class="f-val" id="f-val">–</span><span class="f-unit" id="f-unit">Mbps</span></div>
  </div>
  <div class="f-sub">
    <button class="chip" data-sheet="latency" aria-label="Latency, open details"><span>Latency</span><b id="f-li">–</b><span>ms idle</span><i class="sep"></i><b id="f-lb">–</b><span>ms busy</span></button>
    <button class="chip" data-sheet="speed" aria-label="Upload, open details"><span>Upload</span><b id="f-u">–</b><span id="f-uu">Mbps</span></button>
  </div>
  <div class="f-act"><button class="go" id="f-go">Start test</button><button class="tele-link" data-sheet="latency">Telemetry</button></div>
  <p class="f-msg" id="f-msg" hidden></p>
</div>
<div class="sheet-wrap" id="sheet" hidden><button class="sheet-back" data-close aria-label="Close telemetry" tabindex="-1"></button>
  <section class="sheet" role="dialog" aria-modal="true" aria-label="Telemetry"><div class="sheet-grab"></div>
    <div class="sheet-head"><div class="tabs" role="tablist">${TABS.map(
      ([k, long, short]) => `<button role="tab" data-tab="${k}"><span class="long">${long}</span><span class="short">${short}</span></button>`,
    ).join("")}</div><button class="sheet-x" data-close>Close</button></div>
    <div id="sheet-body"></div></section></div>
<p class="sr" id="live" aria-live="polite"></p>`;

const view = document.getElementById("view")!;
const q = <T extends Element = HTMLElement>(sel: string) => stage.querySelector<T>(sel)!;
q<HTMLButtonElement>("#f-go").onclick = start;

function note(msg: string, warn = false) {
  A.msg = msg;
  A.warn = warn;
}
function announce(msg: string) {
  q("#live").textContent = msg;
}

// Renders are batched into animation frames; the dial eases toward its value.
let raf = 0;
let dirty = false;
let sheetDirty = false;
function update(full = false) {
  dirty = true;
  if (full) sheetDirty = true;
  if (!raf) raf = requestAnimationFrame(frame);
}
const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)");

function frame() {
  raf = 0;
  if (dirty) {
    dirty = false;
    render();
    drawSheet(sheetDirty);
    sheetDirty = false;
  }
  const target = logMap(dialValue());
  A.dial = reduceMotion.matches ? target : A.dial + (target - A.dial) * 0.22;
  if (Math.abs(target - A.dial) < 0.0005) A.dial = target;
  else raf = requestAnimationFrame(frame);
  drawDial(A.dial);
}

function dialValue(): number {
  const run = A.run;
  if (!run) return 0;
  if (run.phase === "download") return run.live;
  if (run.phase === "latency") return 0;
  return run.down ?? 0;
}

function drawDial(f: number) {
  const fp = logMap(S.planDown);
  q("#f-fill").setAttribute("stroke-dasharray", `${Math.min(f, fp).toFixed(4)} 1`);
  q("#f-over").setAttribute("stroke-dasharray", f > fp ? `0 ${fp.toFixed(4)} ${(f - fp).toFixed(4)} 1` : "0 1");
  const [tx, ty] = P(FCX, FCY, FR, -120 + 240 * f);
  q("#f-tip").setAttribute("cx", tx.toFixed(1));
  q("#f-tip").setAttribute("cy", ty.toFixed(1));
}

function render() {
  const run = A.run;
  const running = A.status === "running";
  const phase = run?.phase;
  view.classList.toggle("running", running);
  q("#f-ph").textContent = !running ? "Download speed" : phase === "latency" ? "Connecting" : phase === "download" ? "Measuring download" : "Download speed";
  const val = q("#f-val");
  val.textContent = !run ? "–" : phase === "latency" ? (running ? "0" : "–") : fmt(dialValue());
  val.classList.toggle("long", val.textContent.length > 3); // multi-gigabit figures would cover the dial labels
  q("#f-unit").textContent = unit();
  q("#f-uu").textContent = unit();
  q("#f-li").textContent = fms(run?.idleMs);
  q("#f-lb").textContent = fms(!run ? null : running ? (phase !== "latency" ? run.probeMs : null) : busyOf(run));
  q("#f-u").textContent = phase === "upload" && running ? fmt(run!.live) : fmt(run?.up);
  const go = q<HTMLButtonElement>("#f-go");
  go.disabled = running;
  go.textContent = running ? "Testing…" : A.status === "ready" ? "Start test" : "Test again";
  const msg = q("#f-msg");
  msg.hidden = !A.msg;
  msg.textContent = A.msg;
  msg.classList.toggle("warn", A.warn);
}

// ---------- telemetry sheet ----------
function drawSheet(full: boolean) {
  const w = q("#sheet");
  w.hidden = !A.sheet;
  view.inert = !!A.sheet;
  if (!A.sheet) return;
  w.querySelectorAll<HTMLElement>("[data-tab]").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.tab === A.sheet)));
  const b = q("#sheet-body");
  if (A.sheet === "latency") drawLatency(b, full);
  else if (A.sheet === "speed") b.innerHTML = timeline();
  else if (full && A.sheet === "setup") b.innerHTML = setupBody();
  else if (full && A.sheet === "history") drawHistory(b);
}

function openSheet(tab: Tab, opener?: HTMLElement) {
  if (!A.sheet && opener) A.opener = opener;
  A.sheet = tab;
  drawSheet(true);
  q(".sheet-x").focus({ preventScroll: true });
}
function closeSheet() {
  A.sheet = null;
  drawSheet(false);
  A.opener?.focus({ preventScroll: true });
  A.opener = null;
}

function drawLatency(b: HTMLElement, full: boolean) {
  const run = A.run;
  const running = A.status === "running";
  if (full || !b.querySelector("#lat-side")) {
    b.innerHTML = `<div class="latgrid">
      <div class="lg"><svg viewBox="-6 -6 212 212" role="img" aria-label="Latency gauge. The needle is latency now; the orange marker is the highest latency while the line was busy.">${faceSVG(100, 100, 88)}${gaugeG(CL)}</svg>
        <span class="lg-read"><span id="lg-v">–</span><small>ms</small></span><span class="lg-busy" id="lg-b"></span></div>
      <div class="latside" id="lat-side"></div></div>`;
  }
  b.querySelector("#lat-side")!.innerHTML = latSide(run);
  const lv = !run ? null : running ? run.probeMs : run.idleMs;
  const ghost = !run ? null : running ? run.busyMax : busyOf(run);
  setNeedle(b, CL, lv ?? 0);
  setNeedle(b, CL, ghost ?? 0, "cl-ghost");
  b.querySelector<SVGElement>("#cl-ghost")!.style.opacity = ghost ? "1" : "0";
  b.querySelector("#lg-v")!.textContent = fms(lv);
  b.querySelector("#lg-b")!.textContent = running
    ? run?.busyMax
      ? `peak ${fms(run.busyMax)} ms while busy`
      : ""
    : run && busyOf(run) != null
      ? `${fms(busyOf(run))} ms when busy`
      : "";
}

function latSide(run: Run | null): string {
  const grade = run?.saved?.grade || null;
  const why = grade
    ? (GRADE_TEXT[grade] ?? "")
    : run?.saved
      ? "Not enough latency samples under load to grade this test."
      : A.status === "done"
        ? "Not graded: the result wasn't saved."
        : "Appears when the test finishes.";
  const rpm = run?.saved?.rpm;
  return `<div class="gradebox"><span class="grade">${grade ?? "…"}</span><p><b>Bufferbloat grade</b>${why}</p></div>
    <dl class="kv"><dt>Idle latency</dt><dd>${fms(run?.idleMs)} ms</dd><dt>While downloading</dt><dd>${fms(run?.loadedDown || null)} ms</dd>
      <dt>While uploading</dt><dd>${fms(run?.loadedUp || null)} ms</dd><dt>Jitter</dt><dd>${fms(run?.jitterMs)} ms</dd>
      <dt>Responsiveness</dt><dd>${rpm ? `${rpm.toLocaleString("en-US")} RPM` : "–"}</dd><dt>Server</dt><dd>${esc(location.host)}</dd></dl>`;
}

function timeline(): string {
  const run = A.run;
  if (!run || (!run.detail.rate.length && !run.detail.idle.length)) return `<div class="pnlx"><p class="note">Run a test to see speed and latency over time.</p></div>`;
  const W = 600;
  const H = 150;
  const d = run.detail;
  const lat = [...d.idle, ...d.probes.map(([t, ms]) => [t, ms] as const)];
  let lastT = 0;
  let maxV = S.planDown;
  let maxMs = 60;
  for (const [t, v] of d.rate) [lastT, maxV] = [Math.max(lastT, t), Math.max(maxV, v)];
  for (const [t, ms] of lat) [lastT, maxMs] = [Math.max(lastT, t), Math.max(maxMs, ms)];
  const T = run.phase === "done" || A.status !== "running" ? lastT || 1 : Math.max(run.expected, lastT);
  maxV *= 1.05;
  maxMs *= 1.15;
  const x = (t: number) => ((t / T) * W).toFixed(1);
  const y = (v: number, max: number) => (H - (v / max) * (H - 4)).toFixed(1);
  const area = (dir: 0 | 1) => {
    const a = d.rate.filter((p) => p[2] === dir);
    return a.length < 2 ? "" : `M${x(a[0]![0])} ${H} ` + a.map(([t, v]) => `L${x(t)} ${y(v, maxV)}`).join(" ") + ` L${x(a[a.length - 1]![0])} ${H}Z`;
  };
  const line = lat.map(([t, ms], i) => `${i ? "L" : "M"}${x(t)} ${y(ms, maxMs)}`).join(" ");
  const peak = (dir: 0 | 1) => {
    const a = d.rate.filter((p) => p[2] === dir);
    return a.length ? Math.max(...a.map((p) => p[1])) : null;
  };
  const fromClient = d.upload_from_client
    ? `<p class="note">No upload request finished in time, so upload speed is this browser's own count rather than the server's.</p>`
    : "";
  return `<div class="pnlx"><svg class="tl" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img" aria-label="Speed and latency over the test"><path class="tl-d" d="${area(0)}"/><path class="tl-u" d="${area(1)}"/><path class="tl-l" d="${line}"/></svg>
  <ul class="legend"><li><i style="background:var(--c-down)"></i>Download</li><li><i style="background:var(--c-up)"></i>Upload</li><li><i style="background:var(--c-lat)"></i>Latency</li></ul>
  <dl class="kv"><dt>Peak download</dt><dd>${fmt(peak(0))} ${unit()}</dd><dt>Peak upload</dt><dd>${fmt(peak(1))} ${unit()}</dd><dt>Connections</dt><dd>${run.streams || "–"}${run.auto && run.streams ? ", auto" : ""}</dd></dl>${fromClient}</div>`;
}

function setupBody(): string {
  const seg = (name: string, opts: [string, string][], cur: string) =>
    `<div class="seg">${opts.map(([v, l]) => `<label><input type="radio" name="${name}" value="${v}"${cur === v ? " checked" : ""}><span>${l}</span></label>`).join("")}</div>`;
  const opt = (o: [string, string][], cur: string) => o.map(([v, l]) => `<option value="${v}"${cur === v ? " selected" : ""}>${l}</option>`).join("");
  const has = !!(A.run?.saved || A.run?.summary);
  return `<div class="pnlx"><div class="setgrid">
    <fieldset class="field"><legend>Test length</legend>${seg(
      "set-prof",
      Object.entries(PROFILES).map(([k, p]) => [k, p.label]),
      S.profile,
    )}<p class="note">Quick takes about 10 seconds, Standard about 20, Thorough about 30.</p></fieldset>
    <div class="field"><label for="set-streams">Parallel connections</label><select id="set-streams">${opt(
      [
        ["auto", "Auto (recommended)"],
        ["1", "1"],
        ["2", "2"],
        ["4", "4"],
        ["5", "5"],
      ],
      S.streams,
    )}</select><p class="note">Auto uses what the server's plan asks for, 5 by default. One connection shows what a single download gets.</p></div>
    <div class="field"><label for="set-plan">Your plan's download speed (Mbps)</label><input type="number" id="set-plan" min="1" value="${S.planDown}"><p class="note">The red zone starts here, so reaching it means you get what you pay for.</p></div>
    <fieldset class="field"><legend>Units</legend>${seg(
      "set-unit",
      [
        ["Mbps", "Mbps"],
        ["MBps", "MB/s"],
      ],
      S.unit,
    )}</fieldset>
  </div>
  <div class="exp"><button class="sbtn" data-copy${has ? "" : " disabled"}>Copy result as JSON</button><span class="note" aria-live="polite" data-copied></span></div></div>`;
}

async function drawHistory(b: HTMLElement) {
  b.innerHTML = `<div class="pnlx"><p class="note">Loading…</p></div>`;
  let list: StoredResult[];
  try {
    list = (await getJSON<{ results: StoredResult[] }>("api/v1/results?limit=50")).results;
  } catch (err) {
    if (A.sheet === "history") b.innerHTML = `<div class="pnlx"><p class="note">Couldn't load history: ${esc((err as Error).message)}</p></div>`;
    return;
  }
  if (A.sheet !== "history") return;
  if (!list.length) {
    b.innerHTML = `<div class="pnlx"><p class="note">No tests yet. Every test run against this server shows up here.</p></div>`;
    return;
  }
  const when = (iso: string) => new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  const from = (r: StoredResult) => (r.source === "client" ? r.client || "Browser" : r.source === "schedule" ? "Scheduled" : r.source);
  b.innerHTML = `<div class="pnlx"><div class="tw"><table class="hist"><thead><tr><th>When</th><th>Down (${unit()})</th><th>Up (${unit()})</th><th>Latency</th><th>Grade</th><th>From</th></tr></thead><tbody>${list
    .map(
      (r) =>
        `<tr><td>${esc(when(r.at))}</td><td class="n">${fmt(r.download_mbps)}</td><td class="n">${fmt(r.upload_mbps)}</td><td class="n">${fms(r.idle_ms)} ms</td><td>${esc(r.grade || "–")}</td><td>${esc(from(r))}</td></tr>`,
    )
    .join("")}</tbody></table></div><p class="note">Every test run against this server, newest first. Latency is idle latency.</p></div>`;
}

function resultJSON(): string {
  const run = A.run!;
  if (run.saved) {
    const { detail: _, ...rest } = run.saved;
    return JSON.stringify(rest, null, 2);
  }
  return JSON.stringify(run.summary, null, 2);
}

// ---------- events ----------
stage.addEventListener("click", async (e) => {
  const t = (e.target as Element).closest<HTMLElement>("[data-sheet],[data-close],[data-tab],[data-copy]");
  if (!t) return;
  if (t.dataset.sheet) openSheet(t.dataset.sheet as Tab, t);
  else if (t.dataset.tab) openSheet(t.dataset.tab as Tab);
  else if (t.hasAttribute("data-close")) closeSheet();
  else if (t.hasAttribute("data-copy")) {
    const out = t.parentElement!.querySelector("[data-copied]")!;
    try {
      await navigator.clipboard.writeText(resultJSON());
      out.textContent = "Copied";
    } catch {
      out.textContent = "Copying is blocked here. The result is in History and at /api/v1/results.";
    }
  }
});

stage.addEventListener("change", (e) => {
  const t = e.target as HTMLInputElement;
  if (t.name === "set-prof") S.profile = t.value as Profile;
  else if (t.name === "set-unit") S.unit = t.value as Settings["unit"];
  else if (t.id === "set-streams") S.streams = t.value;
  else if (t.id === "set-plan") {
    S.planDown = Math.max(1, Number(t.value) || DEFAULTS.planDown);
    q("#f-zone").setAttribute("d", zone());
  } else return;
  saveSettings();
  update();
});

addEventListener("keydown", (e) => {
  if (e.key === "Escape" && A.sheet) closeSheet();
});

update();
maybeAutoStart();
