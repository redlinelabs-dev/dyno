// Pure statistics for the measurement worker. Kept free of I/O so they can be
// tested on their own (stats.test.ts).

export const mean = (a: number[]) => (a.length ? a.reduce((x, y) => x + y, 0) / a.length : NaN);

export function median(a: number[]): number {
  if (!a.length) return NaN;
  const s = [...a].sort((x, y) => x - y);
  const m = s.length >> 1;
  return s.length % 2 ? s[m]! : (s[m - 1]! + s[m]!) / 2;
}

/** Jitter as Cloudflare defines it: mean absolute difference of consecutive samples. */
export function jitter(samples: number[]): number {
  if (samples.length < 2) return 0;
  let sum = 0;
  for (let i = 1; i < samples.length; i++) sum += Math.abs(samples[i]! - samples[i - 1]!);
  return sum / (samples.length - 1);
}

/** Megabits per second for `bytes` moved in `seconds`. */
export const mbps = (bytes: number, seconds: number) => (seconds > 0 ? (bytes * 8) / seconds / 1e6 : 0);

/**
 * Bytes per fixed-width time bucket, so the live rate and the steady-state
 * rate both come from the same counts.
 */
export class Buckets {
  readonly counts: number[] = [];
  constructor(readonly width: number) {} // seconds

  add(t: number, bytes: number) {
    const i = Math.floor(t / this.width);
    while (this.counts.length <= i) this.counts.push(0);
    this.counts[i]! += bytes;
  }

  /** Rate over [from, to) seconds, whole buckets only. Buckets nothing arrived in count as zero, trailing ones included. */
  rate(from: number, to: number): number {
    const a = Math.max(0, Math.ceil(from / this.width - 1e-9));
    const b = Math.floor(to / this.width + 1e-9);
    if (b <= a) return 0;
    let sum = 0;
    for (let i = a; i < b; i++) sum += this.counts[i] ?? 0;
    return mbps(sum, (b - a) * this.width);
  }
}

/** One finished upload request: the server's byte count, timed on the client clock. */
export interface UploadReq {
  start: number; // seconds since the phase began, when the request was opened
  end: number; // when the server's reply arrived, so every byte had been received
  bytes: number; // bytes the server received
}

/**
 * Bytes the server received in [from, to), taking each request's bytes as
 * spread evenly over its time on the wire. A request that straddles a window
 * edge counts in part.
 */
export function uploadBytes(streams: UploadReq[][], from: number, to: number): number {
  let sum = 0;
  for (const reqs of streams)
    for (const r of reqs) {
      const overlap = Math.min(r.end, to) - Math.max(r.start, from);
      if (overlap > 0) sum += r.end > r.start ? (r.bytes * overlap) / (r.end - r.start) : r.bytes;
    }
  return sum;
}

/**
 * How far into the phase the server's count is complete: the earliest of the
 * streams' last replies. Past it, some stream has bytes in flight that only
 * its next reply will report. Null until a request has finished.
 */
export function uploadKnown(streams: UploadReq[][]): number | null {
  let known: number | null = null;
  for (const reqs of streams) {
    const last = reqs.at(-1);
    if (last) known = known === null ? last.end : Math.min(known, last.end);
  }
  return known;
}

/**
 * Upload speed from the server's counts (ADR-0003): the bytes every stream
 * delivered between the end of warmup and the point the count is complete,
 * over that one window. One shared window keeps the gaps between requests as
 * the zeros they are; adding per-stream rates timed over different windows
 * read high when streams finished at different times.
 *
 * Spreading a request's bytes evenly only holds when requests are short next
 * to the window: streams share the link unevenly over a long request. So if
 * the window is under a second, or a request in it lasted more than half of
 * it (a link so slow that few requests finish), the result is instead every
 * byte over the whole span from the first request to the last reply. That
 * misses the bytes still in flight at the end, so it reads low, never high.
 * Even when spreading holds, streams whose shares shift within a request can
 * move a few percent of bytes across a window edge, either way.
 * Returns null when no request finished.
 */
export function uploadRate(streams: UploadReq[][], warmup: number): number | null {
  const known = uploadKnown(streams);
  if (known === null) return null;
  if (spreads(streams, warmup, known)) return mbps(uploadBytes(streams, warmup, known), known - warmup);
  const reqs = streams.flat();
  return reqs.length ? confirmed(reqs, Math.max(...reqs.map((r) => r.end))) : null;
}

/**
 * The upload chart, worked out the way uploadRate works out the result: a
 * [seconds, Mbit/s] sample every `step` up to where the count is complete. When
 * the result spreads bytes, each sample is the rate over the `window` before
 * it. When it falls back, each is every byte confirmed so far over the time
 * since the first request, which never reads above the result's method; spread
 * samples would there show several times the link.
 */
export function uploadSeries(streams: UploadReq[][], warmup: number, step: number, window: number): [number, number][] {
  const known = uploadKnown(streams);
  if (known === null) return [];
  const spread = spreads(streams, warmup, known);
  const reqs = streams.flat();
  const last = spread ? known : Math.max(...reqs.map((r) => r.end));
  const out: [number, number][] = [];
  for (let i = 1; i * step <= last + 1e-9; i++) {
    const t = i * step;
    const from = Math.max(0, t - window);
    out.push([t, spread ? mbps(uploadBytes(streams, from, t), t - from) : (confirmed(reqs, t) ?? 0)]);
  }
  return out;
}

/** Whether spreading each request's bytes over its time holds for the window [warmup, known]. */
function spreads(streams: UploadReq[][], warmup: number, known: number): boolean {
  const window = known - warmup;
  const longest = Math.max(0, ...streams.flat().filter((r) => r.end > warmup && r.start < known).map((r) => r.end - r.start));
  return window >= 1 && longest <= window / 2;
}

/** Bytes of the requests answered by `t`, over the time since the first one opened. Null before that. */
function confirmed(reqs: UploadReq[], t: number): number | null {
  const first = Math.min(...reqs.map((r) => r.start));
  if (!(t > first)) return null;
  return mbps(reqs.reduce((s, r) => (r.end <= t ? s + r.bytes : s), 0), t - first);
}

/** Value at quantile q (0..1) by nearest rank. */
export function quantile(a: number[], q: number): number {
  if (!a.length) return NaN;
  const s = [...a].sort((x, y) => x - y);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))]!;
}
