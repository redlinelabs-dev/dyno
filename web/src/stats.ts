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

  /** Rate over [from, to) seconds, whole buckets only. */
  rate(from: number, to: number): number {
    const a = Math.max(0, Math.ceil(from / this.width - 1e-9));
    const b = Math.min(this.counts.length, Math.floor(to / this.width + 1e-9));
    if (b <= a) return 0;
    let sum = 0;
    for (let i = a; i < b; i++) sum += this.counts[i]!;
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
 * Upload speed from the server's counts (ADR-0003). Each stream's rate is the
 * bytes the server received over the stream's wall time, from opening its
 * first request after warmup to the reply to its last. Streams run in
 * parallel, so their rates add. Wall time keeps the gaps between a stream's
 * requests; the server's own per-request time leaves them out, and starts
 * only once the body is already arriving, so it reads high. A stream with no
 * request after warmup falls back to its warmup requests. Returns null when no
 * stream finished a request at all.
 */
export function uploadRate(streams: UploadReq[][], warmup: number): number | null {
  let total = 0;
  let any = false;
  for (const reqs of streams) {
    const steady = reqs.filter((r) => r.start >= warmup);
    const use = steady.length ? steady : reqs;
    if (!use.length) continue;
    const bytes = use.reduce((s, r) => s + r.bytes, 0);
    const secs = Math.max(...use.map((r) => r.end)) - Math.min(...use.map((r) => r.start));
    if (secs > 0) {
      total += mbps(bytes, secs);
      any = true;
    }
  }
  return any ? total : null;
}

/** Value at quantile q (0..1) by nearest rank. */
export function quantile(a: number[], q: number): number {
  if (!a.length) return NaN;
  const s = [...a].sort((x, y) => x - y);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))]!;
}
