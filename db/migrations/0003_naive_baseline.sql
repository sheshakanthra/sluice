-- +goose Up
-- BASELINE ONLY (LOADTEST §4, scenario B0; ADR 0011). The tables a hurried
-- team would write: a cart/order row and a sales table with NO unique
-- constraint on (event_id, seat_no). The naive code checks, then inserts, in
-- separate statements, so concurrent buyers can sell one seat twice. That is
-- the point: the auditor must catch it. Nothing in the real system reads or
-- writes these tables; the API refuses BASELINE=naive outside APP_ENV
-- rush/test.
CREATE TABLE naive_orders (
  order_ref     uuid PRIMARY KEY,                    -- the pseudo-hold id, then the order id
  event_id      uuid NOT NULL,
  user_id       uuid NOT NULL,
  seat_nos      int[] NOT NULL,
  amount_paise  bigint NOT NULL,
  status        text NOT NULL DEFAULT 'PENDING_PAYMENT',  -- PENDING_PAYMENT | CONFIRMED | FAILED
  attempts      int NOT NULL DEFAULT 1,
  pay_status    text NOT NULL DEFAULT 'CREATED',          -- last payment: CREATED | CAPTURED | FAILED
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE naive_sales (                           -- deliberately no UNIQUE (event_id, seat_no)
  id         bigserial PRIMARY KEY,
  event_id   uuid NOT NULL,
  seat_no    int NOT NULL,
  order_ref  uuid NOT NULL,
  user_id    uuid NOT NULL,
  sold_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX naive_sales_lookup_idx ON naive_sales (event_id, seat_no);   -- an index, not a constraint

-- +goose Down
DROP TABLE IF EXISTS naive_sales;
DROP TABLE IF EXISTS naive_orders;
