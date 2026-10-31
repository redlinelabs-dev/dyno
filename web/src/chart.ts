// The Speed over time panels: throughput and latency on their own axes over
// one time scale, with gridlines, warm-up shading and a hover crosshair. Each
// plot is an SVG stretched to its box (paths only, so stretching is safe) with
// axis labels and the crosshair laid over it in HTML, positioned in percent.

import type { Detail, Span } from "./types";

export interface ChartInput {
  detail: Detail;
  T: number; // seconds the time axis spans
  floor: number; // lowest top for the throughput axis, Mbps (the plan speed keeps a live chart steady)
  down: number | null; // results, Mbps
  up: number | null;
  idleMs: number | null;
  conv: (mbps: number) => number; // Mbps to the display unit
  fmt: (mbps: number | null) => string; // Mbps to display text
  fms: (ms: number | null) => string;
  unit: string;
}

export interface Model extends ChartInput {
  spans: Span[];
  rate: [number, number, 0 | 1][][]; // samples split by direction
  probes: [number, number, 0 | 1][][];
  vTop: number; // throughput axis top, display unit
  vStep: number;
  msTop: number;
  msStep: number;
  times: number[]; // every sample time, sorted: what the crosshair snaps to
}

const VW = 1000; // SVG user units across; heights are 100

export function model(c: ChartInput): Model {
  const d = c.detail;
  const rate = [0, 1].map((dir) => d.rate.filter((p) => p[2] === dir));
  const probes = [0, 1].map((dir) => d.probes.filter((p) => p[2] === dir));
  // Results saved before spans were recorded: take each direction's extent from its samples.
  const spans: Span[] =
    d.spans ??
    rate.filter((r) => r.length).map((r) => [r[0]![2], Math.max(0, r[0]![0] - 0.1), r[0]![0] - 0.1, r[r.length - 1]![0]] as Span);
  let vMax = c.floor;
  for (const p of d.rate) vMax = Math.max(vMax, p[1]);
  for (const v of [c.down, c.up]) if (v) vMax = Math.max(vMax, v);
  let msMax = Math.max(4, c.idleMs ?? 0);
  for (const p of d.probes) msMax = Math.max(msMax, p[1]);
  const [vTop, vStep] = nice(c.conv(vMax));
  const [msTop, msStep] = nice(msMax);
  const times = [...d.rate.map((p) => p[0]), ...d.probes.map((p) => p[0])].sort((a, b) => a - b);
  // A stopped test ends before its planned spans do.
  const clamped = spans.filter((sp) => sp[1] < c.T).map(([dir, start, counted, end]) => [dir, start, Math.min(counted, c.T), Math.min(end, c.T)] as Span);
  return { ...c, spans: clamped, rate, probes, vTop, vStep, msTop, msStep, times };
}

/** An axis top and step: five-ish intervals of 1, 2, 2.5 or 5 times a power of ten. */
function nice(max: number): [number, number] {
  if (!(max > 0)) return [1, 0.25];
  const raw = max / 5;
  const p = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((k) => k * p).find((s) => s >= raw)!;
  return [Math.ceil(max / step - 1e-9) * step, step];
}

const tickText = (v: number) => v.toLocaleString("en-US", { maximumFractionDigits: 2 });
const pct = (v: number) => `${Math.max(0, Math.min(100, v)).toFixed(2)}%`;

/** A monotone cubic through the points (Fritsch–Carlson): smooth, but never overshoots a sample. */
function curve(pts: [number, number][]): string {
  const n = pts.length;
  if (n < 2) return "";
  const m: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    const dx = pts[i + 1]![0] - pts[i]![0];
    m.push(dx > 0 ? (pts[i + 1]![1] - pts[i]![1]) / dx : 0);
  }
  const t = pts.map((_, i) => (i === 0 ? m[0]! : i === n - 1 ? m[n - 2]! : m[i - 1]! * m[i]! <= 0 ? 0 : (m[i - 1]! + m[i]!) / 2));
  for (let i = 0; i < n - 1; i++) {
    if (m[i] === 0) {
      t[i] = t[i + 1] = 0;
      continue;
    }
    const a = t[i]! / m[i]!;
    const b = t[i + 1]! / m[i]!;
    const s = a * a + b * b;
    if (s > 9) [t[i], t[i + 1]] = [(3 / Math.sqrt(s)) * a * m[i]!, (3 / Math.sqrt(s)) * b * m[i]!];
  }
  let d = `M${pts[0]![0].toFixed(1)} ${pts[0]![1].toFixed(2)}`;
  for (let i = 0; i < n - 1; i++) {
    const [x0, y0] = pts[i]!;
    const [x1, y1] = pts[i + 1]!;
    const h = (x1 - x0) / 3;
    d += `C${(x0 + h).toFixed(1)} ${(y0 + t[i]! * h).toFixed(2)} ${(x1 - h).toFixed(1)} ${(y1 - t[i + 1]! * h).toFixed(2)} ${x1.toFixed(1)} ${y1.toFixed(2)}`;
  }
  return d;
}

/** A series as a filled area under a line. */
function series(cls: string, pts: [number, number][]): string {
  const line = curve(pts);
  if (!line) return "";
  const x0 = pts[0]![0].toFixed(1);
  const x1 = pts[pts.length - 1]![0].toFixed(1);
  return `<path class="ch-a ${cls}" d="${line}L${x1} 100L${x0} 100Z"/><path class="ch-l ${cls}" d="${line}"/>`;
}

export interface Hover {
  t: number; // seconds
  plot: "tp" | "lp"; // the plot the pointer is over, which holds the tooltip
}

/** Everything inside #ch. */
export function draw(M: Model, hover: Hover | null): string {
  const X = (t: number) => (t / M.T) * VW;
  const xp = (t: number) => pct((t / M.T) * 100);
  const yV = (mbps: number) => 100 - (M.conv(mbps) / M.vTop) * 100;
  const yMs = (ms: number) => 100 - (ms / M.msTop) * 100;

  const [, xStep] = nice(M.T * (5 / 6));
  const xTicks: number[] = [];
  for (let t = 0; t <= M.T + 1e-9; t += xStep) xTicks.push(t);
  const grid = (step: number, top: number) => {
    let g = xTicks.map((t) => `<line class="ch-g" x1="${X(t).toFixed(1)}" x2="${X(t).toFixed(1)}" y1="0" y2="100"/>`).join("");
    for (let v = 0; v <= top + 1e-9; v += step) {
      const y = (100 - (v / top) * 100).toFixed(2);
      g += `<line class="ch-g${v === 0 ? " ch-base" : ""}" x1="0" x2="${VW}" y1="${y}" y2="${y}"/>`;
    }
    return g;
  };
  const yLabels = (step: number, top: number) => {
    let s = "";
    for (let v = 0; v <= top + 1e-9; v += step) s += `<span class="ch-y" style="bottom:${pct((v / top) * 100)}">${tickText(v)}</span>`;
    return s;
  };
  const xAxis = `<div class="ch-xa" aria-hidden="true">${xTicks
    .map((t) => {
      const at = (t / M.T) * 100;
      const edge = at < 3 ? " first" : at > 97 ? " last" : "";
      return `<span class="ch-xt${edge}" style="left:${pct(at)}">${tickText(t)} s</span>`;
    })
    .join("")}</div>`;
  const marks = M.spans
    .map(([, start, counted]) => {
      let s = `<rect class="ch-warm" x="${X(start).toFixed(1)}" width="${Math.max(0, X(counted) - X(start)).toFixed(1)}" y="0" height="100"/>`;
      if (start > 0.05) s += `<line class="ch-bound" x1="${X(start).toFixed(1)}" x2="${X(start).toFixed(1)}" y1="0" y2="100"/>`;
      return s;
    })
    .join("");
  const ref = (cls: string, x0: number, x1: number, y: number) =>
    `<line class="ch-ref ${cls}" x1="${x0.toFixed(1)}" x2="${x1.toFixed(1)}" y1="${y.toFixed(2)}" y2="${y.toFixed(2)}"/>`;

  // Throughput: each direction's line, and its result across the part that counted.
  let tp = `<defs>${grad("ch-gd")}${grad("ch-gu")}${grad("ch-gl")}</defs>${grid(M.vStep, M.vTop)}${marks}`;
  let tpLabels = "";
  M.rate.forEach((r, dir) => (tp += series(dir ? "up" : "down", r.map(([t, v]) => [X(t), yV(v)]))));
  for (const [dir, start, counted, end] of M.spans) {
    tpLabels += `<span class="ch-ph" style="left:${xp(start)}">${dir ? "Upload" : "Download"}</span>`;
    const res = dir ? M.up : M.down;
    if (!res) continue;
    tp += ref(dir ? "up" : "down", X(counted), X(end), yV(res));
    tpLabels += `<span class="ch-rl end" style="left:${xp(end)};bottom:${pct(100 - yV(res))}">Result ${M.fmt(res)}</span>`;
  }

  // Latency: probes under load, against the idle median as a reference.
  let lp = `${grid(M.msStep, M.msTop)}${marks}`;
  let lpLabels = "";
  if (M.idleMs != null) {
    lp += ref("lat", 0, VW, yMs(M.idleMs));
    lpLabels += `<span class="ch-rl" style="left:0;bottom:${pct(100 - yMs(M.idleMs))}">Idle ${M.fms(M.idleMs)}</span>`;
  }
  for (const p of M.probes) lp += series("lat", p.map(([t, ms]) => [X(t), yMs(ms)]));

  const cross = hover ? `<i class="ch-cross" style="left:${xp(hover.t)}"></i>` : "";
  const plot = (cls: Hover["plot"], svg: string, labels: string, step: number, top: number) =>
    `<div class="ch-plot" data-plot="${cls}" aria-hidden="true"><svg viewBox="0 0 ${VW} 100" preserveAspectRatio="none" aria-hidden="true">${svg}</svg>${yLabels(step, top)}${labels}${cross}${
      hover?.plot === cls ? tooltip(M, hover.t) : ""
    }</div>${xAxis}`;

  return `<div class="ch-panel"><div class="ch-head"><h2>Throughput</h2><span>${M.unit}</span></div>
    ${plot("tp", tp, tpLabels, M.vStep, M.vTop)}
    <table class="ch-lg named"><thead><tr><td></td><th scope="col">Result</th><th scope="col">Peak</th></tr></thead><tbody>
      ${legendRow("down", "Download", M.down, peak(M.rate[0]!))}${legendRow("up", "Upload", M.up, peak(M.rate[1]!))}</tbody></table></div>
  <div class="ch-panel"><div class="ch-head"><h2>Latency</h2><span>ms</span></div>
    ${plot("lp", lp, lpLabels, M.msStep, M.msTop)}
    <table class="ch-lg named"><thead><tr><td></td><th scope="col">Idle</th><th scope="col">Downloading</th><th scope="col">Uploading</th><th scope="col">Peak</th></tr></thead><tbody><tr>
      <th scope="row"><i class="ch-k lat"></i>Latency</th><td>${M.fms(M.idleMs)}</td><td>${M.fms(median(M.probes[0]!, M.spans, 0))}</td><td>${M.fms(median(M.probes[1]!, M.spans, 1))}</td><td>${M.fms(peak(M.probes.flat()))}</td></tr></tbody></table></div>`;

  function legendRow(cls: string, name: string, res: number | null, pk: number | null) {
    return `<tr><th scope="row"><i class="ch-k ${cls}"></i>${name}</th><td>${M.fmt(res)}</td><td>${M.fmt(pk)}</td></tr>`;
  }
}

const grad = (id: string) => `<linearGradient id="${id}" class="${id}" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="0" y2="100"><stop offset="0"/><stop offset="1"/></linearGradient>`;
const peak = (a: [number, number, ...unknown[]][]) => (a.length ? Math.max(...a.map((p) => p[1])) : null);

/** The median of a direction's probes after its warm-up, as the result counts them. */
function median(a: [number, number, 0 | 1][], spans: Span[], dir: 0 | 1): number | null {
  const counted = spans.find((s) => s[0] === dir)?.[2] ?? 0;
  const v = a.filter((p) => p[0] >= counted).map((p) => p[1]).sort((x, y) => x - y);
  if (!v.length) return null;
  const h = v.length >> 1;
  return v.length % 2 ? v[h]! : (v[h - 1]! + v[h]!) / 2;
}

/** The sample time nearest t, so the crosshair lands on data rather than between it. */
export function snap(M: Model, t: number): number | null {
  if (!M.times.length) return null;
  let best = M.times[0]!;
  for (const x of M.times) if (Math.abs(x - t) < Math.abs(best - t)) best = x;
  return best;
}

/** The sample time before or after t, for arrow keys. */
export function step(M: Model, t: number | null, by: 1 | -1): number | null {
  const ts = [...new Set(M.times)];
  if (!ts.length) return null;
  if (t == null) return by > 0 ? ts[0]! : ts[ts.length - 1]!;
  const i = ts.findIndex((x) => x >= t - 1e-9);
  return ts[Math.max(0, Math.min(ts.length - 1, (i < 0 ? ts.length : i) + by))]!;
}

function nearest<T extends [number, ...unknown[]]>(a: T[], t: number, within: number): T | null {
  let best: T | null = null;
  for (const p of a) if (Math.abs(p[0] - t) <= within && (!best || Math.abs(p[0] - t) < Math.abs(best[0] - t))) best = p;
  return best;
}

function reading(M: Model, t: number) {
  const span = M.spans.find(([, start, , end]) => t >= start && t <= end + 0.2);
  const phase = !span ? "Idle" : `${span[0] ? "Upload" : "Download"}${t < span[2] ? ", warm-up" : ""}`;
  return { phase, r: nearest(M.detail.rate, t, 0.15), p: nearest(M.detail.probes, t, 0.3) };
}

/** The slider's spoken value for a moment. */
export function describe(M: Model, t: number): string {
  const { phase, r, p } = reading(M, t);
  return [`${tickText(Math.round(t * 10) / 10)} seconds, ${phase}`, r && `${M.fmt(r[1])} ${M.unit}`, p && `latency ${M.fms(p[1])} ms`].filter(Boolean).join(", ");
}

function tooltip(M: Model, t: number): string {
  const { phase, r, p } = reading(M, t);
  const row = (cls: string, v: string, u: string) => `<li><i class="ch-k ${cls}"></i><b>${v}</b> ${u}</li>`;
  const side = t / M.T > 0.6 ? "left" : "right";
  return `<div class="ch-tip ${side}" style="left:${pct((t / M.T) * 100)}"><p>${tickText(Math.round(t * 10) / 10)} s, ${phase}</p><ul>${
    r ? row(r[2] ? "up" : "down", M.fmt(r[1]), M.unit) : ""
  }${p ? row("lat", M.fms(p[1]), "ms") : ""}</ul></div>`;
}
