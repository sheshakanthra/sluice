# DATA MODEL: SLUICE

Postgres is the source of truth. Redis is a rebuildable fast path and coordination layer (R1).

## 1. Postgres: `sluice` database (`db/migrations/0001_init.sql`)

```sql
-- +goose Up
CREATE TYPE event_status   AS ENUM ('SCHEDULED','ON_SALE','SOLD_OUT','CLOSED');
CREATE TYPE seat_state     AS ENUM ('AVAILABLE','HELD','SOLD');
CREATE TYPE hold_status    AS ENUM ('ACTIVE','PAYMENT_PENDING','CONSUMED','RELEASED','EXPIRED');
CREATE TYPE order_status   AS ENUM ('PENDING_PAYMENT','CONFIRMED','FAILED','EXPIRED','REFUNDED');
CREATE TYPE payment_status AS ENUM ('CREATED','AUTHORIZED','CAPTURED','VOIDED','FAILED','REFUNDED');

CREATE TABLE events (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  slug               text NOT NULL UNIQUE,
  name               text NOT NULL,
  venue              text NOT NULL,
  starts_at          timestamptz NOT NULL,
  prequeue_opens_at  timestamptz NOT NULL,
  sale_opens_at      timestamptz NOT NULL,
  capacity           int    NOT NULL CHECK (capacity > 0),
  price_paise        bigint NOT NULL CHECK (price_paise > 0),
  max_per_user       int    NOT NULL DEFAULT 4 CHECK (max_per_user BETWEEN 1 AND 10),
  status             event_status NOT NULL DEFAULT 'SCHEDULED',
  layout             jsonb  NOT NULL,              -- sections/rows/cols for rendering
  created_at         timestamptz NOT NULL DEFAULT now(),
  CHECK (prequeue_opens_at <= sale_opens_at AND sale_opens_at < starts_at)
);

CREATE TABLE seats (
  event_id    uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  seat_no     int  NOT NULL CHECK (seat_no >= 0),   -- 0..capacity-1, compact for Redis intsets
  label       text NOT NULL,                        -- e.g. C3-27
  section     text NOT NULL,                        -- e.g. C3
  row_no      int  NOT NULL,
  col_no      int  NOT NULL,
  state       seat_state NOT NULL DEFAULT 'AVAILABLE',
  hold_id     uuid,
  held_until  timestamptz,
  order_id    uuid,
  version     bigint NOT NULL DEFAULT 0,            -- bumps on every transition (fencing)
  PRIMARY KEY (event_id, seat_no),
  UNIQUE (event_id, label),
  CONSTRAINT seat_state_shape CHECK (
    (state = 'AVAILABLE' AND hold_id IS NULL     AND held_until IS NULL     AND order_id IS NULL) OR
    (state = 'HELD'      AND hold_id IS NOT NULL AND held_until IS NOT NULL AND order_id IS NULL) OR
    (state = 'SOLD'      AND hold_id IS NULL     AND held_until IS NULL     AND order_id IS NOT NULL)
  )
);
CREATE INDEX seats_hold_idx ON seats (hold_id) WHERE hold_id IS NOT NULL;

CREATE TABLE holds (
  id          uuid PRIMARY KEY,                     -- UUIDv5(NS_HOLD, user_id || ':' || idempotency_key)
  event_id    uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL,
  seat_nos    int[] NOT NULL CHECK (cardinality(seat_nos) BETWEEN 1 AND 10),
  status      hold_status NOT NULL DEFAULT 'ACTIVE',
  fence       bigint NOT NULL,                      -- from Redis INCR (or PG sequence on pg_only)
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE SEQUENCE hold_fence_seq;                     -- fence source when HOLD_PATH=pg_only
CREATE INDEX holds_expiry_idx ON holds (expires_at) WHERE status IN ('ACTIVE','PAYMENT_PENDING');
-- one active hold per user per event
CREATE UNIQUE INDEX holds_one_active_per_user ON holds (event_id, user_id)
  WHERE status IN ('ACTIVE','PAYMENT_PENDING');

CREATE TABLE orders (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  event_id      uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  user_id       uuid NOT NULL,
  hold_id       uuid NOT NULL UNIQUE REFERENCES holds(id),   -- one order per hold
  seat_nos      int[] NOT NULL,
  amount_paise  bigint NOT NULL CHECK (amount_paise > 0),
  currency      char(3) NOT NULL DEFAULT 'INR',
  status        order_status NOT NULL DEFAULT 'PENDING_PAYMENT',
  failure_code  text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  confirmed_at  timestamptz
);
CREATE INDEX orders_user_idx ON orders (user_id, created_at DESC);

CREATE TABLE payments (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  order_id             uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  attempt              int  NOT NULL CHECK (attempt BETWEEN 1 AND 3),
  provider             text NOT NULL DEFAULT 'mockpay',
  provider_intent_id   text UNIQUE,
  psp_idempotency_key  text NOT NULL UNIQUE,        -- order:<order_id>:attempt:<n>
  amount_paise         bigint NOT NULL CHECK (amount_paise > 0),
  status               payment_status NOT NULL DEFAULT 'CREATED',
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (order_id, attempt)
);
-- I2 backstop: at most one live payment per order
CREATE UNIQUE INDEX payments_one_live_per_order ON payments (order_id)
  WHERE status IN ('AUTHORIZED','CAPTURED');

CREATE TABLE tickets (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  event_id   uuid NOT NULL,
  seat_no    int  NOT NULL,
  order_id   uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL,
  code       text NOT NULL UNIQUE,                  -- e.g. SLC-7Q4K-92XM
  issued_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (event_id, seat_no) REFERENCES seats(event_id, seat_no) ON DELETE CASCADE,
  UNIQUE (event_id, seat_no)                        -- I1: the last line of defence
);

CREATE TABLE event_user_quota (
  event_id         uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  user_id          uuid NOT NULL,
  seats_purchased  int  NOT NULL CHECK (seats_purchased >= 0),
  max_allowed      int  NOT NULL,                   -- copied from events.max_per_user on insert
  PRIMARY KEY (event_id, user_id),
  CONSTRAINT quota_cap CHECK (seats_purchased <= max_allowed)   -- I4
);

CREATE TABLE idempotency_keys (
  user_id        uuid NOT NULL,
  key            text NOT NULL CHECK (length(key) BETWEEN 8 AND 128),
  method         text NOT NULL,
  route          text NOT NULL,                     -- route pattern, not the raw path
  request_hash   bytea NOT NULL,                    -- sha256(method|route|canonical JSON body)
  status         text NOT NULL CHECK (status IN ('STARTED','COMPLETED')),
  response_code  int,
  response_body  jsonb,
  locked_until   timestamptz NOT NULL,              -- STARTED rows become re-executable after this
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, key)
);
CREATE INDEX idempotency_gc_idx ON idempotency_keys (created_at);   -- GC after 24h

CREATE TABLE webhook_events (                        -- inbox
  provider      text NOT NULL,
  event_id      text NOT NULL,
  type          text NOT NULL,
  payload       jsonb NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  processed_at  timestamptz,
  PRIMARY KEY (provider, event_id)                  -- dedupe
);

CREATE TABLE queue_snapshots (                       -- R1: queue restore after Redis loss
  event_id   uuid PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
  presize    bigint,
  tail       bigint NOT NULL DEFAULT 0,
  cursor     bigint NOT NULL DEFAULT 0,
  sold_out   boolean NOT NULL DEFAULT false,
  taken_at   timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
-- drop in reverse order (write it out in the real file)
```
River creates its own tables (`river_job`, ...) through its migrator. `sluice migrate` runs goose first, then River.

**`0002_naive_baseline.sql`** (P5, baseline only, deliberately unsafe):
```sql
-- DEMO ONLY: used when BASELINE=naive to reproduce oversells. No constraints by design.
CREATE TABLE naive_sales (
  id        bigserial PRIMARY KEY,
  event_id  uuid NOT NULL,
  seat_no   int  NOT NULL,
  user_id   uuid NOT NULL,
  order_ref uuid NOT NULL,
  sold_at   timestamptz NOT NULL DEFAULT now()
);
```

## 2. Postgres: `mockpay` database (owned by the MockPay service only)
```sql
CREATE TABLE merchants (
  id              text PRIMARY KEY,                 -- 'sluice' (seeded)
  api_key_hash    bytea NOT NULL,
  webhook_url     text NOT NULL,
  webhook_secret  text NOT NULL
);
CREATE TABLE intents (
  id               text PRIMARY KEY,                -- pi_<uuidv7 hex>
  merchant_id      text NOT NULL REFERENCES merchants(id),
  idempotency_key  text NOT NULL,
  request_hash     bytea NOT NULL,
  merchant_ref     text NOT NULL,                   -- our order_id
  metadata         jsonb NOT NULL DEFAULT '{}',     -- event_id, attempt, run_id
  amount_paise     bigint NOT NULL CHECK (amount_paise > 0),
  currency         char(3) NOT NULL,
  status           text NOT NULL,                   -- requires_payment|authorized|captured|canceled|failed|refunded
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, idempotency_key)
);
CREATE INDEX intents_ref_idx ON intents (merchant_id, merchant_ref);
CREATE TABLE ledger_entries (                       -- append-only money movements
  id               bigserial PRIMARY KEY,
  intent_id        text NOT NULL REFERENCES intents(id),
  kind             text NOT NULL CHECK (kind IN ('AUTH','CAPTURE','VOID','REFUND')),
  amount_paise     bigint NOT NULL,
  idempotency_key  text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (intent_id, kind)                          -- full-amount ops only; no double capture per intent
);
CREATE TABLE idempotency (
  merchant_id    text NOT NULL,
  key            text NOT NULL,
  request_hash   bytea NOT NULL,
  response_code  int NOT NULL,
  response_body  jsonb NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (merchant_id, key)
);
CREATE TABLE webhook_deliveries (
  id               bigserial PRIMARY KEY,
  event_id         text NOT NULL,                   -- evt_...; duplicates share an event_id
  intent_id        text NOT NULL,
  type             text NOT NULL,
  payload          jsonb NOT NULL,
  attempt          int NOT NULL DEFAULT 0,
  next_attempt_at  timestamptz NOT NULL DEFAULT now(),
  delivered_at     timestamptz,
  last_error       text
);
CREATE INDEX webhook_due_idx ON webhook_deliveries (next_attempt_at) WHERE delivered_at IS NULL;
```
Duplicate-charge truth comes from the ledger: per `merchant_ref`, count intents that have a `CAPTURE` entry and no `REFUND` entry.

## 3. State machines (pure Go in `internal/orders` and `internal/inventory`, with exhaustive tests)
```
seat:     AVAILABLE ─hold→ HELD ─confirm→ SOLD
          HELD ─release/reclaim→ AVAILABLE
hold:     ACTIVE ─order created→ PAYMENT_PENDING ─confirm→ CONSUMED
          ACTIVE|PAYMENT_PENDING ─release→ RELEASED
          ACTIVE|PAYMENT_PENDING ─reclaim (expired)→ EXPIRED
order:    PENDING_PAYMENT ─confirm→ CONFIRMED
          PENDING_PAYMENT ─hold lost / quota / 3 failed attempts→ FAILED
          PENDING_PAYMENT ─hold expired→ EXPIRED
          CONFIRMED ─admin refund (not in v1 UI)→ REFUNDED
payment:  CREATED ─authorized→ AUTHORIZED ─captured→ CAPTURED ─refund→ REFUNDED
          CREATED ─declined→ FAILED
          AUTHORIZED ─void→ VOIDED
```
Every other transition is rejected. Out-of-order events (for example `captured` arriving before `authorized`) apply only if they move forward; regressions are ignored and counted.

## 4. Redis keyspace
Every per-event key carries the hash tag `{e:<eid>}`, so it's cluster-safe and Lua can touch all of them at once. Seat numbers are ints, so sets use the compact intset encoding.

| Key | Type | Meaning |
|---|---|---|
| `sl:{e:<eid>}:meta` | HASH | capacity, max_per_user, sale_opens_ms, price, status, loaded=1 |
| `sl:{e:<eid>}:avail` | SET\<int\> | seats free to hold |
| `sl:{e:<eid>}:sold` | SET\<int\> | seats sold |
| `sl:{e:<eid>}:hold:<hid>` | HASH | uid, seats (csv), exp_ms, fence, state (ACTIVE\|PAYMENT); `PEXPIREAT` exp + 10 min safety |
| `sl:{e:<eid>}:holds` | ZSET | hid → exp_ms (sweeper index) |
| `sl:{e:<eid>}:holding` | SET\<uid\> | users who currently hold seats |
| `sl:{e:<eid>}:fence` | INT | monotonic fence counter |
| `sl:{e:<eid>}:ver` | INT | seat-map version (bumps on every seat change) |
| `sl:{e:<eid>}:q:pre` | ZSET | uid → random score (pre-queue lottery) |
| `sl:{e:<eid>}:q:presize` | INT | ZCARD(pre), frozen at T0 (SET NX) |
| `sl:{e:<eid>}:q:tail` | INT | post-T0 join counter |
| `sl:{e:<eid>}:q:cursor` | INT | highest ticket offered |
| `sl:{e:<eid>}:q:user:<uid>` | STRING | `pre` or the ticket number; also stores rejoin count |
| `sl:{e:<eid>}:q:offers` | ZSET | ticket → claim deadline ms |
| `sl:{e:<eid>}:q:active` | ZSET | uid → admission expiry ms |
| `sl:{e:<eid>}:q:soldout` | STRING | `1` once sold out |
| `sl:{e:<eid>}:q:rate` | STRING | admit_rate_ema (written by tick) |
| `sl:{e:<eid>}:audit` | HASH | live audit cache (oversold, dup, orphan, at_ms), written by the worker every 5 s |
| `sl:{e:<eid>}:demo` | HASH | demo-rush metrics (bots, rps, p95s), demo mode only |
| `sl:rl:*` | — | redis_rate GCRA keys |

Redis config: `appendonly yes`, `appendfsync everysec`, **`maxmemory-policy noeviction`** (a silent eviction would be data loss; writes must fail loudly instead).

## 5. Lua script contracts (`internal/redisx/scripts/`)
Every script is atomic and idempotent where noted, and returns a status string first. The Go side passes **all** randomness and time in through ARGV; scripts never read the clock or call math.random.

| Script | Input (ARGV) | Effect | Returns |
|---|---|---|---|
| `hold_seats.lua` | hid, uid, now_ms, ttl_ms, seat... | If the hold exists → REPLAY. If `q:soldout` is set → SOLD_OUT. If uid is already in `holding` → HOLD_LIMIT. If any seat isn't in `avail` → TAKEN with the list. Otherwise SREM avail, INCR fence, HSET hold, ZADD holds, SADD holding, INCR ver. (The per-user quota is pre-checked in PG by the API and enforced at confirm.) | `OK fence exp` \| `REPLAY ...` \| `TAKEN s...` \| `HOLD_LIMIT` \| `SOLD_OUT` |
| `hold_best.lua` | hid, uid, now_ms, ttl_ms, qty | Same guards. If SCARD(avail) < qty → INSUFFICIENT n. Otherwise SPOP avail qty, then the same bookkeeping | `OK fence exp seats...` \| `INSUFFICIENT n` \| ... |
| `hold_extend.lua` | hid, new_exp_ms | Set state=PAYMENT and the new exp; ZADD holds | `OK` \| `MISSING` |
| `hold_consume.lua` | hid | SADD sold seats; DEL hold; ZREM holds; SREM holding uid; INCR ver | `OK` \| `MISSING` (idempotent) |
| `hold_release.lua` | hid | If present and not consumed: SADD avail seats; DEL; ZREM; SREM holding; INCR ver | `OK` \| `MISSING` (idempotent) |
| `queue_join.lua` | uid, now_ms, sale_opens_ms, rand_score, rejoin(0/1), max_rejoins | Existing entry and not a rejoin → return it. Sold out → SOLD_OUT. If now ≥ open and presize unset → freeze. Pre path: ZADD pre NX. Post path: ticket = presize + INCR tail | `PRE` \| `TICKET n` \| `SOLD_OUT` \| `REJOIN_LIMIT` |
| `queue_resolve.lua` | uid | ZRANK pre uid + 1 → store it as the user's ticket | `TICKET n` \| `NOT_FOUND` |
| `queue_claim.lua` | ticket, uid, now_ms, admission_ttl_ms | Sold out → SOLD_OUT. uid already active → ADMITTED (idempotent). Offer missing or expired → OFFER_EXPIRED. Otherwise ZREM offers, ZADD active | `ADMITTED exp` \| `OFFER_EXPIRED` \| `SOLD_OUT` |
| `queue_leave.lua` | uid | ZREM active; DEL user entry | `OK` |
| `queue_tick.lua` | now_ms, max_active, overadmit, min_admit, max_per_tick, offer_ttl_ms | The algorithm in ARCHITECTURE §5 | `PREQUEUE` \| `PAUSED` \| `SOLD_OUT` \| `ADMITTING k` |
| `snapshot.lua` | — | One read of cursor, presize, tail, soldout, SCARD avail, ZCARD holds, SCARD sold, ver, rate, ZCARD offers, ZCARD active | array |
| `inventory_load.lua` | capacity, avail..., sold... | Reset the event's inventory keys from the provided state (used by rebuild) | `OK` |

## 6. Rebuild procedures (R1)
- **Inventory rebuild** (admin endpoint, plus automatically when the worker sees `meta.loaded` missing): read `seats` and active holds from PG. avail = AVAILABLE seats plus HELD seats whose `held_until < now()` (these get reclaimed in PG first). sold = SOLD. Recreate the hold hashes and the `holds` ZSET from PG holds, then `inventory_load.lua`. Throughout, PG wins.
- **Queue restore:** from `queue_snapshots`, restore presize, tail, cursor and soldout. Users carry signed queue tokens (ticket inside), so positions survive. Tickets in `(cursor − OFFER_CLAIM_TTL × admit_rate, cursor]` get fresh offers. Users whose dedupe key was lost may rejoin, and their old token stays valid.

## 7. Seed data (`sluice seed`, idempotent)
| slug | Name (fictional) | Capacity | Layout | Price | Notes |
|---|---|---|---|---|---|
| `tatkal-chn-sbc` | Demo Express, Chennai → Bengaluru (Tatkal drop) | 500 | 10 coaches C1–C10 × 10 rows × 5 seats (3+2, aisle after col 3) | ₹1,250 (125000 paise) | Rush event. Sale opens `now + 10 min` on seed (admin can reset or open-sale) |
| `quiet-tuesday` | Quiet Tuesday, Chennai → Bengaluru | 500 | same | ₹1,250 | On sale immediately, no rush |

Labels are `C<coach>-<1..50>`. `seat_no = (coach−1)×50 + (row−1)×5 + (col−1)`. The layout JSON carries coach, row and col so the UI can draw it.

**Layout JSON** (`events.layout`, ADR 0005). It's a compact descriptor; the per-seat truth stays in `seats`:
```json
{ "version": 1, "kind": "train", "cols": 5, "aisle_after_col": 3, "seats_per_coach": 50,
  "coaches": [ { "coach": 1, "label": "C1", "first_seat_no": 0, "rows": 10, "seats": 50 }, ... ] }
```
Within a coach, `seat_no = first_seat_no + (row−1)×cols + (col−1)` and `label = <label>-<seat_no − first_seat_no + 1>`. Only the last coach can be partial (capacity not a multiple of 50).

**Seed rules:** the rush event's `sale_opens_at = now + 10 min` (or `--opens-in`). quiet-tuesday goes on sale at seed time (`ON_SALE`). `prequeue_opens_at` = seed time, `starts_at = sale_opens_at + 24 h`. Re-running seed never modifies an existing event: it fills in missing seats and refuses a capacity mismatch. Re-time with the admin open-sale endpoint.

`sluice seed --event loadtest-big --capacity 50000` (rush/test only) creates a large event for the k6 checkout-capacity test.
