package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// Check names (LOADTEST §6, in order).
const (
	CheckOversold          = "oversold"
	CheckDuplicateCharges  = "duplicate_charges"
	CheckOrphanCharges     = "orphan_charges"
	CheckQuotaViolations   = "quota_violations"
	CheckStuckHolds        = "stuck_holds"
	CheckInventoryDrift    = "inventory_drift"
	CheckTicketOrder       = "ticket_order_consistency"
	CheckAdmittedAfterSold = "admitted_after_soldout"
	CheckUnresolvedUsers   = "unresolved_users"
)

func counted(name string, n int64, detail map[string]any, examples []string) Check {
	return Check{Name: name, Count: &n, Pass: n == 0, Detail: detail, Examples: examples}
}

func notApplicable(name, why string) Check { return Check{Name: name, Pass: true, Note: why} }

// The SQL in this file is hand-written on purpose (package doc).
func (a *auditor) checks(ctx context.Context, rep *Report) error {
	naive := a.o.Mode == ModeNaive
	steps := []func(context.Context, bool) (Check, error){
		a.oversold, a.money(CheckDuplicateCharges), a.money(CheckOrphanCharges), a.quota,
		a.stuckHolds, a.drift, a.ticketOrder,
	}
	for _, step := range steps {
		c, err := step(ctx, naive)
		if err != nil {
			return err
		}
		rep.Checks = append(rep.Checks, c)
	}
	rep.Checks = append(rep.Checks, a.swarmChecks()...)
	return nil
}

// sales is the table of issued seats: tickets, or naive_sales in naive mode.
func sales(naive bool) string {
	if naive {
		return "naive_sales"
	}
	return "tickets"
}

// owner is the column that tells two sales of a seat apart.
func owner(naive bool) string {
	if naive {
		return "order_ref"
	}
	return "order_id"
}

// oversold (SPEC §6): seats with more than one issued ticket, plus
// max(0, tickets − capacity). In naive mode a "ticket" is a distinct
// (seat, order) pair in naive_sales, so one order's repeated insert (its
// own retry) is not counted as a second buyer.
func (a *auditor) oversold(ctx context.Context, naive bool) (Check, error) {
	q := fmt.Sprintf(`SELECT seat_no, count(DISTINCT %[2]s) FROM %[1]s WHERE event_id = $1
		GROUP BY seat_no HAVING count(DISTINCT %[2]s) > 1 ORDER BY seat_no`, sales(naive), owner(naive))
	rows, err := a.pg.Query(ctx, q, a.event.id)
	if err != nil {
		return Check{}, fmt.Errorf("audit: oversold: %w", err)
	}
	var (
		multi    int64
		examples []string
	)
	for rows.Next() {
		var seat, n int64
		if err := rows.Scan(&seat, &n); err != nil {
			return Check{}, fmt.Errorf("audit: oversold: %w", err)
		}
		multi++
		if len(examples) < maxExamples {
			examples = append(examples, fmt.Sprintf("seat %d sold to %d orders", seat, n))
		}
	}
	if err := rows.Err(); err != nil {
		return Check{}, fmt.Errorf("audit: oversold: %w", err)
	}
	var issued int64
	q = fmt.Sprintf(`SELECT count(*) FROM (SELECT DISTINCT seat_no, %[2]s FROM %[1]s WHERE event_id = $1) s`, sales(naive), owner(naive))
	if err := a.pg.QueryRow(ctx, q, a.event.id).Scan(&issued); err != nil {
		return Check{}, fmt.Errorf("audit: issued: %w", err)
	}
	excess := max(0, issued-a.event.capacity)
	return counted(CheckOversold, multi+excess, map[string]any{
		"source": sales(naive), "seats_sold_more_than_once": multi, "issued": issued, "capacity": a.event.capacity,
		"excess_over_capacity": excess,
	}, examples), nil
}

// money returns the duplicate- or orphan-charge check, both from MockPay's
// ledger summary for the event (SPEC §6).
func (a *auditor) money(name string) func(context.Context, bool) (Check, error) {
	return func(ctx context.Context, naive bool) (Check, error) {
		sum, err := a.psp.LedgerSummary(ctx, a.event.id.String())
		if err != nil {
			return Check{}, fmt.Errorf("audit: ledger summary: %w", err)
		}
		if name == CheckDuplicateCharges {
			var refs, extra int64
			var examples []string
			for _, r := range sum.Refs {
				if net := r.Captured - r.Refunded; net > 1 {
					refs++
					extra += net - 1
					if len(examples) < maxExamples {
						examples = append(examples, fmt.Sprintf("%s: %d captured, %d refunded", r.MerchantRef, r.Captured, r.Refunded))
					}
				}
			}
			return counted(name, refs, map[string]any{"refs_in_ledger": len(sum.Refs), "extra_charges": extra,
				"definition": "merchant_refs (orders) with more than 1 captured intent net of refunds"}, examples), nil
		}
		// Orphans: captured-not-refunded money whose order is not CONFIRMED.
		refs := make([]string, 0, len(sum.Refs))
		for _, r := range sum.Refs {
			if r.Captured-r.Refunded > 0 {
				refs = append(refs, r.MerchantRef)
			}
		}
		confirmed, err := a.confirmed(ctx, naive, refs)
		if err != nil {
			return Check{}, err
		}
		var n int64
		var examples []string
		for _, r := range sum.Refs {
			net := r.Captured - r.Refunded
			if net > 0 && !confirmed[r.MerchantRef] {
				n += net
				if len(examples) < maxExamples {
					examples = append(examples, fmt.Sprintf("%s: %d captured, order not CONFIRMED", r.MerchantRef, net))
				}
			}
		}
		return counted(name, n, map[string]any{"definition": "captured-not-refunded intents whose order is not CONFIRMED"}, examples), nil
	}
}

// confirmed reports which of refs are CONFIRMED orders.
func (a *auditor) confirmed(ctx context.Context, naive bool, refs []string) (map[string]bool, error) {
	q := `SELECT id::text FROM orders WHERE id::text = ANY($1) AND status = 'CONFIRMED'`
	if naive {
		q = `SELECT order_ref::text FROM naive_orders WHERE order_ref::text = ANY($1) AND status = 'CONFIRMED'`
	}
	rows, err := a.pg.Query(ctx, q, refs)
	if err != nil {
		return nil, fmt.Errorf("audit: confirmed orders: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("audit: confirmed orders: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// quota (I4): tickets per (event, user) above max_per_user.
func (a *auditor) quota(ctx context.Context, naive bool) (Check, error) {
	q := fmt.Sprintf(`SELECT user_id::text, count(*) FROM (SELECT DISTINCT seat_no, %[2]s, user_id FROM %[1]s WHERE event_id = $1) s
		GROUP BY user_id HAVING count(*) > $2 ORDER BY count(*) DESC`, sales(naive), owner(naive))
	rows, err := a.pg.Query(ctx, q, a.event.id, a.event.maxPerUser)
	if err != nil {
		return Check{}, fmt.Errorf("audit: quota: %w", err)
	}
	var (
		n        int64
		examples []string
	)
	for rows.Next() {
		var uid string
		var c int64
		if err := rows.Scan(&uid, &c); err != nil {
			return Check{}, fmt.Errorf("audit: quota: %w", err)
		}
		n++
		if len(examples) < maxExamples {
			examples = append(examples, fmt.Sprintf("user %s: %d tickets", uid, c))
		}
	}
	if err := rows.Err(); err != nil {
		return Check{}, fmt.Errorf("audit: quota: %w", err)
	}
	return counted(CheckQuotaViolations, n, map[string]any{"max_per_user": a.event.maxPerUser}, examples), nil
}

// stuckHolds (I5): live holds more than 2 sweeper intervals past expiry.
func (a *auditor) stuckHolds(ctx context.Context, naive bool) (Check, error) {
	if naive {
		return notApplicable(CheckStuckHolds, "naive mode has no holds"), nil
	}
	grace := 2 * a.o.SweeperInterval
	rows, err := a.pg.Query(ctx, `SELECT id::text, status::text, expires_at FROM holds
		WHERE event_id = $1 AND status IN ('ACTIVE','PAYMENT_PENDING') AND expires_at < now() - make_interval(secs => $2)
		ORDER BY expires_at`, a.event.id, grace.Seconds())
	if err != nil {
		return Check{}, fmt.Errorf("audit: stuck holds: %w", err)
	}
	var (
		n        int64
		examples []string
	)
	for rows.Next() {
		var id, st string
		var exp time.Time
		if err := rows.Scan(&id, &st, &exp); err != nil {
			return Check{}, fmt.Errorf("audit: stuck holds: %w", err)
		}
		n++
		if len(examples) < maxExamples {
			examples = append(examples, fmt.Sprintf("hold %s %s, expired %s", id, st, exp.UTC().Format(time.RFC3339)))
		}
	}
	if err := rows.Err(); err != nil {
		return Check{}, fmt.Errorf("audit: stuck holds: %w", err)
	}
	return counted(CheckStuckHolds, n, map[string]any{"grace_s": grace.Seconds()}, examples), nil
}

// drift: Redis seat sets vs Postgres seat states, seat by seat. PG SOLD
// must be in sold and not avail; AVAILABLE in avail and not sold; HELD in
// neither (a live hold has taken it out of avail).
func (a *auditor) drift(ctx context.Context, naive bool) (Check, error) {
	if naive {
		return notApplicable(CheckInventoryDrift, "naive mode keeps no Redis inventory"), nil
	}
	loaded, err := a.rdb.HGet(ctx, keys.Meta(a.event.id), "loaded").Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return Check{}, fmt.Errorf("audit: redis meta: %w", err)
	}
	if loaded != "1" {
		return notApplicable(CheckInventoryDrift, "inventory not loaded in Redis (HOLD_PATH=pg_only or never loaded)"), nil
	}
	avail, err := a.seatSet(ctx, keys.Avail(a.event.id))
	if err != nil {
		return Check{}, err
	}
	sold, err := a.seatSet(ctx, keys.Sold(a.event.id))
	if err != nil {
		return Check{}, err
	}
	rows, err := a.pg.Query(ctx, `SELECT seat_no, state::text FROM seats WHERE event_id = $1 ORDER BY seat_no`, a.event.id)
	if err != nil {
		return Check{}, fmt.Errorf("audit: seats: %w", err)
	}
	var (
		n        int64
		examples []string
	)
	for rows.Next() {
		var seat int64
		var st string
		if err := rows.Scan(&seat, &st); err != nil {
			return Check{}, fmt.Errorf("audit: seats: %w", err)
		}
		inA, inS := avail[seat], sold[seat]
		ok := (st == "SOLD" && inS && !inA) || (st == "AVAILABLE" && inA && !inS) || (st == "HELD" && !inA && !inS)
		if !ok {
			n++
			if len(examples) < maxExamples {
				examples = append(examples, fmt.Sprintf("seat %d: PG %s, Redis avail=%t sold=%t", seat, st, inA, inS))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Check{}, fmt.Errorf("audit: seats: %w", err)
	}
	return counted(CheckInventoryDrift, n, nil, examples), nil
}

func (a *auditor) seatSet(ctx context.Context, key string) (map[int64]bool, error) {
	ms, err := a.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("audit: redis %s: %w", key, err)
	}
	out := make(map[int64]bool, len(ms))
	for _, m := range ms {
		n, err := strconv.ParseInt(m, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("audit: redis %s member %q: %w", key, m, err)
		}
		out[n] = true
	}
	return out, nil
}

// ticketOrder: every ticket's order is CONFIRMED with a CAPTURED or
// AUTHORIZED payment. Naive mode: every naive_sales row's order is
// CONFIRMED (no sale left behind by a failed or abandoned payment).
func (a *auditor) ticketOrder(ctx context.Context, naive bool) (Check, error) {
	q := `SELECT t.seat_no, t.order_id::text, o.status::text FROM tickets t JOIN orders o ON o.id = t.order_id
		WHERE t.event_id = $1 AND (o.status <> 'CONFIRMED' OR NOT EXISTS (
			SELECT 1 FROM payments p WHERE p.order_id = o.id AND p.status IN ('AUTHORIZED','CAPTURED')))
		ORDER BY t.seat_no`
	if naive {
		q = `SELECT s.seat_no, s.order_ref::text, coalesce(o.status, 'MISSING') FROM naive_sales s
			LEFT JOIN naive_orders o ON o.order_ref = s.order_ref
			WHERE s.event_id = $1 AND coalesce(o.status, 'MISSING') <> 'CONFIRMED' ORDER BY s.seat_no`
	}
	rows, err := a.pg.Query(ctx, q, a.event.id)
	if err != nil {
		return Check{}, fmt.Errorf("audit: ticket/order: %w", err)
	}
	var (
		n        int64
		examples []string
	)
	for rows.Next() {
		var seat int64
		var oid, st string
		if err := rows.Scan(&seat, &oid, &st); err != nil {
			return Check{}, fmt.Errorf("audit: ticket/order: %w", err)
		}
		n++
		if len(examples) < maxExamples {
			examples = append(examples, fmt.Sprintf("seat %d: order %s is %s", seat, oid, st))
		}
	}
	if err := rows.Err(); err != nil {
		return Check{}, fmt.Errorf("audit: ticket/order: %w", err)
	}
	return counted(CheckTicketOrder, n, nil, examples), nil
}

// swarmRecords is the part of summary.json the auditor reads.
type swarmRecords struct {
	Outcomes map[string]int64 `json:"outcomes"`
	Records  *struct {
		AdmittedAfterSoldout *int64 `json:"admitted_after_soldout"`
		Admissions           int64  `json:"admissions"`
		Note                 string `json:"note"`
	} `json:"records"`
}

// swarmChecks reads the swarm's records from the run's summary.json.
func (a *auditor) swarmChecks() []Check {
	if a.o.Results == "" {
		why := "no --results: swarm records not available"
		return []Check{notApplicable(CheckAdmittedAfterSold, why), notApplicable(CheckUnresolvedUsers, why)}
	}
	b, err := os.ReadFile(filepath.Join(a.o.Results, "summary.json"))
	var s swarmRecords
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	if err != nil {
		n := int64(1)
		c := Check{Count: &n, Note: "summary.json unreadable: " + err.Error()}
		c1, c2 := c, c
		c1.Name, c2.Name = CheckAdmittedAfterSold, CheckUnresolvedUsers
		return []Check{c1, c2}
	}
	out := make([]Check, 0, 2)
	switch {
	case s.Records == nil:
		out = append(out, notApplicable(CheckAdmittedAfterSold, "summary.json has no swarm records"))
	case s.Records.AdmittedAfterSoldout == nil:
		out = append(out, notApplicable(CheckAdmittedAfterSold, s.Records.Note))
	default:
		c := counted(CheckAdmittedAfterSold, *s.Records.AdmittedAfterSoldout, map[string]any{"admissions": s.Records.Admissions}, nil)
		c.Note = s.Records.Note
		out = append(out, c)
	}
	return append(out, counted(CheckUnresolvedUsers, s.Outcomes["UNRESOLVED"], nil, nil))
}

// Render is the auditor's table for stdout and reports.
func Render(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Audit: %s (%s mode)\nSettle %.1f s (timed out: %t). Passed: **%t** (exit %d).\n\n| check | count | pass | note / examples |\n|---|---|---|---|\n",
		r.Event, r.Mode, r.Settle.Seconds, r.Settle.TimedOut, r.Passed, r.ExitCode)
	for _, c := range r.Checks {
		count := "n/a"
		if c.Count != nil {
			count = strconv.FormatInt(*c.Count, 10)
		}
		note := c.Note
		if len(c.Examples) > 0 {
			note = strings.TrimSpace(note + " " + strings.Join(c.Examples, "; "))
		}
		fmt.Fprintf(&b, "| %s | %s | %t | %s |\n", c.Name, count, c.Pass, note)
	}
	return b.String()
}
