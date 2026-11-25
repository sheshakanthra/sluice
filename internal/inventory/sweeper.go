package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// sweepBatch is the per-transaction and per-event batch (ARCHITECTURE §4.4).
const sweepBatch = 200

// sweepTimeout bounds one sweep round.
const sweepTimeout = 30 * time.Second

// SweepResult counts what one sweep did.
type SweepResult struct {
	Reclaimed     int // PG: ACTIVE|PAYMENT_PENDING -> EXPIRED, seats freed
	RedisReleased int
	RedisConsumed int
	RedisExtended int
}

// RunSweeper sweeps every interval until ctx is done (I5: an expired hold
// is reclaimed within two intervals). Safe to run in several workers: PG
// work uses SKIP LOCKED and conditional updates, and the Redis scripts are
// idempotent.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		// Bound each round so a stalled datastore cannot wedge the loop (P7).
		sctx, cancel := context.WithTimeout(ctx, sweepTimeout)
		res, err := s.SweepOnce(sctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.log.ErrorContext(ctx, "sweep", slog.String("error", err.Error()))
		} else if res != (SweepResult{}) {
			s.log.DebugContext(ctx, "sweep", slog.Int("reclaimed", res.Reclaimed), slog.Int("redis_released", res.RedisReleased),
				slog.Int("redis_consumed", res.RedisConsumed), slog.Int("redis_extended", res.RedisExtended))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// SweepOnce runs one sweep: Postgres first, then Redis (ARCHITECTURE §4.4).
// It also loads any open event whose Redis inventory is missing (R1).
func (s *Service) SweepOnce(ctx context.Context) (SweepResult, error) {
	res, err := s.reclaimExpired(ctx)
	if err != nil || s.pgOnly {
		return res, err
	}
	evs, err := s.q.ListOpenEvents(ctx)
	if err != nil {
		return res, fmt.Errorf("list events: %w", err)
	}
	var errs []error
	for _, ev := range evs {
		if err := s.EnsureLoaded(ctx, ev.ID); err != nil {
			if !errors.Is(err, ErrNotLoaded) {
				errs = append(errs, fmt.Errorf("event %s: %w", ev.ID, err))
			}
			continue
		}
		if err := s.sweepRedis(ctx, ev.ID, &res); err != nil {
			errs = append(errs, fmt.Errorf("event %s: %w", ev.ID, err))
		}
	}
	return res, errors.Join(errs...)
}

// reclaimExpired expires PG holds past expires_at and frees their seats, in
// batches, then mirrors each into Redis with hold_release.
func (s *Service) reclaimExpired(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	for {
		now := s.now()
		var batch, reclaimed []gen.ListExpiredHoldsRow
		err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
			reclaimed = reclaimed[:0]
			q := gen.New(tx)
			var err error
			if batch, err = q.ListExpiredHolds(ctx, gen.ListExpiredHoldsParams{Now: now, BatchSize: sweepBatch}); err != nil {
				return fmt.Errorf("list expired holds: %w", err)
			}
			for _, h := range batch {
				n, err := q.ExpireHold(ctx, gen.ExpireHoldParams{ID: h.ID, Now: now})
				if err != nil {
					return fmt.Errorf("expire hold: %w", err)
				}
				if n != 1 {
					continue
				}
				if _, err := q.ReleaseSeats(ctx, gen.ReleaseSeatsParams{EventID: h.EventID, HoldID: h.ID}); err != nil {
					return fmt.Errorf("free seats: %w", err)
				}
				if s.reclaim != nil {
					if err := s.reclaim(ctx, tx, h.ID); err != nil {
						return fmt.Errorf("reclaim hook: %w", err)
					}
				}
				reclaimed = append(reclaimed, h)
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		res.Reclaimed += len(reclaimed)
		if s.metrics != nil {
			s.metrics.expired.Add(float64(len(reclaimed)))
		}
		if !s.pgOnly {
			for _, h := range reclaimed {
				if err := s.releaseRedis(ctx, h.EventID, h.ID, h.UserID, freeAdmission); err != nil {
					// The Redis pass of a later sweep releases it.
					s.log.WarnContext(ctx, "redis release of reclaimed hold", slog.String("error", err.Error()))
				}
			}
		}
		if len(batch) < sweepBatch || ctx.Err() != nil {
			return res, nil
		}
	}
}

// sweepRedis resolves holds in the Redis expiry index whose exp has passed,
// using PG as the judge: released/expired/unknown -> hold_release, consumed
// -> hold_consume, extended in PG -> hold_extend. A hold still live and
// expired in PG is left for the next PG pass.
func (s *Service) sweepRedis(ctx context.Context, eventID uuid.UUID, res *SweepResult) error {
	now := s.now()
	ids, err := s.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key: keys.Holds(eventID), Start: "-inf", Stop: strconv.FormatInt(now.UnixMilli(), 10),
		ByScore: true, Count: sweepBatch,
	}).Result()
	if err != nil {
		return fmt.Errorf("redis expiry index: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	hids := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if u, err := uuid.Parse(id); err == nil {
			hids = append(hids, u)
		} else {
			_ = s.rdb.ZRem(ctx, keys.Holds(eventID), id).Err() // garbage member; best effort
		}
	}
	rows, err := s.q.GetHoldsByIDs(ctx, hids)
	if err != nil {
		return fmt.Errorf("holds by id: %w", err)
	}
	pg := make(map[uuid.UUID]gen.GetHoldsByIDsRow, len(rows))
	for _, r := range rows {
		pg[r.ID] = r
	}
	for _, hid := range hids {
		r, ok := pg[hid]
		switch {
		case !ok: // a grant PG never committed (compensated): its user is still shopping
			err = s.releaseRedis(ctx, eventID, hid, uuid.Nil, keepAdmission)
			res.RedisReleased++
		case r.Status == gen.HoldStatusEXPIRED || r.Status == gen.HoldStatusRELEASED:
			err = s.releaseRedis(ctx, eventID, hid, r.UserID, freeAdmission)
			res.RedisReleased++
		case r.Status == gen.HoldStatusCONSUMED:
			err = s.consumeRedis(ctx, eventID, hid, r.UserID)
			res.RedisConsumed++
		case r.ExpiresAt.After(now):
			_, err = s.sc.Run(ctx, redisx.HoldExtend, extendKeys(eventID, hid), hid.String(), r.ExpiresAt.UnixMilli())
			res.RedisExtended++
		}
		if err != nil {
			return err
		}
	}
	return nil
}
