package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// rebuildLockTTL bounds how long one process may own an event's rebuild.
const rebuildLockTTL = 30 * time.Second

// RebuildResult summarises what was loaded into Redis.
type RebuildResult struct {
	EventID   uuid.UUID `json:"event_id"`
	Available int       `json:"available"`
	Held      int       `json:"held"`
	Sold      int       `json:"sold"`
	Holds     int       `json:"holds"`
	Reclaimed int       `json:"reclaimed"`
	Fence     int64     `json:"fence_floor"`
}

// Loaded reports whether the event's Redis inventory is loaded.
func (s *Service) Loaded(ctx context.Context, eventID uuid.UUID) (bool, error) {
	v, err := s.rdb.HGet(ctx, keys.Meta(eventID), "loaded").Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read meta.loaded: %w", err)
	}
	return v == "1", nil
}

// EnsureLoaded rebuilds the event's Redis inventory from PG when meta.loaded
// is missing (worker start-up, or after Redis lost its data). One process
// rebuilds at a time (SET NX lock); the others get ErrNotLoaded.
func (s *Service) EnsureLoaded(ctx context.Context, eventID uuid.UUID) error {
	if s.pgOnly {
		return nil
	}
	ok, err := s.Loaded(ctx, eventID)
	if err != nil || ok {
		return err
	}
	lock := keys.RebuildLock(eventID)
	token := uuid.NewString()
	got, err := s.rdb.SetNX(ctx, lock, token, rebuildLockTTL).Result()
	if err != nil {
		return fmt.Errorf("rebuild lock: %w", err)
	}
	if !got {
		return ErrNotLoaded
	}
	defer func() {
		// Delete only our own lock (it may have expired and been retaken).
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
		defer cancel()
		if v, err := s.rdb.Get(dctx, lock).Result(); err == nil && v == token {
			_ = s.rdb.Del(dctx, lock).Err() // best effort: the lock expires anyway
		}
	}()
	res, err := s.Rebuild(ctx, eventID)
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "inventory loaded into redis", slog.String("event_id", eventID.String()),
		slog.Int("available", res.Available), slog.Int("held", res.Held), slog.Int("sold", res.Sold), slog.Int("holds", res.Holds))
	return nil
}

// Rebuild replaces the event's Redis inventory with Postgres state
// (DATA-MODEL §6, R1). Expired holds are reclaimed in PG first; then one
// REPEATABLE READ snapshot gives the seats, the live holds and the fence
// floor, and inventory_load writes them atomically. PG wins throughout.
//
// Run while holds are in flight, a grant that commits between the snapshot
// and the load is dropped from Redis; PG still refuses any second hold on
// its seats (compensation + repair), so this costs retries, never seats.
func (s *Service) Rebuild(ctx context.Context, eventID uuid.UUID) (RebuildResult, error) {
	res := RebuildResult{EventID: eventID}
	if s.pgOnly {
		return res, errors.New("rebuild: HOLD_PATH=pg_only has no Redis inventory")
	}
	sw, err := s.reclaimExpired(ctx)
	if err != nil {
		return res, err
	}
	res.Reclaimed = sw.Reclaimed

	now := s.now()
	var (
		ev    gen.Event
		seats []gen.ListSeatStatesRow
		holds []gen.ListLiveHoldsByEventRow
	)
	err = db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		q := gen.New(tx)
		var err error
		if ev, err = q.GetEventByID(ctx, eventID); errors.Is(err, pgx.ErrNoRows) {
			return ErrEventNotFound
		} else if err != nil {
			return fmt.Errorf("get event: %w", err)
		}
		if seats, err = q.ListSeatStates(ctx, eventID); err != nil {
			return fmt.Errorf("list seats: %w", err)
		}
		if holds, err = q.ListLiveHoldsByEvent(ctx, eventID); err != nil {
			return fmt.Errorf("list holds: %w", err)
		}
		if res.Fence, err = q.MaxHoldFence(ctx, eventID); err != nil {
			return fmt.Errorf("max fence: %w", err)
		}
		return nil
	}, db.Isolation(pgx.RepeatableRead), db.ReadOnly())
	if err != nil {
		return res, err
	}

	var avail, sold []any
	heldBy := map[uuid.UUID][]string{}
	for _, st := range seats {
		switch {
		case st.State == gen.SeatStateSOLD:
			sold = append(sold, st.SeatNo)
		case st.State == gen.SeatStateHELD && st.HoldID != nil && st.HeldUntil != nil && st.HeldUntil.After(now):
			heldBy[*st.HoldID] = append(heldBy[*st.HoldID], strconv.Itoa(int(st.SeatNo)))
			res.Held++
		default: // AVAILABLE, or HELD past held_until (reclaimable; a hold takes it over in PG)
			avail = append(avail, st.SeatNo)
		}
	}
	res.Available, res.Sold = len(avail), len(sold)

	args := make([]any, 0, 10+len(avail)+len(sold)+6*len(holds))
	args = append(args, keys.Prefix(eventID), now.UnixMilli(), res.Fence, ev.Capacity, ev.MaxPerUser,
		ev.SaleOpensAt.UnixMilli(), ev.PricePaise, string(ev.Status), len(avail))
	args = append(args, avail...)
	args = append(args, len(sold))
	args = append(args, sold...)
	for _, h := range holds {
		seatsCSV := heldBy[h.ID]
		if len(seatsCSV) == 0 || !h.ExpiresAt.After(now) {
			continue // expired: its seats are in avail
		}
		state := "ACTIVE"
		if h.Status == gen.HoldStatusPAYMENTPENDING {
			state = "PAYMENT"
		}
		args = append(args, h.ID.String(), h.UserID.String(), h.ExpiresAt.UnixMilli(), h.Fence, state, strings.Join(seatsCSV, ","))
		res.Holds++
	}
	r, err := s.sc.Run(ctx, redisx.InventoryLoad, loadKeys(eventID), args...)
	if err != nil {
		return res, err
	}
	if r.Status != "OK" {
		return res, fmt.Errorf("inventory_load: unexpected status %q", r.Status)
	}
	return res, nil
}

func loadKeys(eid uuid.UUID) []string {
	return []string{keys.Meta(eid), keys.Avail(eid), keys.Sold(eid), keys.Holds(eid), keys.Holding(eid), keys.Fence(eid), keys.Ver(eid)}
}
