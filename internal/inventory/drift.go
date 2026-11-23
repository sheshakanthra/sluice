package inventory

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// RepairDrift compares Redis's seat sets with Postgres for one event and
// repairs Redis where they disagree (ARCHITECTURE §4.5, last bullet). It
// returns how many seats were fixed. PG is the judge:
//   - PG SOLD, Redis not sold: mark sold.
//   - PG HELD (unexpired), Redis offers it: take it out of avail.
//   - PG AVAILABLE, Redis has it sold: make it available.
//   - PG AVAILABLE, Redis neither offers it nor has it in a live Redis
//     hold: make it available (a leaked seat).
//
// A seat that a Redis hold still lists is left alone in the last case: the
// hold may be a grant whose PG transaction is still in flight, and the
// sweeper's Redis pass resolves it at its expiry. Not loaded or pg_only:
// nothing to do.
func (s *Service) RepairDrift(ctx context.Context, eventID uuid.UUID) (int, error) {
	if s.pgOnly {
		return 0, nil
	}
	loaded, err := s.Loaded(ctx, eventID)
	if err != nil || !loaded {
		return 0, err
	}
	avail, err := s.redisSeatSet(ctx, keys.Avail(eventID))
	if err != nil {
		return 0, err
	}
	sold, err := s.redisSeatSet(ctx, keys.Sold(eventID))
	if err != nil {
		return 0, err
	}
	inHold, err := s.redisHeldSeats(ctx, eventID)
	if err != nil {
		return 0, err
	}
	rows, err := s.q.ListSeatStates(ctx, eventID)
	if err != nil {
		return 0, fmt.Errorf("seat states: %w", err)
	}
	now := s.now()
	args := []any{keys.Prefix(eventID), 0}
	fixed := 0
	for _, r := range rows {
		sn := r.SeatNo
		var want string
		switch r.State {
		case gen.SeatStateSOLD:
			if !sold[sn] || avail[sn] {
				want = "S"
			}
		case gen.SeatStateHELD:
			if avail[sn] && r.HeldUntil != nil && r.HeldUntil.After(now) {
				want = "H"
			}
		case gen.SeatStateAVAILABLE:
			if sold[sn] || (!avail[sn] && !inHold[sn]) {
				want = "A"
			}
		}
		if want != "" {
			args = append(args, sn, want)
			fixed++
		}
	}
	if fixed == 0 {
		return 0, nil
	}
	if _, err := s.sc.Run(ctx, redisx.InventoryRepair, repairKeys(eventID), args...); err != nil {
		return 0, fmt.Errorf("repair: %w", err)
	}
	if s.metrics != nil {
		s.metrics.repairs.Add(float64(fixed))
	}
	s.log.WarnContext(ctx, "inventory drift repaired", "event_id", eventID.String(), "seats", fixed)
	return fixed, nil
}

func (s *Service) redisSeatSet(ctx context.Context, key string) (map[int32]bool, error) {
	ms, err := s.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers: %w", err)
	}
	out := make(map[int32]bool, len(ms))
	for _, m := range ms {
		if n, err := strconv.ParseInt(m, 10, 32); err == nil {
			out[int32(n)] = true
		}
	}
	return out, nil
}

// redisHeldSeats is every seat listed by a hold in the Redis expiry index.
func (s *Service) redisHeldSeats(ctx context.Context, eventID uuid.UUID) (map[int32]bool, error) {
	ids, err := s.rdb.ZRange(ctx, keys.Holds(eventID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("holds index: %w", err)
	}
	out := map[int32]bool{}
	for _, id := range ids {
		hid, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		seats, err := s.rdb.HGet(ctx, keys.Hold(eventID, hid), "seats").Result()
		if err != nil {
			continue // released between the two reads
		}
		for _, f := range strings.Split(seats, ",") {
			if n, err := strconv.ParseInt(f, 10, 32); err == nil {
				out[int32(n)] = true
			}
		}
	}
	return out, nil
}
