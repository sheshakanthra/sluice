// K2 (LOADTEST §5): checkout capacity on a 50,000-seat event. Each step is
// a constant-arrival-rate of STEPS[i] checkouts/s for STEP_S seconds (the
// defined K2 is STEPS=200, STEP_S=60). One iteration = POST /sessions,
// POST /test/admit, hold(1), order, pay(pm_ok). "checkout" latency is each
// hold/order/pay request (SPEC §6 checkout p95). The max sustainable rate is
// the highest step with checkout p95 <= 250 ms, errors <= 0.1% and no
// dropped iterations.
//
// Run through `task k6:checkout` (grafana/k6 on the compose network). Every
// iteration uses its own synthetic X-Forwarded-For (per-IP limits apply per
// simulated client). Thresholds are always-true on purpose: they make the
// per-step submetrics appear in the summary; handleSummary judges.
import http from 'k6/http';
import exec from 'k6/execution';
import { Rate, Trend, Counter } from 'k6/metrics';

const BASE = (__ENV.BASE || 'http://caddy:8000') + '/api/v1';
const EVENT = __ENV.EVENT;
const STEPS = (__ENV.STEPS || '200').split(',').map((s) => parseInt(s, 10));
const STEP_S = parseInt(__ENV.STEP_S || '60', 10);
const OUT = __ENV.OUT || '.';
const P95_LIMIT_MS = 250;
const ERR_LIMIT = 0.001;

const checkoutMs = new Trend('checkout_ms', true);
const errors = new Rate('errors');
const checkouts = new Counter('checkouts_ok');

const scenarios = {};
const thresholds = {};
STEPS.forEach((rate, i) => {
  const name = `s${rate}`;
  scenarios[name] = {
    executor: 'constant-arrival-rate',
    rate,
    timeUnit: '1s',
    duration: `${STEP_S}s`,
    startTime: `${i * STEP_S}s`,
    preAllocatedVUs: Math.min(rate * 2, 600),
    maxVUs: 1000,
    tags: { step: String(rate) },
  };
  thresholds[`checkout_ms{step:${rate}}`] = ['p(95)>=0'];
  thresholds[`errors{step:${rate}}`] = ['rate>=0'];
  thresholds[`checkouts_ok{step:${rate}}`] = ['count>=0'];
  thresholds[`dropped_iterations{scenario:${name}}`] = ['count>=0'];
  thresholds[`iterations{scenario:${name}}`] = ['count>=0'];
});

export const options = {
  scenarios,
  thresholds,
  setupTimeout: '60s',
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max', 'count'],
};

function xff(i) {
  return `198.${18 + ((i >> 16) & 1)}.${(i >> 8) & 255}.${i & 255}`;
}

// setup: open the sale now (holds need an on-sale event).
export function setup() {
  const r = http.post(`${BASE}/admin/events/${EVENT}/open-sale`, JSON.stringify({ opens_in_s: 0 }), {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${__ENV.ADMIN_TOKEN}` },
  });
  if (r.status !== 200) {
    throw new Error(`open-sale: HTTP ${r.status} ${r.body}`);
  }
}

function ik() {
  // UUIDv4-shaped; only needs to be unique per intent.
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function post(path, body, token, key, route) {
  const headers = { 'Content-Type': 'application/json', 'X-Forwarded-For': xff(exec.scenario.iterationInTest) };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  if (key) {
    headers['Idempotency-Key'] = key;
  }
  return http.post(`${BASE}${path}`, body === null ? null : JSON.stringify(body), { headers, tags: { route } });
}

export default function () {
  let r = post('/sessions', null, '', '', 'session');
  if (r.status !== 201) {
    errors.add(true);
    return;
  }
  const session = r.json('session_token');
  const uid = r.json('user_id');
  r = post('/test/admit', { event_id: EVENT, user_id: uid }, '', '', 'admit');
  if (r.status !== 200) {
    errors.add(true);
    return;
  }
  const adm = r.json('admission_token');

  r = post(`/events/${EVENT}/holds`, { quantity: 1 }, adm, ik(), 'hold');
  checkoutMs.add(r.timings.duration);
  if (r.status !== 201) {
    errors.add(true);
    return;
  }
  r = post('/orders', { hold_id: r.json('hold_id') }, adm, ik(), 'order');
  checkoutMs.add(r.timings.duration);
  if (r.status !== 201) {
    errors.add(true);
    return;
  }
  r = post(`/orders/${r.json('order_id')}/pay`, { payment_method: 'pm_ok' }, session, ik(), 'pay');
  checkoutMs.add(r.timings.duration);
  // pm_ok answers CONFIRMED, or PENDING_PAYMENT if MockPay injected a
  // timeout-after-commit fault (the order then confirms via webhook/poll).
  const ok = r.status === 200 && ['CONFIRMED', 'PENDING_PAYMENT'].includes(r.json('status'));
  errors.add(!ok);
  if (ok) {
    checkouts.add(1);
  }
}

function v(data, metric, stat) {
  const m = data.metrics[metric];
  return m && m.values && m.values[stat] !== undefined ? m.values[stat] : null;
}

export function handleSummary(data) {
  const steps = STEPS.map((rate) => {
    const p95 = v(data, `checkout_ms{step:${rate}}`, 'p(95)');
    const err = v(data, `errors{step:${rate}}`, 'rate');
    const dropped = v(data, `dropped_iterations{scenario:s${rate}}`, 'count') || 0;
    return {
      rate_per_s: rate,
      iterations: v(data, `iterations{scenario:s${rate}}`, 'count'),
      checkouts_ok: v(data, `checkouts_ok{step:${rate}}`, 'count'),
      dropped_iterations: dropped,
      checkout_ms: {
        p50: v(data, `checkout_ms{step:${rate}}`, 'med'),
        p95,
        p99: v(data, `checkout_ms{step:${rate}}`, 'p(99)'),
        max: v(data, `checkout_ms{step:${rate}}`, 'max'),
      },
      error_rate: err,
      sustainable: p95 !== null && p95 <= P95_LIMIT_MS && (err || 0) <= ERR_LIMIT && dropped === 0,
    };
  });
  const ok = steps.filter((s) => s.sustainable);
  const out = {
    test: 'K2 checkout',
    step_s: STEP_S,
    steps,
    max_sustainable_checkouts_per_s: ok.length ? Math.max(...ok.map((s) => s.rate_per_s)) : null,
    criteria: `checkout p95 <= ${P95_LIMIT_MS} ms, errors <= ${ERR_LIMIT * 100}%, no dropped iterations`,
    note: 'k6 times each request from when it is sent (no intended-start correction)',
  };
  return {
    stdout: '\n' + JSON.stringify(out, null, 2) + '\n',
    [`${OUT}/k2.json`]: JSON.stringify(out, null, 2),
  };
}
