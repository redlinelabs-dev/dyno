// The measurement worker. It runs the server's plan off the main thread, so
// gauge rendering and transfers don't slow each other down. Rules are
// ADR-0003's: HTTP/1.1, at most the plan's streams, probes on the free sixth
// connection, download counted here, upload counted by the server. The UI
// aborts a test by terminating this worker.

import { Buckets, jitter, mbps as rateOf, median, quantile, uploadBytes, uploadKnown, uploadRate, uploadSeries, type UploadReq } from "./stats";
import type { Detail, Direction, Plan, Span, StartMsg, Summary, Transfer, WorkerMsg } from "./types";

const BUCKET = 0.1; // seconds per byte-count bucket
const LIVE_WINDOW = 0.5; // seconds averaged for the live gauge
const UPLOAD_WINDOW = 1; // the same for upload, whose bytes are spread over requests of about a second
const PING_TIMEOUT = 10_000; // ms an idle-latency probe may take; the server's own stall limit
const LAG_TICK = 50; // ms between timer-lag samples
const LAG_LIMITED = 20; // median lag in ms that marks this device as the bottleneck
const MIN_UPLOAD = 64 << 10; // smallest upload request, bytes

let t0 = 0;
const now = () => (performance.now() - t0) / 1000;
const send = (m: WorkerMsg) => postMessage(m);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const bust = () => Math.random().toString(36).slice(2);

onmessage = (e: MessageEvent<StartMsg>) => {
  if (e.data.type === "start") run(e.data).catch((err) => send({ type: "error", message: String(err?.message ?? err) }));
};

async function run({ plan, origin, streams, scale }: StartMsg) {
  t0 = performance.now();
  performance.setResourceTimingBufferSize?.(1000);
  const url = (path: string) => new URL(path, origin).href;
  const detail: Detail = { idle: [], rate: [], probes: [] };

  // Idle latency: one warm-up ping, then the plan's samples back to back.
  send({ type: "phase", phase: "latency", t: now() });
  const pingURL = url(plan.endpoints.ping);
  const idle: number[] = [];
  try {
    await ping(pingURL, AbortSignal.timeout(PING_TIMEOUT));
    for (let i = 0; i < Math.max(1, plan.latency.idle_samples); i++) {
      const ms = await ping(pingURL, AbortSignal.timeout(PING_TIMEOUT));
      idle.push(ms);
      detail.idle.push([round(now()), round(ms)]);
      send({ type: "idle", t: now(), ms, median: median(idle), jitter: jitter(idle) });
    }
  } catch (err) {
    throw lost("latency", err);
  }

  const lag = startLagMeter();
  let down: DirectionResult, up: DirectionResult, lagMs: number;
  try {
    down = await transfer("download", plan, scale, streams, url, detail);
    up = await transfer("upload", plan, scale, streams, url, detail);
  } finally {
    lagMs = lag.stop(); // a failed test must not leave the meter ticking
  }

  const summary: Summary = {
    download_mbps: round(down.mbps),
    upload_mbps: round(up.mbps),
    idle_ms: round(median(idle)),
    loaded_down_ms: round(down.loadedMs),
    loaded_up_ms: round(up.loadedMs),
    jitter_ms: round(jitter(idle), 2),
    streams,
    client_limited: lagMs > LAG_LIMITED,
  };
  detail.timer_lag_ms = round(lagMs);
  if (up.fromClient) detail.upload_from_client = true;
  send({ type: "done", summary, detail });
}

interface DirectionResult {
  mbps: number;
  loadedMs: number;
  fromClient: boolean;
}

/** Runs one direction for its scaled duration with loaded-latency probes alongside. */
async function transfer(
  dir: Direction,
  plan: Plan,
  scale: number,
  streams: number,
  url: (p: string) => string,
  detail: Detail,
): Promise<DirectionResult> {
  const spec: Transfer = plan[dir];
  const duration = (spec.duration_ms * scale) / 1000;
  const warmup = Math.min(spec.warmup_ms / 1000, duration * 0.4);
  const d = dir === "download" ? 0 : 1;
  const t = round(now());
  const span: Span = [d, t, round(t + warmup), round(t + duration)];
  detail.spans = [...(detail.spans ?? []), span];
  send({ type: "phase", phase: dir, t: now(), span });

  const start = now();
  const at = () => now() - start;
  const buckets = new Buckets(BUCKET); // download: bytes as they arrive; upload: the browser's progress events
  const stop = new AbortController();
  const uploads: UploadReq[][] = [];
  const progress: Detail["rate"] = [];

  // The live rate. Upload progress events count bytes the browser handed to
  // its socket, which runs megabytes ahead of the network, so the live upload
  // rate comes from the server's replies instead: the last second of
  // what the server has confirmed. It trails real time by about a request.
  const ticker = setInterval(() => {
    const t = at();
    let mbps = buckets.rate(Math.max(0, t - LIVE_WINDOW), t);
    if (dir === "upload") {
      progress.push([round(now()), round(mbps), d]);
      const known = uploadKnown(uploads);
      mbps = known === null ? 0 : rateOf(uploadBytes(uploads, known - UPLOAD_WINDOW, known), UPLOAD_WINDOW);
    } else detail.rate.push([round(now()), round(mbps), d]);
    send({ type: "rate", dir, t: now(), mbps });
  }, BUCKET * 1000);

  // Loaded latency: probes on their own connection, the sixth one (ADR-0003).
  const loaded: number[] = [];
  const pingURL = url(plan.endpoints.ping);
  const probing = (async () => {
    while (!stop.signal.aborted) {
      const ms = await ping(pingURL, stop.signal).catch(() => NaN);
      if (stop.signal.aborted || Number.isNaN(ms)) break;
      if (at() >= warmup) loaded.push(ms);
      detail.probes.push([round(now()), round(ms), d]);
      send({ type: "probe", dir, t: now(), ms });
      await sleep(plan.latency.loaded_interval_ms);
    }
  })();

  const workers = Array.from({ length: streams }, (_, i) => {
    const count = (bytes: number) => buckets.add(at(), bytes);
    if (dir === "download") return downloadStream(url(plan.endpoints.download), plan.test_id, spec, stop.signal, count);
    uploads[i] = [];
    return uploadStream(url(plan.endpoints.upload), plan.test_id, spec, stop.signal, count, at, uploads[i]!);
  });

  // A stream that fails ends the test. The rest would be a result from fewer
  // streams than it claims, and a server that went away would still get a grade.
  let failure: unknown = null;
  const failed = new Promise<void>((resolve) =>
    workers.forEach((w) =>
      w.catch((err) => {
        failure ??= err;
        resolve();
      }),
    ),
  );
  await Promise.race([sleep(duration * 1000), failed]);
  stop.abort();
  clearInterval(ticker);
  await Promise.allSettled([...workers, probing]);
  if (failure) throw lost(dir, failure);

  let mbps = buckets.rate(warmup, duration);
  let fromClient = false;
  if (dir === "upload") {
    const server = uploadRate(uploads, warmup);
    if (server === null) {
      fromClient = true;
      detail.rate.push(...progress);
    } else {
      mbps = server;
      for (const [t, v] of uploadSeries(uploads, warmup, BUCKET, UPLOAD_WINDOW)) detail.rate.push([round(start + t), round(v), 1]);
    }
  }
  const loadedMs = loaded.length ? median(loaded) : 0;
  send({ type: "direction-done", dir, mbps, loaded_ms: loadedMs });
  return { mbps, loadedMs, fromClient };
}

/** The message a failed test shows: which part lost the server, and the browser's reason. */
function lost(part: string, err: unknown): Error {
  const why = String((err as Error)?.message ?? err);
  return new Error(`The server stopped answering during the ${part} test (${why}). Check it's running, then test again.`);
}

/** Back-to-back downloads on one connection, growing each request until it takes the target time. */
async function downloadStream(base: string, testID: string, spec: Transfer, signal: AbortSignal, count: (n: number) => void) {
  let size = spec.start_bytes;
  while (!signal.aborted) {
    const began = performance.now();
    try {
      const res = await fetch(`${base}?bytes=${size}&t=${testID}&r=${bust()}`, { cache: "no-store", signal });
      if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);
      const reader = res.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        count(value.byteLength);
      }
    } catch (err) {
      if (signal.aborted) return;
      throw err;
    }
    if (performance.now() - began < spec.target_request_ms) size = Math.min(size * 2, spec.max_bytes);
  }
}

// One random body for every upload, sliced per request. A browser holds the
// whole body in memory, hence the plan's 32 MiB cap. The 1 MiB random block
// repeats; browsers never compress request bodies, so that's safe.
let uploadBody: Blob | null = null;
function bodyOf(size: number, max: number): Blob {
  if (!uploadBody || uploadBody.size < max) {
    const block = new Uint8Array(1 << 20);
    for (let i = 0; i < block.length; i += 65536) crypto.getRandomValues(block.subarray(i, i + 65536));
    uploadBody = new Blob(Array.from({ length: Math.ceil(max / block.length) }, () => block));
  }
  return uploadBody.slice(0, size);
}

/** Back-to-back uploads. The server's reply is the measurement; progress events are kept only for when no reply ever comes. */
async function uploadStream(
  base: string,
  testID: string,
  spec: Transfer,
  signal: AbortSignal,
  count: (n: number) => void,
  at: () => number,
  done: UploadReq[],
) {
  let size = spec.start_bytes;
  while (!signal.aborted) {
    const start = at();
    const began = performance.now();
    const reply = await new Promise<{ bytes: number } | null>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      let sent = 0;
      const abort = () => xhr.abort();
      signal.addEventListener("abort", abort, { once: true });
      xhr.open("POST", `${base}?t=${testID}&r=${bust()}`);
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      xhr.upload.onprogress = (e) => {
        count(e.loaded - sent);
        sent = e.loaded;
      };
      xhr.onload = () => {
        signal.removeEventListener("abort", abort);
        if (xhr.status !== 200) return reject(new Error(`HTTP ${xhr.status}`));
        try {
          resolve(JSON.parse(xhr.responseText));
        } catch {
          reject(new Error("a reply that isn't dyno's")); // thrown here, it would leave this request pending forever
        }
      };
      xhr.onabort = () => resolve(null);
      xhr.onerror = () => (signal.aborted ? resolve(null) : reject(new Error("the connection dropped")));
      xhr.send(bodyOf(size, spec.max_bytes));
    });
    if (!reply) return;
    done.push({ start, end: at(), bytes: reply.bytes });
    // Size the next request to take the target time at this one's rate, up or
    // down. Doubling like downloads overshoots to twice the target, and the
    // server's count only arrives with each reply: a request that runs long
    // leaves the rate unknown for that long, which a short test can't spare.
    const took = Math.max(1, performance.now() - began);
    size = Math.round(Math.min(spec.max_bytes, Math.max(MIN_UPLOAD, (reply.bytes / took) * spec.target_request_ms)));
  }
}

/**
 * One latency probe. Resource Timing's request-to-first-byte excludes this
 * thread's scheduling delay; when the browser doesn't fill it in (it's zeroed
 * on some Safari versions), fall back to wall time around the fetch.
 */
async function ping(base: string, signal?: AbortSignal): Promise<number> {
  const u = `${base}?r=${bust()}`;
  const began = performance.now();
  const res = await fetch(u, { cache: "no-store", signal });
  await res.arrayBuffer();
  if (!res.ok) throw new Error(`HTTP ${res.status}`); // a proxy's error page isn't a round trip to dyno
  const wall = performance.now() - began;
  const e = performance.getEntriesByName(u).pop() as PerformanceResourceTiming | undefined;
  performance.clearResourceTimings();
  if (e && e.requestStart > 0 && e.responseStart > e.requestStart) return e.responseStart - e.requestStart;
  return wall;
}

/**
 * Samples how late a 50 ms timer fires. When this thread can't keep up, the
 * device's CPU, not the network, is likely what limited the result (ADR-0003 §7).
 * Returns the median lag over the transfers.
 */
function startLagMeter() {
  const lags: number[] = [];
  let last = performance.now();
  const id = setInterval(() => {
    const t = performance.now();
    lags.push(Math.max(0, t - last - LAG_TICK));
    last = t;
  }, LAG_TICK);
  return {
    stop() {
      clearInterval(id);
      return lags.length ? quantile(lags, 0.5) : 0;
    },
  };
}

const round = (v: number, digits = 1) => {
  const f = 10 ** digits;
  return Math.round(v * f) / f;
};
