-- MockPay's own database (DATA-MODEL §2). Owned by `sluice mockpay`, which
-- applies these migrations at start-up; SLUICE never connects to it.

-- +goose Up
CREATE TABLE merchants (
  id              text PRIMARY KEY,                 -- 'sluice' (upserted from config at start-up)
  api_key_hash    bytea NOT NULL,                   -- sha256(MOCKPAY_API_KEY)
  webhook_url     text NOT NULL,
  webhook_secret  text NOT NULL
);

CREATE TABLE intents (
  id               text PRIMARY KEY,                -- pi_<uuidv7 hex>
  merchant_id      text NOT NULL REFERENCES merchants(id),
  idempotency_key  text NOT NULL,
  request_hash     bytea NOT NULL,
  merchant_ref     text NOT NULL,                   -- SLUICE's order_id
  metadata         jsonb NOT NULL DEFAULT '{}',     -- event_id, attempt, run_id
  amount_paise     bigint NOT NULL CHECK (amount_paise > 0),
  currency         char(3) NOT NULL,
  status           text NOT NULL CHECK (status IN ('requires_payment','authorized','captured','canceled','failed','refunded')),
  return_url       text NOT NULL DEFAULT '',        -- hosted checkout redirects here (ADR 0007)
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, idempotency_key)
);
CREATE INDEX intents_ref_idx ON intents (merchant_id, merchant_ref);
CREATE INDEX intents_status_idx ON intents (merchant_id, status, updated_at);

CREATE TABLE ledger_entries (                       -- append-only money movements
  id               bigserial PRIMARY KEY,
  intent_id        text NOT NULL REFERENCES intents(id),
  kind             text NOT NULL CHECK (kind IN ('AUTH','CAPTURE','VOID','REFUND')),
  amount_paise     bigint NOT NULL,
  idempotency_key  text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (intent_id, kind)                          -- full-amount ops only; no double capture per intent
);

-- Append-only, enforced by the database rather than by convention (ADR 0007):
-- a money movement is corrected by a new entry (VOID, REFUND), never edited.
-- +goose StatementBegin
CREATE FUNCTION ledger_entries_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'ledger_entries is append-only (% rejected)', TG_OP USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER ledger_entries_no_update_delete
  BEFORE UPDATE OR DELETE OR TRUNCATE ON ledger_entries
  FOR EACH STATEMENT EXECUTE FUNCTION ledger_entries_append_only();

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

-- +goose Down
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS idempotency;
DROP TABLE IF EXISTS ledger_entries;
DROP FUNCTION IF EXISTS ledger_entries_append_only();
DROP TABLE IF EXISTS intents;
DROP TABLE IF EXISTS merchants;
