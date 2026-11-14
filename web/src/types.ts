// Shapes shared by the UI and the measurement worker. Plan and StoredResult
// mirror the server's JSON (docs/api.md).

export interface Transfer {
  streams: number;
  duration_ms: number;
  warmup_ms: number;
  start_bytes: number;
  max_bytes: number;
  target_request_ms: number;
}

export interface Plan {
  test_id: string;
  server: { name: string; version: string };
  endpoints: { download: string; upload: string; ping: string; results: string };
  latency: { idle_samples: number; loaded_interval_ms: number };
  download: Transfer;
  upload: Transfer;
}

export type Phase = "latency" | "download" | "upload";
export type Direction = "download" | "upload";

/** What the UI asks the worker to run. */
export interface StartMsg {
  type: "start";
  plan: Plan;
  origin: string;
  streams: number; // 1..plan streams
  scale: number; // test-length multiplier on the plan's durations
}

/** Everything a finished test hands back; posted to /api/v1/results. */
export interface Summary {
  download_mbps: number;
  upload_mbps: number;
  idle_ms: number;
  loaded_down_ms: number;
  loaded_up_ms: number;
  jitter_ms: number;
  streams: number;
  client_limited: boolean;
}

/** Raw series for the Speed over time chart. Times are seconds from test start. */
export interface Detail {
  idle: [t: number, ms: number][];
  rate: [t: number, mbps: number, dir: 0 | 1][]; // dir 0 = download, 1 = upload
  probes: [t: number, ms: number, dir: 0 | 1][];
  spans?: Span[]; // one per direction, for phase marks and the warm-up shading
  upload_from_client?: boolean; // no upload request completed, so the client's count was used
  timer_lag_ms?: number;
}

/** A transfer's planned timing: when it starts, when the counted part starts (after warm-up), when it ends. */
export type Span = [dir: 0 | 1, start: number, counted: number, end: number];

export type WorkerMsg =
  | { type: "phase"; phase: Phase; t: number; span?: Span }
  | { type: "idle"; t: number; ms: number; median: number; jitter: number }
  | { type: "rate"; dir: Direction; t: number; mbps: number }
  | { type: "probe"; dir: Direction; t: number; ms: number }
  | { type: "direction-done"; dir: Direction; mbps: number; loaded_ms: number }
  | { type: "done"; summary: Summary; detail: Detail }
  | { type: "error"; message: string };

export interface StoredResult extends Summary {
  id: number;
  at: string;
  source: string;
  client: string;
  device: string; // the posting browser's own id, "" for older results
  test_id: string;
  bufferbloat_ms: number;
  grade: string;
  rpm: number;
  server_bytes_down: number;
  server_bytes_up: number;
  detail?: Detail;
}
