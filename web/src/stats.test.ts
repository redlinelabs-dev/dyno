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

test("upload rate sums per-stream server rates after warmup", () => {
  const s = (start: number, mb: number, secs: number) => ({ start, bytes: mb * 125_000, elapsedUs: secs * 1e6 });
  // Two streams at 100 and 200 Mbit/s after warmup; warmup requests are slower and ignored.
  const rate = uploadRate([[s(0, 10, 1), s(3, 100, 1), s(4, 100, 1)], [s(0, 10, 1), s(3.5, 400, 2)]], 3);
  expect(rate).toBeCloseTo(300);
});

test("upload rate falls back to warmup requests, then to null", () => {
  expect(uploadRate([[{ start: 0, bytes: 125_000, elapsedUs: 1e6 }]], 3)).toBeCloseTo(1);
  expect(uploadRate([[], []], 3)).toBeNull();
});

test("quantile", () => {
  expect(quantile([1, 2, 3, 4], 0.5)).toBe(2);
  expect(quantile([1, 2, 3, 4], 0.9)).toBe(4);
});
