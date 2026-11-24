// K1 (LOADTEST §5): queue-join spike. ramping-arrival-rate to RATE
// iterations/s (default 3,000) held for 30 s; each iteration is
// POST /sessions + POST /events/{id}/queue. Its join p95 should agree with
// the swarm's within ±20%.
//
// Run through `task k6:spike` (grafana/k6 on the compose network). Every
// iteration uses its own synthetic X-Forwarded-For (198.18.0.0/15, as the
// swarm does), so per-IP rate limits apply per simulated client, not to
// k6's one container IP. Thresholds are always-true on purpose: they only
// make the submetrics appear in the summary; handleSummary judges.
import http from 'k6/http';
import exec from 'k6/execution';
import { Rate, Trend, Counter } from 'k6/metrics';

const BASE = (__ENV.BASE || 'http://caddy:8000') + '/api/v1';
const EVENT = __ENV.EVENT;
const RATE = parseInt(__ENV.RATE || '3000', 10);
const OUT = __ENV.OUT || '.';

const sessionMs = new Trend('session_ms', true);
const joinMs = new Trend('join_ms', true);
const errors = new Rate('errors');
const joins = new Counter('joins_ok');

export const options = {
  scenarios: {
    spike: {
      executor: 'ramping-arrival-rate',
      startRate: 0,
      timeUnit: '1s',
      preAllocatedVUs: 300,
      maxVUs: 1500,
      stages: [
        { target: RATE, duration: '10s' },
        { target: RATE, duration: '30s' },
        { target: 0, duration: '5s' },
      ],
    },
  },
  thresholds: { join_ms: ['p(95)>=0'], session_ms: ['p(95)>=0'], errors: ['rate>=0'] },
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max', 'count'],
};

function xff(i) {
  return `198.${18 + ((i >> 16) & 1)}.${(i >> 8) & 255}.${i & 255}`;
}

// setup: the sale opens in 10 minutes, so every join lands in the
// pre-queue lottery (the swarm's main join path: 70% arrive before T0).
export function setup() {
  const r = http.post(`${BASE}/admin/events/${EVENT}/open-sale`, JSON.stringify({ opens_in_s: 600 }), {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${__ENV.ADMIN_TOKEN}` },
  });
  if (r.status !== 200) {
    throw new Error(`open-sale: HTTP ${r.status} ${r.body}`);
  }
}

export default function () {
  const h = { 'X-Forwarded-For': xff(exec.scenario.iterationInTest), 'Content-Type': 'application/json' };
  let r = http.post(`${BASE}/sessions`, null, { headers: h, tags: { route: 'session' } });
  sessionMs.add(r.timings.duration);
  if (r.status !== 201) {
    errors.add(true);
    return;
  }
  const tok = r.json('session_token');
  r = http.post(`${BASE}/events/${EVENT}/queue`, null, {
    headers: Object.assign({ Authorization: `Bearer ${tok}` }, h),
    tags: { route: 'join' },
  });
  joinMs.add(r.timings.duration);
  errors.add(r.status !== 200);
  if (r.status === 200) {
    joins.add(1);
  }
}

function v(data, metric, stat) {
  const m = data.metrics[metric];
  return m && m.values && m.values[stat] !== undefined ? m.values[stat] : null;
}

export function handleSummary(data) {
  const out = {
    test: 'K1 spike-queue',
    target_rate_per_s: RATE,
    iterations: v(data, 'iterations', 'count'),
    achieved_iterations_per_s: v(data, 'iterations', 'rate'),
    dropped_iterations: v(data, 'dropped_iterations', 'count') || 0,
    joins_ok: v(data, 'joins_ok', 'count'),
    error_rate: v(data, 'errors', 'rate'),
    join_ms: { p50: v(data, 'join_ms', 'med'), p95: v(data, 'join_ms', 'p(95)'), p99: v(data, 'join_ms', 'p(99)'), max: v(data, 'join_ms', 'max') },
    session_ms: { p50: v(data, 'session_ms', 'med'), p95: v(data, 'session_ms', 'p(95)'), p99: v(data, 'session_ms', 'p(99)') },
    vus_max: v(data, 'vus_max', 'max'),
    note: 'k6 times each request from when it is sent (no intended-start correction); iterations k6 could not start are dropped_iterations',
  };
  return {
    stdout: '\n' + JSON.stringify(out, null, 2) + '\n',
    [`${OUT}/k1.json`]: JSON.stringify(out, null, 2),
  };
}
