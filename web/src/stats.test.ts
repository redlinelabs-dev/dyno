import { expect, test } from "bun:test";
import { Buckets, jitter, mbps, median, quantile, uploadBytes, uploadKnown, uploadRate, uploadSeries } from "./stats";

test("median of odd, even and empty", () => {
  expect(median([3, 1, 2])).toBe(2);
  expect(median([4, 1, 3, 2])).toBe(2.5);
  expect(median([])).toBeNaN();
});

test("jitter is the mean absolute step", () => {
  expect(jitter([10, 12, 9, 9])).toBeCloseTo((2 + 3 + 0) / 3);
  expect(jitter([5])).toBe(0);
});

test("mbps", () => {
  expect(mbps(125_000_000, 1)).toBe(1000);
});

test("bucket rate counts whole buckets in the window", () => {
  const b = new Buckets(0.1);
  for (let i = 0; i < 30; i++) b.add(i * 0.1 + 0.05, i < 10 ? 1_000 : 12_500_000); // warmup slow, then 1 Gbit/s
  expect(b.rate(1, 3)).toBeCloseTo(1000);
  expect(b.rate(2.95, 3)).toBe(0); // no whole bucket inside
});

test("bucket rate counts a stall at the end as zero", () => {
  const b = new Buckets(0.1);
  for (let i = 0; i < 50; i++) b.add(i * 0.1 + 0.05, 1_250_000); // 100 Mbit/s for 5 s, then nothing
  expect(b.rate(1.5, 10)).toBeCloseTo((3.5 * 100) / 8.5); // 3.5 s at 100 over 8.5 s
});

const req = (start: number, end: number, mbit: number) => ({ start, end, bytes: mbit * 125_000 });

test("upload rate is every stream's bytes after warmup over one shared window", () => {
  // Two streams at 100 and 200 Mbit/s. Stream a's request over warmup at 3 s counts in half;
  // the count is complete up to b's last reply at 7.5 s.
  const a = Array.from({ length: 8 }, (_, i) => req(i + 0.5, i + 1.5, 100));
  const b = Array.from({ length: 15 }, (_, i) => req(i / 2, i / 2 + 0.5, 100));
  expect(uploadRate([a, b], 3)).toBeCloseTo((450 + 900) / 4.5);
});

test("upload rate counts the gaps between requests", () => {
  // Each request sends 75 Mbit in 0.75 s and the next starts 0.25 s later, so the
  // stream delivers 300 Mbit in 3.75 s (80 Mbit/s), not the 100 it reaches while a request is open.
  const reqs = [0, 1, 2, 3].map((i) => req(3 + i, 3.75 + i, 75));
  expect(uploadRate([reqs], 3)).toBeCloseTo(80);
});

test("upload rate never adds rates timed over different windows", () => {
  // A 5 Mbit/s link shared by five streams that each manage one request, finishing
  // one after another as the link frees up. The old per-stream sum read 6.1 here.
  const ends = [5.41, 6.87, 7.5, 8.13, 8.92];
  const streams = ends.map((end) => [req(0.27, end, 8.39)]);
  const rate = uploadRate(streams, 3)!;
  expect(rate).toBeLessThanOrEqual(5);
  expect(rate).toBeGreaterThan(4);
});

test("upload rate falls back to the first request, then to null", () => {
  expect(uploadRate([[req(0, 1, 1)]], 3)).toBeCloseTo(1);
  expect(uploadRate([[], []], 3)).toBeNull();
});

test("upload bytes spread each request evenly over its time", () => {
  expect(uploadBytes([[req(0, 2, 8)]], 1, 1.5)).toBeCloseTo(2 * 125_000);
  expect(uploadKnown([[req(0, 2, 8)], [req(0, 1, 8)]])).toBe(1);
  expect(uploadKnown([[], []])).toBeNull();
});

test("upload chart never shows more than the result's method allows", () => {
  // A 5 Mbit/s link: five streams open 5 Mbit requests at 3 s and the replies
  // come a second apart. Spread evenly, the sample at 4 s read 11.4.
  const streams = [4, 5, 6, 7, 8].map((end) => [req(3, end, 5)]);
  expect(uploadRate(streams, 3)).toBeCloseTo(5);
  const series = uploadSeries(streams, 3, 0.1, 1);
  expect(Math.max(...series.map(([, v]) => v))).toBeLessThanOrEqual(5 + 1e-9);
  expect(series.at(-1)![0]).toBeCloseTo(8);
  expect(series.at(-1)![1]).toBeCloseTo(5);
});

test("upload chart spreads bytes when the result does", () => {
  const a = Array.from({ length: 8 }, (_, i) => req(i + 0.5, i + 1.5, 100));
  const b = Array.from({ length: 15 }, (_, i) => req(i / 2, i / 2 + 0.5, 100));
  const series = uploadSeries([a, b], 3, 0.1, 1);
  expect(series.at(-1)![0]).toBeCloseTo(7.5);
  expect(series.at(-1)![1]).toBeCloseTo(300); // a at 100 plus b at 200 over [6.5, 7.5]
  expect(uploadSeries([[], []], 3, 0.1, 1)).toEqual([]);
});

test("quantile", () => {
  expect(quantile([1, 2, 3, 4], 0.5)).toBe(2);
  expect(quantile([1, 2, 3, 4], 0.9)).toBe(4);
});
