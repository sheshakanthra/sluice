package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// sideEffectTimeout bounds Redis clean-up that runs after the request's own
// work is decided (compensation, repair), detached from client cancellation.
const sideEffectTimeout = 2 * time.Second

// errConcurrentReplay: another request inserted the same hold id first.
var errConcurrentReplay = errors.New("hold id inserted concurrently")

func holdScriptKeys(eid, hid uuid.UUID) []string {
	return []string{keys.Meta(eid), keys.Avail(eid), keys.Holds(eid), keys.Holding(eid),
		keys.Fence(eid), keys.Ver(eid), keys.QSoldOut(eid), keys.Hold(eid, hid)}
}

func releaseKeys(eid, hid uuid.UUID) []string {
	return []string{keys.Hold(eid, hid), keys.Holds(eid), keys.Holding(eid), keys.Avail(eid), keys.Ver(eid), keys.Sold(eid),
		keys.QActive(eid)}
}

func consumeKeys(eid, hid uuid.UUID) []string {
	return []string{keys.Hold(eid, hid), keys.Holds(eid), keys.Holding(eid), keys.Sold(eid), keys.Ver(eid), keys.Avail(eid),
		keys.QActive(eid)}
}

func extendKeys(eid, hid uuid.UUID) []string { return []string{keys.Hold(eid, hid), keys.Holds(eid)} }

func repairKeys(eid uuid.UUID) []string {
	return []string{keys.Avail(eid), keys.Sold(eid), keys.Holds(eid), keys.Holding(eid), keys.Ver(eid), keys.QActive(eid)}
}

// admission says whether a Redis hold mirror also releases the user's queue
// admission (ADR 0010 addendum).
type admission bool

const (
	keepAdmission admission = false // the user is still shopping (compensation, unknown hold)
	freeAdmission admission = true  // the hold or its order has ended in PG
)

// releaseRedis runs hold_release for hid. With freeAdmission the same script
// removes uid from q:active unless uid still holds, so the admission
// controller can offer the slot at once instead of after ADMISSION_TTL.
// uid may be uuid.Nil: the script then takes it from the hold hash.
func (s *Service) releaseRedis(ctx context.Context, eid, hid, uid uuid.UUID, a admission) error {
	flag := 0
	if a == freeAdmission {
		flag = 1
	}
	r, err := s.sc.Run(ctx, redisx.HoldRelease, releaseKeys(eid, hid), hid.String(), uidArg(uid), flag)
	if err != nil {
		return err
	}
	s.countFreed(r)
	return nil
}

// consumeRedis runs hold_consume for hid; the buyer's admission is released
// in the same script (the order is CONFIRMED).
func (s *Service) consumeRedis(ctx context.Context, eid, hid, uid uuid.UUID) error {
	r, err := s.sc.Run(ctx, redisx.HoldConsume, consumeKeys(eid, hid), hid.String(), uidArg(uid))
	if err != nil {
		return err
	}
	s.countFreed(r)
	return nil
}

func uidArg(uid uuid.UUID) string {
	if uid == uuid.Nil {
		return ""
	}
	return uid.String()
}

// countFreed counts an admission the script released (its last field is 1).
func (s *Service) countFreed(r redisx.Reply) {
	if s.metrics == nil || len(r.Fields) == 0 {
		return
	}
	if n, err := r.Int(len(r.Fields) - 1); err == nil && n == 1 {
		s.metrics.admissionsFreed.Inc()
	}
}

// grant is what the fast path (or pg_only) decided before Postgres commits.
type grant struct {
	hid, uid uuid.UUID
	seats    []int32 // empty for a pg_only best-available hold
	qty      int
	fence    int64
	exp      time.Time
}

type takeover struct {
	id    uuid.UUID
	seats []int32
}

// Hold places an all-or-nothing hold (ARCHITECTURE §4.2): quota pre-check
// in PG, the Redis script (skipped with PGOnly), then the authoritative PG
// transaction. If PG refuses a Redis grant, the grant is undone in Redis
// (compensation) and the conflicting seats are repaired from PG.
func (s *Service) Hold(ctx context.Context, eventID, userID uuid.UUID, idemKey string, req Request) (Hold, error) {
	h, err := s.hold(ctx, eventID, userID, idemKey, req)
	s.count(resultOf(h, err))
	return h, err
}

func resultOf(h Hold, err error) string {
	switch {
	case err == nil && h.Replayed:
		return ResultReplay
	case err == nil:
		return ResultOK
	case errors.Is(err, ErrSeatsTaken):
		return ResultTaken
	case errors.Is(err, ErrInsufficient):
		return ResultInsufficient
	case errors.Is(err, ErrSoldOut):
		return ResultSoldOut
	case errors.Is(err, ErrHoldLimit):
		return ResultHoldLimit
	case errors.Is(err, ErrQuotaExceeded):
		return ResultQuota
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrEventNotFound):
		return ResultInvalid
	default:
		return ResultError
	}
}

func (s *Service) hold(ctx context.Context, eventID, userID uuid.UUID, idemKey string, req Request) (Hold, error) {
	ev, err := s.events.get(ctx, eventID.String())
	if err != nil {
		return Hold{}, err
	}
	seats, qty, err := s.validate(ev, req)
	if err != nil {
		return Hold{}, err
	}
	now := s.now()
	g := grant{hid: HoldID(userID, idemKey), uid: userID, seats: seats, qty: qty, exp: now.Add(s.ttl).Truncate(time.Millisecond)}
	if !s.pgOnly {
		if g, err = s.redisGrant(ctx, ev, g, now); err != nil {
			return Hold{}, err
		}
	}
	h, takeovers, err := s.commit(ctx, ev, g, now)
	if err != nil {
		if !s.pgOnly && (errors.Is(err, ErrSeatsTaken) || errors.Is(err, ErrHoldLimit) || errors.Is(err, ErrQuotaExceeded)) {
			s.compensate(ctx, ev, g, err)
		}
		return Hold{}, err
	}
	if len(takeovers) > 0 {
		if s.metrics != nil {
			s.metrics.expired.Add(float64(len(takeovers)))
		}
		if !s.pgOnly {
			s.repairTakeovers(ctx, ev, h, takeovers)
		}
	}
	return h, nil
}

func (s *Service) validate(ev *EventInfo, req Request) ([]int32, int, error) {
	switch {
	case len(req.SeatNos) > 0 && req.Quantity != 0:
		return nil, 0, fmt.Errorf("%w: send seat_nos or quantity, not both", ErrInvalidRequest)
	case len(req.SeatNos) == 0 && req.Quantity <= 0:
		return nil, 0, fmt.Errorf("%w: send seat_nos or a positive quantity", ErrInvalidRequest)
	}
	n := max(len(req.SeatNos), req.Quantity)
	if n > s.max {
		return nil, 0, fmt.Errorf("%w: at most %d seats per hold", ErrInvalidRequest, s.max)
	}
	if req.Quantity > 0 {
		return nil, n, nil
	}
	seats := slices.Clone(req.SeatNos)
	slices.Sort(seats)
	for i, sn := range seats {
		if sn < 0 || sn >= ev.Capacity {
			return nil, 0, fmt.Errorf("%w: seat %d does not exist", ErrInvalidRequest, sn)
		}
		if i > 0 && seats[i-1] == sn {
			return nil, 0, fmt.Errorf("%w: seat %d requested twice", ErrInvalidRequest, sn)
		}
	}
	return seats, n, nil
}

// checkQuota is the PG pre-check of max_per_user (enforced again at confirm).
// It runs inside the hold transaction, after the Redis grant, so requests
// Redis rejects never touch Postgres.
func checkQuota(ctx context.Context, q *gen.Queries, ev *EventInfo, userID uuid.UUID, n int) error {
	bought, limit := int32(0), ev.MaxPerUser
	qr, err := q.GetQuota(ctx, gen.GetQuotaParams{EventID: ev.ID, UserID: userID})
	switch {
	case err == nil:
		bought, limit = qr.SeatsPurchased, qr.MaxAllowed
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("quota pre-check: %w", err)
	}
	if int(bought)+n > int(limit) {
		return fmt.Errorf("%w: %d bought, %d requested, limit %d", ErrQuotaExceeded, bought, n, limit)
	}
	return nil
}

// redisGrant runs hold_seats or hold_best. A NOT_LOADED event is rebuilt
// from PG once (R1) and the script retried.
func (s *Service) redisGrant(ctx context.Context, ev *EventInfo, g grant, now time.Time) (grant, error) {
	name := redisx.HoldSeats
	args := []any{g.hid.String(), g.uid.String(), now.UnixMilli(), s.ttl.Milliseconds()}
	if len(g.seats) == 0 {
		name = redisx.HoldBest
		args = append(args, g.qty)
	} else {
		for _, sn := range g.seats {
			args = append(args, sn)
		}
	}
	for attempt := 0; ; attempt++ {
		r, err := s.sc.Run(ctx, name, holdScriptKeys(ev.ID, g.hid), args...)
		if err != nil {
			return g, err
		}
		switch r.Status {
		case "OK":
			if g.fence, err = r.Int(0); err != nil {
				return g, err
			}
			exp, err := r.Int(1)
			if err != nil {
				return g, err
			}
			g.exp = time.UnixMilli(exp).UTC()
			if name == redisx.HoldBest {
				if g.seats, err = parseSeats(r.Fields[2:]); err != nil {
					return g, err
				}
			}
			return g, nil
		case "REPLAY":
			if len(r.Fields) < 3 {
				return g, fmt.Errorf("redis hold: short REPLAY reply %v", r.Fields)
			}
			if g.fence, err = r.Int(0); err != nil {
				return g, err
			}
			exp, err := r.Int(1)
			if err != nil {
				return g, err
			}
			g.exp = time.UnixMilli(exp).UTC()
			if g.seats, err = parseSeats(strings.Split(r.Fields[2], ",")); err != nil {
				return g, err
			}
			return g, nil
		case "NOT_LOADED":
			if attempt > 0 {
				return g, ErrNotLoaded
			}
			if err := s.EnsureLoaded(ctx, ev.ID); err != nil {
				return g, err
			}
		case "SOLD_OUT":
			return g, ErrSoldOut
		case "HOLD_LIMIT":
			return g, ErrHoldLimit
		case "TAKEN":
			taken, err := parseSeats(r.Fields)
			if err != nil {
				return g, err
			}
			return g, &SeatsTakenError{Taken: taken}
		case "INSUFFICIENT":
			n, err := r.Int(0)
			if err != nil {
				return g, err
			}
			return g, &InsufficientError{Available: n}
		default:
			return g, fmt.Errorf("redis hold: unexpected status %q", r.Status)
		}
	}
}

func parseSeats(fields []string) ([]int32, error) {
	out := make([]int32, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.ParseInt(f, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse seat %q: %w", f, err)
		}
		out = append(out, int32(n))
	}
	return out, nil
}

// commit is the authoritative PG transaction: insert the hold, then move
// every seat to HELD with a conditional update whose row count must equal
// the seat count (all or nothing). Seats HELD by an expired hold are taken
// over, and that hold is marked EXPIRED and its other seats freed in the
// same transaction.
func (s *Service) commit(ctx context.Context, ev *EventInfo, g grant, now time.Time) (Hold, []takeover, error) {
	var (
		out       gen.Hold
		replay    bool
		takeovers []takeover
	)
	fn := func(tx pgx.Tx) error {
		out, replay, takeovers = gen.Hold{}, false, nil
		q := gen.New(tx)
		existing, err := q.GetHold(ctx, g.hid)
		if err == nil {
			out, replay = existing, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get hold: %w", err)
		}
		if err := checkQuota(ctx, q, ev, g.uid, g.qty); err != nil {
			return err
		}
		seats, fence := g.seats, g.fence
		if s.pgOnly {
			if fence, err = q.NextHoldFence(ctx); err != nil {
				return fmt.Errorf("next fence: %w", err)
			}
			if len(seats) == 0 {
				//nolint:gosec // G115: qty <= MAX_SEATS_PER_ORDER
				if seats, err = q.PickFreeSeats(ctx, gen.PickFreeSeatsParams{EventID: ev.ID, Now: now, Qty: int32(g.qty)}); err != nil {
					return fmt.Errorf("pick seats: %w", err)
				}
				if len(seats) < g.qty {
					free, err := q.CountFreeSeats(ctx, gen.CountFreeSeatsParams{EventID: ev.ID, Now: now})
					if err != nil {
						return fmt.Errorf("count free seats: %w", err)
					}
					return &InsufficientError{Available: int64(min(free, int32(len(seats))))} //nolint:gosec // G115: <= qty
				}
			}
		}
		out, err = q.InsertHold(ctx, gen.InsertHoldParams{
			ID: g.hid, EventID: ev.ID, UserID: g.uid, SeatNos: seats, Fence: fence, ExpiresAt: g.exp,
		})
		switch {
		case db.IsUniqueViolation(err, "holds_one_active_per_user"):
			return ErrHoldLimit
		case db.IsUniqueViolation(err, "holds_pkey"):
			return errConcurrentReplay
		case err != nil:
			return fmt.Errorf("insert hold: %w", err)
		}
		rows, err := q.TakeSeats(ctx, gen.TakeSeatsParams{HoldID: g.hid, HeldUntil: g.exp, EventID: ev.ID, Now: now, SeatNos: seats})
		if err != nil {
			return fmt.Errorf("take seats: %w", err)
		}
		if len(rows) != len(seats) {
			got := make(map[int32]bool, len(rows))
			for _, r := range rows {
				got[r.SeatNo] = true
			}
			taken := make([]int32, 0, len(seats)-len(rows))
			for _, sn := range seats {
				if !got[sn] {
					taken = append(taken, sn)
				}
			}
			return &SeatsTakenError{Taken: taken}
		}
		var prev []uuid.UUID
		for _, r := range rows {
			if r.PrevHoldID != nil && !slices.Contains(prev, *r.PrevHoldID) {
				prev = append(prev, *r.PrevHoldID)
			}
		}
		slices.SortFunc(prev, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		for _, p := range prev {
			old, err := q.GetHoldForUpdate(ctx, p)
			if err != nil {
				return fmt.Errorf("lock taken-over hold: %w", err)
			}
			n, err := q.ExpireHold(ctx, gen.ExpireHoldParams{ID: p, Now: now})
			if err != nil {
				return fmt.Errorf("expire taken-over hold: %w", err)
			}
			if n == 1 {
				if _, err := q.ReleaseSeats(ctx, gen.ReleaseSeatsParams{EventID: ev.ID, HoldID: p}); err != nil {
					return fmt.Errorf("free taken-over hold: %w", err)
				}
				if s.reclaim != nil {
					if err := s.reclaim(ctx, tx, p); err != nil {
						return fmt.Errorf("reclaim hook: %w", err)
					}
				}
				takeovers = append(takeovers, takeover{id: p, seats: old.SeatNos})
			}
		}
		return nil
	}
	err := db.WithTx(ctx, s.pg, fn)
	if errors.Is(err, errConcurrentReplay) {
		err = db.WithTx(ctx, s.pg, fn) // the other insert committed: this run replays it
	}
	if err != nil {
		return Hold{}, nil, err
	}
	h := holdFromRow(out)
	if replay {
		h.Replayed = true
		if !h.Status.Live() || !h.ExpiresAt.After(now) {
			return Hold{}, nil, fmt.Errorf("%w: hold %s is %s", ErrHoldExpired, h.ID, h.Status)
		}
	}
	return h, takeovers, nil
}

// compensate undoes a Redis grant that PG refused and repairs the seats PG
// says are not free (ARCHITECTURE §4.2). Best effort: a failure is logged
// and the sweeper releases the grant at its expiry anyway.
func (s *Service) compensate(ctx context.Context, ev *EventInfo, g grant, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
	defer cancel()
	if s.metrics != nil {
		s.metrics.compensations.Inc()
	}
	s.log.WarnContext(ctx, "hold compensation: postgres refused a redis grant",
		slog.String("event_id", ev.ID.String()), slog.String("hold_id", g.hid.String()), slog.String("cause", cause.Error()))
	if err := s.releaseRedis(ctx, ev.ID, g.hid, g.uid, keepAdmission); err != nil {
		s.log.ErrorContext(ctx, "compensation release", slog.String("error", err.Error()))
		return
	}
	if err := s.repairSeats(ctx, ev, g.seats); err != nil {
		s.log.ErrorContext(ctx, "compensation repair", slog.String("error", err.Error()))
	}
}

// repairSeats copies PG's view of seats into Redis: HELD (unexpired) and
// SOLD seats leave avail; free seats are left alone (release already
// returned them).
func (s *Service) repairSeats(ctx context.Context, ev *EventInfo, seats []int32) error {
	rows, err := s.q.GetSeatsByNos(ctx, gen.GetSeatsByNosParams{EventID: ev.ID, SeatNos: seats})
	if err != nil {
		return fmt.Errorf("read seats: %w", err)
	}
	now := s.now()
	args := []any{keys.Prefix(ev.ID), 0}
	for _, r := range rows {
		switch {
		case r.State == gen.SeatStateSOLD:
			args = append(args, r.SeatNo, "S")
		case r.State == gen.SeatStateHELD && r.HeldUntil != nil && r.HeldUntil.After(now):
			args = append(args, r.SeatNo, "H")
		}
	}
	if len(args) == 2 {
		return nil
	}
	_, err = s.sc.Run(ctx, redisx.InventoryRepair, repairKeys(ev.ID), args...)
	return err
}

// repairTakeovers drops the expired holds' Redis entries (if any) and makes
// their other, now free, seats available again.
func (s *Service) repairTakeovers(ctx context.Context, ev *EventInfo, h Hold, tos []takeover) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
	defer cancel()
	args := []any{keys.Prefix(ev.ID), len(tos)}
	for _, t := range tos {
		args = append(args, t.id.String())
	}
	for _, t := range tos {
		for _, sn := range t.seats {
			if !slices.Contains(h.SeatNos, sn) {
				args = append(args, sn, "A")
			}
		}
	}
	if _, err := s.sc.Run(ctx, redisx.InventoryRepair, repairKeys(ev.ID), args...); err != nil {
		s.log.ErrorContext(ctx, "takeover repair", slog.String("error", err.Error()))
	}
}

// Release releases the user's ACTIVE hold (DELETE /holds/{id}). Releasing a
// hold that is already RELEASED or EXPIRED is a no-op; a hold with an order
// (PAYMENT_PENDING) or a sold one gives ErrHoldState.
func (s *Service) Release(ctx context.Context, holdID, userID uuid.UUID) (Hold, error) {
	var row gen.Hold
	err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		q := gen.New(tx)
		var err error
		row, err = q.GetHoldForUpdate(ctx, holdID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.UserID != userID) {
			return ErrHoldNotFound
		}
		if err != nil {
			return fmt.Errorf("lock hold: %w", err)
		}
		switch HoldStatus(row.Status) {
		case HoldActive:
			if n, err := q.ReleaseHold(ctx, holdID); err != nil || n != 1 {
				return fmt.Errorf("release hold (rows %d): %w", n, err)
			}
			if _, err := q.ReleaseSeats(ctx, gen.ReleaseSeatsParams{EventID: row.EventID, HoldID: holdID}); err != nil {
				return fmt.Errorf("release seats: %w", err)
			}
			row.Status = gen.HoldStatusRELEASED
			return nil
		case HoldReleased, HoldExpired:
			return nil
		default:
			return fmt.Errorf("%w: hold is %s", ErrHoldState, row.Status)
		}
	})
	if err != nil {
		return Hold{}, err
	}
	if !s.pgOnly {
		if err := s.releaseRedis(ctx, row.EventID, holdID, userID, freeAdmission); err != nil {
			// PG already released; the sweeper's Redis pass finishes the job.
			s.log.WarnContext(ctx, "redis release after PG release", slog.String("error", err.Error()))
		}
	}
	return holdFromRow(row), nil
}

// Extend moves an ACTIVE, unexpired hold to PAYMENT_PENDING with a new
// expiry of now+ttl, in its own transaction, then mirrors it into Redis.
// Orders use ExtendInTx instead so the order insert and the extension
// commit together.
func (s *Service) Extend(ctx context.Context, holdID uuid.UUID, ttl time.Duration) (Hold, error) {
	now := s.now()
	exp := now.Add(ttl).Truncate(time.Millisecond) // ms: Redis and PG store the same instant
	var h Hold
	err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		var err error
		h, err = s.ExtendInTx(ctx, gen.New(tx), holdID, exp, now)
		return err
	})
	if err != nil {
		return Hold{}, err
	}
	s.MirrorExtend(ctx, h.EventID, holdID, exp)
	return h, nil
}

// ExtendInTx is Extend's PG half inside the caller's transaction: lock the
// hold, ACTIVE -> PAYMENT_PENDING with expiry exp (only while unexpired at
// now), and re-date every seat. Every seat must still be HELD by this hold,
// or ErrHoldExpired is returned and the caller must roll back.
func (s *Service) ExtendInTx(ctx context.Context, q *gen.Queries, holdID uuid.UUID, exp, now time.Time) (Hold, error) {
	row, err := q.GetHoldForUpdate(ctx, holdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, ErrHoldNotFound
	} else if err != nil {
		return Hold{}, fmt.Errorf("lock hold: %w", err)
	}
	n, err := q.MarkHoldPaymentPending(ctx, gen.MarkHoldPaymentPendingParams{ExpiresAt: exp, ID: holdID, Now: now})
	if err != nil {
		return Hold{}, fmt.Errorf("mark payment pending: %w", err)
	}
	if n != 1 {
		if HoldStatus(row.Status) == HoldActive || HoldStatus(row.Status) == HoldExpired {
			return Hold{}, fmt.Errorf("%w: hold %s", ErrHoldExpired, holdID)
		}
		return Hold{}, fmt.Errorf("%w: hold is %s", ErrHoldState, row.Status)
	}
	m, err := q.ExtendSeatHold(ctx, gen.ExtendSeatHoldParams{HeldUntil: exp, EventID: row.EventID, HoldID: holdID})
	if err != nil {
		return Hold{}, fmt.Errorf("extend seats: %w", err)
	}
	if m != int64(len(row.SeatNos)) {
		return Hold{}, fmt.Errorf("%w: %d of %d seats still held", ErrHoldExpired, m, len(row.SeatNos))
	}
	row.Status, row.ExpiresAt = gen.HoldStatusPAYMENTPENDING, exp
	return holdFromRow(row), nil
}

// MirrorExtend copies a committed extension into Redis (hold_extend). Best
// effort: the sweeper's Redis pass re-extends from PG if this fails.
func (s *Service) MirrorExtend(ctx context.Context, eventID, holdID uuid.UUID, exp time.Time) {
	if s.pgOnly {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
	defer cancel()
	if _, err := s.sc.Run(ctx, redisx.HoldExtend, extendKeys(eventID, holdID), holdID.String(), exp.UnixMilli()); err != nil {
		s.log.WarnContext(ctx, "redis extend after PG extend", slog.String("error", err.Error()))
	}
}

// MirrorRelease copies a committed release (order failed or expired) into
// Redis (hold_release) and releases userID's queue admission in the same
// script (ADR 0010 addendum). Idempotent; best effort: the sweeper and the
// reconciler repair drift, and an admission left behind expires with
// ADMISSION_TTL.
func (s *Service) MirrorRelease(ctx context.Context, eventID, holdID, userID uuid.UUID) {
	if s.pgOnly {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
	defer cancel()
	if err := s.releaseRedis(ctx, eventID, holdID, userID, freeAdmission); err != nil {
		s.log.WarnContext(ctx, "redis release after PG release", slog.String("error", err.Error()))
	}
}

// Consume mirrors a committed confirm into Redis: the hold's seats move to
// sold and userID's queue admission is released in the same script (ADR 0010
// addendum). The PG side (SellSeats + ConsumeHold) belongs to ConfirmOrder's
// transaction in P3; call this after it commits. Idempotent.
func (s *Service) Consume(ctx context.Context, eventID, holdID, userID uuid.UUID) error {
	if s.pgOnly {
		return nil
	}
	return s.consumeRedis(ctx, eventID, holdID, userID)
}
