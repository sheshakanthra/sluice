// Unit tests for lib/backoff.ts: `pnpm --dir web test` (node --test; Node 24
// strips the TypeScript types on import).
import assert from "node:assert/strict";
import { test } from "node:test";

import { BACKOFF_CAP_MS, backoffMs, jitterMs, stretchMs } from "./backoff.ts";

test("backoff is exponential with full jitter and a 30 s cap", () => {
  const cases = [
    // [attempt, floorMs, u, want]
    [0, 0, 1, 500],
    [0, 0, 0.5, 250],
    [3, 0, 0, 0],
    [1, 0, 1, 1000],
    [4, 0, 1, 8000],
    [6, 0, 1, BACKOFF_CAP_MS],
    [200, 0, 1, BACKOFF_CAP_MS],
    [-1, 0, 1, 500],
  ];
  for (const [attempt, floor, u, want] of cases) {
    assert.equal(backoffMs(attempt, floor, u), want, `backoffMs(${attempt}, ${floor}, ${u})`);
  }
});

test("backoff never retries sooner than the server's pacing", () => {
  assert.equal(backoffMs(0, 15_000, 1), 15_000);
  assert.equal(backoffMs(6, 10_000, 0.5), 15_000);
  assert.equal(backoffMs(9, 60_000, 1), 60_000);
});

test("backoff draws spread over the window", () => {
  let lo = 0;
  let hi = 0;
  for (let i = 0; i < 10_000; i++) {
    const d = backoffMs(4, 0);
    assert.ok(d >= 0 && d <= 8000, `draw ${d}`);
    if (d < 4000) lo++;
    else hi++;
  }
  assert.ok(lo > 4000 && hi > 4000, `lo ${lo} hi ${hi}`);
});

test("stretch only lengthens retry_after, by at most 10%", () => {
  assert.equal(stretchMs(10_000, 0), 10_000);
  assert.equal(stretchMs(10_000, 1), 11_000);
  for (let i = 0; i < 1000; i++) {
    const d = stretchMs(10_000);
    assert.ok(d >= 10_000 && d <= 11_000, `stretch ${d}`);
  }
});

test("jitter spreads a fixed interval by ±20%", () => {
  assert.equal(jitterMs(1000, 0), 800);
  assert.equal(jitterMs(1000, 1), 1200);
  assert.equal(jitterMs(1000, 0.5), 1000);
});
