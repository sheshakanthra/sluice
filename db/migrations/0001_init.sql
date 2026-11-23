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
-- Reverse order of creation. River's tables are owned by rivermigrate, not goose.
DROP TABLE IF EXISTS queue_snapshots;
DROP TABLE IF EXISTS webhook_events;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS event_user_quota;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS holds;
DROP SEQUENCE IF EXISTS hold_fence_seq;
DROP TABLE IF EXISTS seats;
DROP TABLE IF EXISTS events;
DROP TYPE IF EXISTS payment_status;
DROP TYPE IF EXISTS order_status;
DROP TYPE IF EXISTS hold_status;
DROP TYPE IF EXISTS seat_state;
DROP TYPE IF EXISTS event_status;
