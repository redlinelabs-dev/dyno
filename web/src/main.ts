// The web UI: prototypes/dyno-ui.html's Fast lane with Cluster's latency gauge
// (ADR-0004), driving the measurement worker (engine.ts). It talks to the
// server only through the public API in docs/api.md (ADR-0002).

import { ARC, CL, P, faceSVG, gaugeG, logMap, setNeedle } from "./gauge";
import { describe, draw, model, snap, step, type Hover, type Model } from "./chart";
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
  name: string; // this device's name in History; empty means the browser label
}
const KEY = "dyno.settings";
const DEFAULTS: Settings = { profile: "standard", streams: "auto", planDown: 1000, unit: "Mbps", name: "" };
const S: Settings = loadSettings();

function loadSettings(): Settings {
  try {
    const s = { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) ?? "{}") };
    if (!(s.profile in PROFILES)) s.profile = DEFAULTS.profile;
    if (!(s.planDown > 0)) s.planDown = DEFAULTS.planDown;
    if (typeof s.name !== "string") s.name = "";
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

// A random id this browser keeps for itself, so History can show its own tests
// (ADR-0022). Made with getRandomValues: randomUUID needs HTTPS, and a LAN
// server is often plain HTTP.
const DEVICE = (() => {
  const k = "dyno.device";
  try {
    const old = localStorage.getItem(k);
    if (old && /^[\w-]{16,64}$/.test(old)) return old;
  } catch {
    // Storage off: fall through to a fresh id.
  }
  const id = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16))))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  try {
    localStorage.setItem(k, id);
  } catch {
    // Storage off: this visit gets an id, the next one a new one.
  }
  return id;
})();

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
  hover: null as Hover | null, // Speed over time crosshair
  histAll: false, // History shows every device, not just this one
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
  announce("Speed test running.");
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
      if (m.span) d.spans = [...(d.spans ?? []), m.span];
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
      if (m.dir === "download") announce(`Download ${fmt(m.mbps)} ${unitWords()}. Measuring upload.`);
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
  if (summary.client_limited) note("This device was busy during the test and may have held the result back.", true);
  announce(
    `Test done. Download ${fmt(summary.download_mbps)} ${unitWords()}, upload ${fmt(summary.upload_mbps)}. ` +
      `Latency ${fms(summary.idle_ms)} milliseconds idle, ${fms(busyOf(run))} when busy.${summary.client_limited ? " " + A.msg : ""}`,
  );
  update(true);
  try {
    run.saved = await getJSON<StoredResult>(plan.endpoints.results.replace(/^\//, ""), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ test_id: plan.test_id, client: S.name.trim() || clientLabel(), device: DEVICE, ...summary, detail }),
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

/** The Stop button: anything that starts on its own and moves for this long must be stoppable (WCAG 2.2.2). */
function stop() {
  stopWorker();
  if (A.run) A.run.live = 0;
  A.status = "failed";
  note("Test stopped. Press Test again to run it.");
  announce("Test stopped.");
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

/** "Chrome on Android" and the like: a device's name in History until it's given one. */
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
  <header class="v-top"><h1 class="v-logo"><span class="v-brand" aria-hidden="true">DYNO<b>/</b></span><span class="sr">dyno speed test</span><span class="v-by">by Redline Labs</span></h1><span class="v-host">This server (${esc(location.host)})</span></header>
  <div class="v-main"><div class="fit">
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
    <line class="f-mark" id="f-mark" x1="0" y1="0" x2="0" y2="0"/>
    <circle class="f-tip" id="f-tip" r="10" cx="0" cy="0"/></svg>
    <div class="f-center"><span class="f-phase"><i></i><span id="f-ph">Download speed</span></span><span class="f-val none" id="f-val">–</span><span class="f-unit" id="f-unit">Mbps</span></div>
  </div>
  <div class="read">
    <button class="rg lat" data-sheet="latency" aria-haspopup="dialog"><span class="rg-h">Latency</span>
      <span class="rv"><span class="rl">Idle</span><span class="rn"><b id="f-li" class="none">–</b><small>ms</small></span></span>
      <span class="rv"><span class="rl">Busy</span><span class="rn"><b id="f-lb" class="none">–</b><small>ms</small></span></span></button>
    <button class="rg up" data-sheet="speed" aria-haspopup="dialog"><span class="rg-h">Upload</span>
      <span class="rv"><span class="rl"></span><span class="rn"><b id="f-u" class="wide none">–</b><small id="f-uu">Mbps</small></span></span></button>
  </div>
  <div class="f-act"><button class="go" id="f-go">Start test</button><button class="tele-link" data-sheet="latency" aria-haspopup="dialog">Telemetry</button></div>
  <p class="f-msg" id="f-msg"></p>
  </div></div>
</div>
<div class="sheet-wrap" id="sheet" hidden><div class="sheet-back" data-close></div>
  <section class="sheet" role="dialog" aria-modal="true" aria-label="Telemetry"><div class="sheet-grab"></div>
    <div class="sheet-head"><div class="tabs" role="tablist">${TABS.map(
      ([k, long, short]) =>
        `<button role="tab" id="tab-${k}" aria-controls="sheet-body" data-tab="${k}"><span class="long">${long}</span><span class="short">${short}</span></button>`,
    ).join("")}</div><button class="sheet-x" data-close>Close</button></div>
    <div id="sheet-body" role="tabpanel" tabindex="0"></div></section></div>
<p class="sr" id="live" aria-live="polite"></p>`;

const view = document.getElementById("view")!;
const q = <T extends Element = HTMLElement>(sel: string) => stage.querySelector<T>(sel)!;
q<HTMLButtonElement>("#f-go").onclick = () => (A.status === "running" ? stop() : start());

/**
 * Scales the fast lane (dial, readouts, actions) to the largest size that fits
 * under the header (ADR-0016). Small labels have minimum sizes, so the
 * composition's shape changes as it shrinks; a bisection finds the scale. On
 * short, wide screens (a phone on its side) the readouts may sit beside the
 * dial instead of under it, whichever gives the bigger dial.
 */
const fitEl = q(".fit");
const mainEl = q(".v-main");
function fit() {
  const { width: W, height: H } = mainEl.getBoundingClientRect();
  const scale = (row: boolean) => {
    fitEl.classList.toggle("row", row);
    const fits = (p: number) => {
      fitEl.style.setProperty("--p", `${p}px`);
      const w = Math.max(...[fitEl, ...fitEl.children].map((e) => e.getBoundingClientRect().width));
      return w <= W && fitEl.getBoundingClientRect().height <= H;
    };
    let [lo, hi] = [0.2, Math.max(0.2, W / 540)];
    for (let i = 0; i < 14; i++) {
      const mid = (lo + hi) / 2;
      if (fits(mid)) lo = mid;
      else hi = mid;
    }
    return lo;
  };
  const col = scale(false);
  const row = W > H * 1.5 ? scale(true) : 0;
  const useRow = row > col * 1.1;
  fitEl.classList.toggle("row", useRow);
  fitEl.style.setProperty("--p", `${useRow ? row : col}px`);
}
new ResizeObserver(fit).observe(mainEl);
document.fonts?.ready.then(fit);

/** Writes a live number with each digit in a fixed-width box (.tn), so it doesn't wobble as it changes. */
function setNum(el: Element, text: string) {
  if (el.getAttribute("data-v") === text) return;
  el.setAttribute("data-v", text);
  el.innerHTML = `<span aria-hidden="true">${text.replace(/\d/g, '<span class="tn">$&</span>')}</span><span class="sr">${text === "–" ? "none yet" : text}</span>`;
  el.classList.toggle("none", text === "–"); // no value yet: dim, so it doesn't read as one
}

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
  if (run.phase === "upload" && A.status === "running") return run.live;
  if (run.phase === "latency") return 0;
  return run.down ?? 0;
}
const uploading = () => A.run?.phase === "upload" && A.status === "running";

function drawDial(f: number) {
  const fp = uploading() ? 1 : logMap(S.planDown); // the plan zone is a download speed
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
  q(".f-dial").classList.toggle("up", uploading());
  q("#f-ph").textContent = !running ? "Download speed" : phase === "latency" ? "Connecting" : phase === "download" ? "Measuring download" : "Measuring upload";
  const mark = q("#f-mark");
  if (uploading() && run?.down) {
    const a = fAng(run.down);
    const [x1, y1] = P(FCX, FCY, FR - 15, a);
    const [x2, y2] = P(FCX, FCY, FR + 15, a);
    mark.setAttribute("x1", x1.toFixed(1));
    mark.setAttribute("y1", y1.toFixed(1));
    mark.setAttribute("x2", x2.toFixed(1));
    mark.setAttribute("y2", y2.toFixed(1));
  }
  mark.classList.toggle("on", uploading() && !!run?.down);
  const val = q("#f-val");
  setNum(val, !run ? "–" : running ? (phase === "latency" ? "0" : fmt(dialValue())) : fmt(run.down)); // a stopped test shows no download, not 0
  val.classList.toggle("long", (val.getAttribute("data-v") ?? "").length > 3); // multi-gigabit figures would cover the dial labels
  q("#f-unit").textContent = unit();
  q("#f-uu").textContent = unit();
  setNum(q("#f-li"), fms(run?.idleMs));
  setNum(q("#f-lb"), fms(!run ? null : running ? (phase !== "latency" ? run.probeMs : null) : busyOf(run)));
  setNum(q("#f-u"), fmt(run?.up)); // the dial shows upload while it runs
  const go = q<HTMLButtonElement>("#f-go");
  go.textContent = running ? "Stop test" : A.status === "ready" ? "Start test" : "Test again";
  const msg = q("#f-msg"); // always in the layout, so a message doesn't push the dial up
  msg.textContent = A.msg;
  msg.classList.toggle("warn", A.warn);
}

// ---------- telemetry sheet ----------
function drawSheet(full: boolean) {
  const w = q("#sheet");
  w.hidden = !A.sheet;
  view.inert = !!A.sheet;
  document.documentElement.classList.toggle("sheet-open", !!A.sheet);
  if (!A.sheet) return;
  w.querySelectorAll<HTMLElement>("[data-tab]").forEach((b) => {
    b.setAttribute("aria-selected", String(b.dataset.tab === A.sheet));
    b.tabIndex = b.dataset.tab === A.sheet ? 0 : -1;
  });
  const b = q("#sheet-body");
  b.setAttribute("aria-labelledby", `tab-${A.sheet}`);
  if (A.sheet === "latency") drawLatency(b, full);
  else if (A.sheet === "speed") drawSpeed(b, full);
  else if (full && A.sheet === "setup") b.innerHTML = setupBody();
  else if (full && A.sheet === "history") drawHistory(b);
}

function openSheet(tab: Tab, opener?: HTMLElement) {
  if (!A.sheet && opener) A.opener = opener;
  A.sheet = tab;
  drawSheet(true);
  q(`#tab-${tab}`).focus({ preventScroll: true });
}
function closeSheet() {
  A.sheet = null;
  drawSheet(false);
  sheetEl.style.translate = sheetEl.style.transition = q(".sheet-back").style.opacity = "";
  A.opener?.focus({ preventScroll: true });
  A.opener = null;
}

// Phones: drag the sheet down from its top to dismiss it, as its grab handle
// suggests. Close and Escape do the same without dragging (WCAG 2.5.7).
const sheetEl = q(".sheet");
const asDialog = matchMedia("(min-width: 700px) and (min-height: 560px)");
let drag: { x: number; y: number; dy: number; at: number; v: number; on: boolean | null; tabs: boolean } | null = null;
sheetEl.addEventListener(
  "touchstart",
  (e) => {
    const t = e.touches[0]!;
    // The tab strip also scrolls sideways, so a drag there waits a few pixels to show which way it goes.
    const tabs = !!(e.target as Element).closest(".tabs");
    drag = e.touches.length === 1 && !asDialog.matches ? { x: t.clientX, y: t.clientY, dy: 0, at: e.timeStamp, v: 0, on: null, tabs } : null;
  },
  { passive: true },
);
sheetEl.addEventListener(
  "touchmove",
  (e) => {
    if (!drag) return;
    const t = e.touches[0]!;
    const dx = t.clientX - drag.x;
    const dy = t.clientY - drag.y;
    if (drag.on === null) {
      // Only a downward drag that starts with the sheet scrolled to the top; anything else scrolls as usual.
      // Elsewhere it is decided on the first move: Safari ignores preventDefault once its own scroll has begun.
      if (drag.tabs ? Math.hypot(dx, dy) < 6 : !dx && !dy) return;
      drag.on = dy > 0 && dy >= Math.abs(dx) && sheetEl.scrollTop <= 0;
      if (!drag.on) return void (drag = null);
    }
    e.preventDefault();
    const d = Math.max(0, dy);
    drag.v = (d - drag.dy) / Math.max(1, e.timeStamp - drag.at);
    drag.dy = d;
    drag.at = e.timeStamp;
    sheetEl.style.transition = "none";
    sheetEl.style.translate = `0 ${d}px`;
    q(".sheet-back").style.opacity = String(1 - Math.min(1, d / sheetEl.offsetHeight));
  },
  { passive: false },
);
const dragEnd = () => {
  if (!drag?.on) return void (drag = null);
  const { dy, v } = drag;
  drag = null;
  const away = dy > Math.min(140, sheetEl.offsetHeight / 4) || (dy > 40 && v > 0.5);
  const ms = reduceMotion.matches ? 0 : away ? 160 : 200;
  sheetEl.style.transition = `translate ${ms}ms ease-out`;
  sheetEl.style.translate = away ? "0 100%" : "";
  q(".sheet-back").style.opacity = away ? "0" : "";
  if (away) setTimeout(closeSheet, ms);
  else setTimeout(() => (sheetEl.style.transition = ""), ms);
};
sheetEl.addEventListener("touchend", dragEnd);
sheetEl.addEventListener("touchcancel", dragEnd);

function drawLatency(b: HTMLElement, full: boolean) {
  const run = A.run;
  const running = A.status === "running";
  if (full || !b.querySelector("#lat-side")) {
    b.innerHTML = `<div class="latgrid">
      <div class="lg"><svg viewBox="-6 -6 212 212" role="img" aria-label="Latency gauge. The needle is latency now; the orange marker is the highest latency while the line was busy.">${faceSVG(100, 100, 88)}${gaugeG(CL)}</svg>
        <span class="lg-read"><span id="lg-v" class="lg-n none">–</span><small>ms</small></span><span class="lg-busy" id="lg-b"></span></div>
      <div class="latside" id="lat-side"></div></div>`;
  }
  b.querySelector("#lat-side")!.innerHTML = latSide(run);
  const lv = !run ? null : running ? run.probeMs : run.idleMs;
  const ghost = !run ? null : running ? run.busyMax : busyOf(run);
  setNeedle(b, CL, lv ?? 0);
  setNeedle(b, CL, ghost ?? 0, "cl-ghost");
  b.querySelector<SVGElement>("#cl-ghost")!.style.opacity = ghost ? "1" : "0";
  setNum(b.querySelector("#lg-v")!, fms(lv));
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

function drawSpeed(b: HTMLElement, full: boolean) {
  const run = A.run;
  if (!run || (!run.detail.rate.length && !run.detail.idle.length)) {
    b.innerHTML = `<div class="pnlx"><p class="note">Run a test to see speed and latency over time.</p></div>`;
    return;
  }
  if (full || !b.querySelector("#ch")) {
    b.innerHTML = `<div class="pnlx"><div class="ch-key sr" id="ch-key" role="slider" tabindex="0" aria-label="Moment in the test. Use the arrow keys to read speed and latency at each sample." aria-valuemin="0"></div><div class="ch" id="ch"></div><div class="pnlx" id="ch-info"></div></div>`;
  }
  let lastT = 0;
  for (const p of run.detail.rate) lastT = Math.max(lastT, p[0]);
  for (const p of run.detail.probes) lastT = Math.max(lastT, p[0]);
  const live = run.phase !== "done" && A.status === "running";
  chart = model({
    detail: run.detail,
    T: live ? Math.max(run.expected, lastT) : lastT || 1,
    floor: S.planDown,
    down: run.down,
    up: run.up,
    idleMs: run.idleMs,
    conv: (v) => (S.unit === "MBps" ? v / 8 : v),
    fmt,
    fms,
    unit: unit(),
  });
  drawChart();
  const fromClient = run.detail.upload_from_client
    ? `<p class="note">No upload request finished in time, so upload speed is this browser's own count rather than the server's.</p>`
    : "";
  b.querySelector("#ch-info")!.innerHTML = `<p class="note">The shaded start of each transfer is warm-up. It isn't counted in the result, so a result can sit below the peak.</p>
  <dl class="kv"><dt>Connections</dt><dd>${run.streams || "–"}${run.auto && run.streams ? ", auto" : ""}</dd></dl>${fromClient}`;
}

let chart: Model | null = null;
// Phones read the chart a transfer at a time, in the order the test ran (ADR-0012).
const narrow = matchMedia("(max-width: 599px)");
narrow.addEventListener("change", drawChart);
function drawChart() {
  const el = stage.querySelector("#ch");
  if (!el || !chart) return;
  el.innerHTML = draw(chart, A.hover, narrow.matches);
  const key = stage.querySelector("#ch-key")!;
  key.setAttribute("aria-valuemax", chart.T.toFixed(1));
  key.setAttribute("aria-valuenow", (A.hover?.t ?? 0).toFixed(1));
  key.setAttribute("aria-valuetext", A.hover ? describe(chart, A.hover.t) : "No moment chosen");
}

/** A row of radio buttons that look like a segmented control. */
const seg = (name: string, opts: [string, string][], cur: string) =>
  `<div class="seg">${opts.map(([v, l]) => `<label><input type="radio" name="${name}" value="${v}"${cur === v ? " checked" : ""}><span>${l}</span></label>`).join("")}</div>`;

function setupBody(): string {
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
    <div class="field"><label for="set-name">This device's name</label><input type="text" id="set-name" maxlength="80" autocomplete="off" spellcheck="false" placeholder="${esc(clientLabel())}" value="${esc(S.name)}"><p class="note">History shows it beside this device's tests. Left empty, it's ${esc(clientLabel())}.</p></div>
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
  // The switch stays put while the list reloads, so keyboard focus survives a change of view.
  if (!b.querySelector("#hist-list")) {
    b.innerHTML = `<div class="pnlx"><fieldset class="field hscope"><legend class="sr">Show tests from</legend>${seg(
      "hist-scope",
      [
        ["mine", "This device"],
        ["all", "All devices"],
      ],
      A.histAll ? "all" : "mine",
    )}</fieldset><div id="hist-list"></div></div>`;
  }
  const out = b.querySelector<HTMLElement>("#hist-list")!;
  const all = A.histAll;
  out.innerHTML = `<p class="note">Loading…</p>`;
  let list: StoredResult[];
  try {
    list = (await getJSON<{ results: StoredResult[] }>(`api/v1/results?limit=50${all ? "" : `&device=${DEVICE}`}`)).results;
  } catch (err) {
    if (A.sheet === "history" && A.histAll === all) out.innerHTML = `<p class="note">Couldn't load history: ${esc((err as Error).message)}</p>`;
    return;
  }
  if (A.sheet !== "history" || A.histAll !== all) return;
  if (!list.length) {
    out.innerHTML = `<p class="note">${
      all ? "No tests yet. Every test run against this server shows up here." : "No tests from this device yet. Tests from your other devices are under All devices."
    }</p>`;
    return;
  }
  // Date and time as two spans, so a phone can stack them in a narrow column.
  const when = (iso: string) => {
    const d = new Date(iso);
    return `<span>${esc(d.toLocaleDateString(undefined, { month: "short", day: "numeric" }))},</span> <span>${esc(
      d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }),
    )}</span>`;
  };
  const from = (r: StoredResult) =>
    (r.source === "client" ? r.client || "Browser" : r.source === "schedule" ? "Scheduled" : r.source) + (r.device === DEVICE ? " (this device)" : "");
  // All devices names each row's device: in its own column, or under the time on a phone.
  out.innerHTML = `<div class="tw" role="region" aria-label="Test history" tabindex="0"><table class="hist"><thead><tr><th>When</th><th>Down</th><th>Up</th><th>Latency</th><th>Grade</th>${
    all ? `<th class="from">From</th>` : ""
  }</tr></thead><tbody>${list
    .map(
      (r) =>
        `<tr><td class="when">${when(r.at)}${all ? `<span class="dev">${esc(from(r))}</span>` : ""}</td><td class="n">${fmt(r.download_mbps)}</td><td class="n">${fmt(
          r.upload_mbps,
        )}</td><td class="n">${fms(r.idle_ms)} ms</td><td>${esc(r.grade || "–")}</td>${all ? `<td class="from">${esc(from(r))}</td>` : ""}</tr>`,
    )
    .join("")}</tbody></table></div><p class="note">${
    all ? "Every test run against this server" : `Tests from this device (${esc(S.name || clientLabel())})`
  }, newest first. Speeds in ${unit()}; latency is idle latency.</p>`;
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
  if (t.name === "hist-scope") {
    A.histAll = t.value === "all";
    drawHistory(q("#sheet-body"));
    return;
  }
  if (t.name === "set-prof") S.profile = t.value as Profile;
  else if (t.name === "set-unit") S.unit = t.value as Settings["unit"];
  else if (t.id === "set-streams") S.streams = t.value;
  else if (t.id === "set-name") S.name = t.value.trim();
  else if (t.id === "set-plan") {
    S.planDown = Math.max(1, Number(t.value) || DEFAULTS.planDown);
    q("#f-zone").setAttribute("d", zone());
  } else return;
  saveSettings();
  update();
});

// Speed over time crosshair: follows the pointer, or the arrow keys when the chart has focus.
stage.addEventListener("pointermove", (e) => {
  const p = (e.target as Element).closest<HTMLElement>("[data-plot]");
  if (!chart || !p) return;
  const r = p.getBoundingClientRect();
  const t0 = Number(p.dataset.t0);
  const t1 = Number(p.dataset.t1);
  const t = snap(chart, t0 + ((e.clientX - r.left) / r.width) * (t1 - t0), t0, t1);
  A.hover = t == null ? null : { t, plot: p.dataset.plot as Hover["plot"] };
  drawChart();
});
stage.addEventListener("pointerout", (e) => {
  const to = e.relatedTarget as Element | null;
  if (A.hover && !to?.closest("[data-plot]")) {
    A.hover = null;
    drawChart();
  }
});
const STEPS: Record<string, 1 | -1 | "first" | "last"> = { ArrowRight: 1, ArrowUp: 1, ArrowLeft: -1, ArrowDown: -1, Home: "first", End: "last" };
stage.addEventListener("keydown", (e) => {
  const target = e.target as HTMLElement;
  const by = STEPS[e.key];
  if (by == null) return;
  if (target.id === "ch-key" && chart) {
    e.preventDefault();
    const t = by === "first" || by === "last" ? step(chart, null, by === "first" ? 1 : -1) : step(chart, A.hover?.t ?? null, by);
    A.hover = t == null ? null : { t, plot: "tp" };
    drawChart();
  } else if (target.dataset.tab && (e.key === "ArrowLeft" || e.key === "ArrowRight" || by === "first" || by === "last")) {
    // Tabs: arrows move between tabs and open them (WAI-ARIA tabs, automatic activation).
    e.preventDefault();
    const keys = TABS.map(([k]) => k);
    const i = keys.indexOf(target.dataset.tab as Tab);
    const n = by === "first" ? 0 : by === "last" ? keys.length - 1 : (i + (by as number) + keys.length) % keys.length;
    openSheet(keys[n]!);
  }
});
stage.addEventListener("focusin", (e) => {
  if ((e.target as Element).id === "ch-key" && chart && !A.hover) {
    const t = step(chart, null, 1);
    A.hover = t == null ? null : { t, plot: "tp" };
    drawChart();
  }
});
stage.addEventListener("focusout", (e) => {
  if ((e.target as Element).id === "ch-key" && A.hover) {
    A.hover = null;
    drawChart();
  }
});

addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (A.hover) {
    // Escape dismisses the chart tooltip first, without moving the pointer (WCAG 1.4.13).
    A.hover = null;
    drawChart();
  } else if (A.sheet) closeSheet();
});

update();
maybeAutoStart();
