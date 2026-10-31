import { expect, test } from "bun:test";
import { Buckets, jitter, mbps, median, quantile, uploadRate } from "./stats";

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

test("upload rate is each stream's bytes over its own wall time after warmup, summed", () => {
  const s = (start: number, end: number, mb: number) => ({ start, end, bytes: mb * 125_000 });
  // Two streams at 100 and 200 Mbit/s after warmup; warmup requests are slower and ignored.
  const rate = uploadRate([[s(0, 1, 10), s(3, 4, 100), s(4, 5, 100)], [s(0, 1, 10), s(3.5, 5.5, 400)]], 3);
  expect(rate).toBeCloseTo(300);
});

test("upload rate counts the gaps between requests", () => {
  // Each request sends 75 Mbit in 0.75 s and the next starts 0.25 s later, so the
  // stream delivers 300 Mbit in 3.75 s (80 Mbit/s), not the 100 it reaches while a request is open.
  const reqs = [0, 1, 2, 3].map((i) => ({ start: 3 + i, end: 3.75 + i, bytes: 75 * 125_000 }));
  expect(uploadRate([reqs], 3)).toBeCloseTo(80);
});

test("upload rate falls back to warmup requests, then to null", () => {
  expect(uploadRate([[{ start: 0, end: 1, bytes: 125_000 }]], 3)).toBeCloseTo(1);
  expect(uploadRate([[], []], 3)).toBeNull();
});

test("quantile", () => {
  expect(quantile([1, 2, 3, 4], 0.5)).toBe(2);
  expect(quantile([1, 2, 3, 4], 0.9)).toBe(4);
});
