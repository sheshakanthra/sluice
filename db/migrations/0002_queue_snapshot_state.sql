-- +goose Up
-- R1 queue restore (ADR 0009): besides the counters, the snapshot carries the
-- offers and admissions that were live when it was taken, so a restore can
-- re-create exactly those and never admit a user twice.
ALTER TABLE queue_snapshots
  ADD COLUMN admit_rate    double precision NOT NULL DEFAULT 0,
  ADD COLUMN offers        bigint[] NOT NULL DEFAULT '{}',   -- outstanding offer tickets
  ADD COLUMN active_uids   uuid[]   NOT NULL DEFAULT '{}',   -- admitted users ...
  ADD COLUMN active_exp_ms bigint[] NOT NULL DEFAULT '{}',   -- ... and their expiries (same order)
  ADD CONSTRAINT queue_snapshots_active_len
    CHECK (cardinality(active_uids) = cardinality(active_exp_ms));

-- +goose Down
ALTER TABLE queue_snapshots
  DROP CONSTRAINT IF EXISTS queue_snapshots_active_len,
  DROP COLUMN IF EXISTS active_exp_ms,
  DROP COLUMN IF EXISTS active_uids,
  DROP COLUMN IF EXISTS offers,
  DROP COLUMN IF EXISTS admit_rate;
