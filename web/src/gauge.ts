// Gauge geometry, ported from prototypes/dyno-ui.html. Angles are degrees
// clockwise from 12 o'clock; dials sweep -120..120.

export function P(cx: number, cy: number, r: number, deg: number): [number, number] {
  const a = ((deg - 90) * Math.PI) / 180;
  return [cx + r * Math.cos(a), cy + r * Math.sin(a)];
}

export function ARC(cx: number, cy: number, r: number, a0: number, a1: number): string {
  const [x0, y0] = P(cx, cy, r, a0);
  const [x1, y1] = P(cx, cy, r, a1);
  return `M${x0.toFixed(2)} ${y0.toFixed(2)} A${r} ${r} 0 ${a1 - a0 > 180 ? 1 : 0} 1 ${x1.toFixed(2)} ${y1.toFixed(2)}`;
}

/** 1 Mbps .. 10 Gbps onto 0..1. */
export const logMap = (v: number) => Math.min(1, Math.max(0, Math.log10(Math.max(v || 0, 1)) / 4));
export const linMap = (max: number) => (v: number) => Math.min(1, Math.max(0, (v || 0) / max));

interface Tick {
  v: number;
  k: "major" | "mid" | "minor";
  label: string | null;
}

export function linTicks(max: number, minor: number, major: number): Tick[] {
  const t: Tick[] = [];
  for (let v = 0; v <= max; v += minor) t.push({ v, k: v % major === 0 ? "major" : "minor", label: v % major === 0 ? String(v) : null });
  return t;
}

export interface Gauge {
  id: string;
  cx: number;
  cy: number;
  r: number;
  map: (v: number) => number;
  ticks: Tick[];
  red?: number;
  ghost?: boolean;
  labelScale?: number;
}

const angOf = (o: Gauge, v: number) => -120 + 240 * o.map(v);

export function gaugeG(o: Gauge): string {
  const { cx, cy, r, ticks, id } = o;
  const tl = r * 0.17;
  const needle = r * 0.86;
  const hub = r * 0.085;
  let s = "";
  if (o.red != null) s += `<path d="${ARC(cx, cy, r * 0.95, angOf(o, o.red), 120)}" class="g-red" stroke-width="${(r * 0.075).toFixed(1)}"/>`;
  for (const t of ticks) {
    const a = angOf(o, t.v);
    const len = t.k === "major" ? tl : t.k === "mid" ? tl * 0.62 : tl * 0.4;
    const [x0, y0] = P(cx, cy, r * 0.9 - len, a);
    const [x1, y1] = P(cx, cy, r * 0.9, a);
    const w = t.k === "major" ? r * 0.038 : t.k === "mid" ? r * 0.02 : r * 0.012;
    s += `<line class="g-tick ${t.k}" x1="${x0.toFixed(1)}" y1="${y0.toFixed(1)}" x2="${x1.toFixed(1)}" y2="${y1.toFixed(1)}" stroke-width="${w.toFixed(2)}"/>`;
    if (t.label) {
      const [lx, ly] = P(cx, cy, r * 0.6, a);
      s += `<text class="g-lab" x="${lx.toFixed(1)}" y="${ly.toFixed(1)}" font-size="${(r * (o.labelScale ?? 0.13)).toFixed(1)}">${t.label}</text>`;
    }
  }
  if (o.ghost)
    s += `<g id="${id}-ghost" class="g-ghost" style="transform-origin:${cx}px ${cy}px"><line x1="${cx}" y1="${(cy - r * 0.97).toFixed(1)}" x2="${cx}" y2="${(cy - r * 0.74).toFixed(1)}" stroke-width="${(r * 0.05).toFixed(1)}" stroke-linecap="round"/></g>`;
  s += `<g id="${id}" class="g-needle" style="transform-origin:${cx}px ${cy}px"><path class="g-ndl" d="M${cx - r * 0.026} ${cy + r * 0.13} L${cx - r * 0.008} ${cy - needle} Q${cx} ${cy - needle - r * 0.03} ${cx + r * 0.008} ${cy - needle} L${cx + r * 0.026} ${cy + r * 0.13}Z"/><circle class="g-hub" cx="${cx}" cy="${cy}" r="${hub.toFixed(1)}" stroke-width="${(r * 0.02).toFixed(1)}"/></g>`;
  return s;
}

export function setNeedle(el: ParentNode, o: Gauge, v: number, which = o.id) {
  const n = el.querySelector<SVGGElement>("#" + which);
  if (n) n.style.transform = `rotate(${angOf(o, v).toFixed(2)}deg)`;
}

export const faceSVG = (cx: number, cy: number, r: number) =>
  `<circle cx="${cx}" cy="${cy}" r="${r * 1.065}" fill="var(--bezel)"/><circle cx="${cx}" cy="${cy}" r="${r * 1.025}" fill="var(--black)"/><circle cx="${cx}" cy="${cy}" r="${r}" fill="var(--face)"/>`;

/** The latency gauge in the telemetry sheet: 0..150 ms, red from 100. */
export const CL: Gauge = { id: "cl", cx: 100, cy: 100, r: 88, map: linMap(150), ticks: linTicks(150, 10, 50), red: 100, ghost: true, labelScale: 0.16 };
