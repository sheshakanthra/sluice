package rush

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Outcome is a user's terminal state (SPEC §6) or UNRESOLVED.
type Outcome int

// Terminal outcomes.
const (
	OutTicket Outcome = iota
	OutSoldOut
	OutNoSeat
	OutPaymentFailed
	OutAbandoned
	OutUnresolved
	numOutcomes
)

var outcomeNames = [numOutcomes]string{"TICKET", "SOLD_OUT", "NO_SEAT", "PAYMENT_FAILED", "ABANDONED", "UNRESOLVED"}

func (o Outcome) String() string { return outcomeNames[o] }

// Retry budgets.
const (
	maxSendAttempts = 30 // same request, same IK
	maxRejoins      = 2
	maxHoldTries    = 4 // first try + 3 retries (pick → best → fewer)
	maxPayAttempts  = 3
	confirmBudget   = 3 * time.Minute
	minPoll         = 250 * time.Millisecond
	maxPoll         = 60 * time.Second // the server's own cap (API.md §3.3)
	// pollJitter: the swarm waits retry_after x U(1, 1 + pollJitter), never
	// sooner than the server asked; the server bounds retry_after for this
	// (API.md §3.3, ADR 0017).
	pollJitter = 0.1
)

// user is one simulated person.
type user struct {
	s       *swarm
	p       plan
	c       *client
	session string
	queue   string // queue token
	adm     string // admission token
	joined  bool
	rejoins int  // queue rejoins used (OFFER_EXPIRED or ADMISSION_EXPIRED), ≤ maxRejoins
	lapsed  bool // the last shop ended on 403 ADMISSION_EXPIRED: rejoin, don't end
}

func newIK() string { return uuid.NewString() }

// run plays the whole journey and returns the terminal outcome.
func (u *user) run(ctx context.Context) Outcome {
	if !u.sleepUntil(ctx, u.s.t0.Add(u.p.arriveAt)) {
		return OutUnresolved
	}
	if !u.createSession(ctx) {
		return OutUnresolved
	}
	if u.s.o.Scenario.Direct {
		u.markJoined()
		// No waiting room: go straight for seats once the sale is open.
		if !u.sleepUntil(ctx, latest(u.s.t0, time.Now())) {
			return OutUnresolved
		}
		return u.end(ctx, u.shop(ctx, u.session)) // end: leave is a no-op without a queue
	}
	out, admitted := u.queueUp(ctx, false)
	for admitted {
		u.lapsed = false
		if out = u.shop(ctx, u.adm); !u.lapsed {
			break
		}
		// 403 ADMISSION_EXPIRED: the server ended the admission lease; the
		// client rejoins (API.md §1.3), within the same two-rejoin budget.
		if u.rejoins >= maxRejoins {
			out = OutNoSeat
			break
		}
		u.rejoins++
		u.adm = ""
		out, admitted = u.queueUp(ctx, true)
	}
	return u.end(ctx, out)
}

// end applies LOADTEST §3.2's exit rule in one place: a user who ends as
// NO_SEAT or ABANDONED leaves the queue (DELETE /queue), so the server can
// hand the admission slot to someone else at once. Every other outcome
// returns unchanged.
func (u *user) end(ctx context.Context, out Outcome) Outcome {
	if out == OutNoSeat || out == OutAbandoned {
		u.leave(ctx)
	}
	return out
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// sleepUntil waits for a scheduled moment and records how late it woke (generator lag).
func (u *user) sleepUntil(ctx context.Context, at time.Time) bool {
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
	u.s.rec.RecordLag(time.Since(at))
	return ctx.Err() == nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// send retries one request (same IK) on timeouts, transport errors, 5xx, 429
// and REQUEST_IN_PROGRESS. ok=false means the run ended or the budget ran out.
func (u *user) send(ctx context.Context, q req) (result, bool) {
	return sendWith(ctx, u.c, q)
}

func sendWith(ctx context.Context, c *client, q req) (result, bool) {
	var r result
	for attempt := range maxSendAttempts {
		r = c.do(ctx, q)
		if ctx.Err() != nil {
			return r, false
		}
		wait, again := retryable(&r, attempt, q.floor, jitterU())
		if !again {
			return r, true
		}
		if !sleepCtx(ctx, wait) {
			return r, false
		}
		q.intended = time.Now()
	}
	return r, false
}

func (u *user) createSession(ctx context.Context) bool {
	r, ok := u.send(ctx, req{rt: rSession, method: http.MethodPost, path: "/sessions", timeout: callTimeout, intended: time.Now()})
	if !ok || r.status != http.StatusCreated {
		u.s.anomaly("session", &r)
		return false
	}
	var b struct {
		Token string `json:"session_token"`
	}
	if err := json.Unmarshal(r.body, &b); err != nil || b.Token == "" {
		u.s.anomaly("session", &r)
		return false
	}
	u.session = b.Token
	return true
}

func (u *user) markJoined() {
	if !u.joined {
		u.joined = true
		u.s.enter()
	}
}

// queueView is the subset of the join/status body the swarm uses.
type queueView struct {
	State          string    `json:"state"`
	QueueToken     string    `json:"queue_token"`
	AdmissionToken string    `json:"admission_token"`
	RetryAfterMS   int64     `json:"retry_after_ms"`
	sent           time.Time // when the request that returned this view was sent
}

func (u *user) join(ctx context.Context, rejoin bool) (queueView, bool) {
	path := u.s.eventPath + "/queue"
	if rejoin {
		path += "?rejoin=true"
	}
	sent := time.Now()
	r, ok := u.send(ctx, req{rt: rJoin, method: http.MethodPost, path: path, token: u.session, timeout: callTimeout, intended: sent})
	v := queueView{sent: sent}
	if !ok || r.status != http.StatusOK || json.Unmarshal(r.body, &v) != nil {
		u.s.anomaly("join", &r)
		return v, false
	}
	u.markJoined()
	if v.QueueToken != "" {
		u.queue = v.QueueToken
	}
	return v, true
}

// queueUp joins (or rejoins) and polls until admitted or a terminal answer.
func (u *user) queueUp(ctx context.Context, rejoin bool) (Outcome, bool) {
	v, ok := u.join(ctx, rejoin)
	if !ok {
		return u.unresolvedOr(ctx, OutUnresolved), false
	}
	u.s.waiting.Add(1)
	defer u.s.waiting.Add(-1)
	next := time.Now()
	for {
		switch v.State {
		case "ADMITTED":
			if v.AdmissionToken != "" {
				u.s.recordAdmit(v.sent)
				u.adm = v.AdmissionToken
				return 0, true
			}
		case "SOLD_OUT", "CLOSED":
			return OutSoldOut, false
		case "OFFER_EXPIRED":
			if u.rejoins >= maxRejoins {
				return OutNoSeat, false
			}
			u.rejoins++
			if v, ok = u.join(ctx, true); !ok {
				return u.unresolvedOr(ctx, OutNoSeat), false
			}
			next = time.Now()
			continue
		}
		wait := min(max(time.Duration(v.RetryAfterMS)*time.Millisecond, minPoll), maxPoll)
		next = next.Add(stretch(u.p.rng, wait, pollJitter))
		if !u.sleepUntil(ctx, next) {
			return OutUnresolved, false
		}
		var st queueView
		if st, ok = u.status(ctx, next, wait); !ok {
			return u.unresolvedOr(ctx, OutUnresolved), false
		}
		v = st
		if v.QueueToken != "" {
			u.queue = v.QueueToken
		}
		if time.Now().After(next) {
			next = time.Now() // don't burst to catch up after a slow call
		}
	}
}

// status polls once (2 s timeout). A failed attempt is retried with full
// jitter backoff, never sooner than floor, the server's last retry_after.
func (u *user) status(ctx context.Context, intended time.Time, floor time.Duration) (queueView, bool) {
	sent := time.Now()
	r, ok := u.send(ctx, req{rt: rStatus, method: http.MethodGet, path: u.s.eventPath + "/queue/status", token: u.queue,
		timeout: statusTimeout, intended: intended, floor: floor})
	v := queueView{sent: sent}
	if !ok || r.status != http.StatusOK || json.Unmarshal(r.body, &v) != nil {
		u.s.anomaly("status", &r)
		return v, false
	}
	return v, true
}

func (u *user) unresolvedOr(ctx context.Context, o Outcome) Outcome {
	if ctx.Err() != nil {
		return OutUnresolved
	}
	return o
}

// leave frees the queue slot (NO_SEAT / ABANDONED). Best effort.
func (u *user) leave(ctx context.Context) {
	if u.s.o.Scenario.Direct {
		return
	}
	tok := u.adm
	if tok == "" {
		tok = u.queue
	}
	r, _ := u.send(ctx, req{rt: rLeave, method: http.MethodDelete, path: u.s.eventPath + "/queue", token: tok, timeout: callTimeout, intended: time.Now()})
	if r.status >= 400 && r.status != http.StatusNotFound {
		u.s.anomaly("leave", &r)
	}
}

// release gives a hold back (DELETE /holds/{id}). Best effort.
func (u *user) release(ctx context.Context, tok, holdID string) {
	r, _ := u.send(ctx, req{rt: rRelease, method: http.MethodDelete, path: "/holds/" + holdID, token: tok, timeout: callTimeout, intended: time.Now()})
	if r.status >= 400 && r.status != http.StatusNotFound && r.code() != "HOLD_EXPIRED" {
		u.s.anomaly("release", &r)
	}
}

// shop: think → hold (with retries) → think → order → pay.
func (u *user) shop(ctx context.Context, tok string) Outcome {
	u.s.shopping.Add(1)
	defer u.s.shopping.Add(-1)
	at := time.Now().Add(u.p.thinkAdmit)
	if !u.sleepUntil(ctx, at) {
		return OutUnresolved
	}
	holdID, out, ok := u.hold(ctx, tok, at)
	if !ok {
		return out
	}
	at = time.Now().Add(u.p.thinkHold)
	if !u.sleepUntil(ctx, at) {
		return OutUnresolved
	}
	if u.p.abandon {
		return OutAbandoned // run leaves the queue; the hold is left for the TTL sweeper
	}
	r, ok := u.send(ctx, req{rt: rOrder, method: http.MethodPost, path: "/orders", token: tok, ik: newIK(),
		body: map[string]string{"hold_id": holdID}, timeout: callTimeout, intended: at})
	if !ok {
		return OutUnresolved
	}
	var ov orderView
	if r.code() == "ADMISSION_EXPIRED" {
		// Give the seats back now rather than at the hold TTL, then rejoin.
		u.release(ctx, tok, holdID)
		u.lapsed = true
		return OutNoSeat
	}
	if (r.status != http.StatusCreated && r.status != http.StatusOK) || json.Unmarshal(r.body, &ov) != nil {
		u.s.anomaly("order", &r)
		return OutNoSeat
	}
	return u.payFlow(ctx, ov.OrderID)
}

// hold tries up to maxHoldTries times: pick (or best) → best → fewer seats.
func (u *user) hold(ctx context.Context, tok string, intended time.Time) (string, Outcome, bool) {
	qty := u.p.qty
	pick := u.p.pick
	for try := range maxHoldTries {
		body := map[string]any{"quantity": qty}
		if pick {
			if seats := u.pickSeats(ctx, qty, intended); len(seats) == qty {
				body = map[string]any{"seat_nos": seats}
			}
			intended = time.Now()
		}
		r, ok := u.send(ctx, req{rt: rHold, method: http.MethodPost, path: u.s.eventPath + "/holds", token: tok, ik: newIK(),
			body: body, timeout: callTimeout, intended: intended})
		if !ok {
			return "", OutUnresolved, false
		}
		if r.status == http.StatusCreated || r.status == http.StatusOK {
			var b struct {
				HoldID string `json:"hold_id"`
			}
			if json.Unmarshal(r.body, &b) == nil && b.HoldID != "" {
				return b.HoldID, 0, true
			}
			u.s.anomaly("hold", &r)
			return "", OutNoSeat, false
		}
		p := r.problem()
		switch p.Code {
		case "SOLD_OUT":
			return "", OutSoldOut, false
		case "ADMISSION_EXPIRED":
			u.lapsed = true
			return "", OutNoSeat, false
		case "SEATS_TAKEN":
			pick = false // next: best-available, same quantity
		case "INSUFFICIENT_SEATS":
			pick = false
			if p.Available != nil && *p.Available == 0 {
				// Every seat is held, not sold (that would be SOLD_OUT):
				// fewer seats cannot help, so stop retrying. LOADTEST §3.2:
				// TAKEN/INSUFFICIENT -> NO_SEAT, and the user leaves.
				return "", OutNoSeat, false
			}
			if p.Available != nil && *p.Available < qty {
				qty = *p.Available
			} else if qty > 1 {
				qty--
			}
		default: // QUOTA_EXCEEDED, HOLD_LIMIT, NOT_ADMITTED, tokens, validation
			u.s.anomaly("hold", &r)
			return "", OutNoSeat, false
		}
		if try >= 1 && qty > 1 && p.Code == "SEATS_TAKEN" {
			qty-- // third try onwards: fewer seats
		}
		intended = time.Now()
	}
	return "", OutNoSeat, false
}

// pickSeats reads the availability bitmap and picks qty random free seats.
func (u *user) pickSeats(ctx context.Context, qty int, intended time.Time) []int {
	r, ok := u.send(ctx, req{rt: rAvail, method: http.MethodGet, path: u.s.eventPath + "/availability", timeout: callTimeout, intended: intended})
	if !ok || r.status != http.StatusOK {
		return nil
	}
	var b struct {
		Counts struct {
			Capacity int `json:"capacity"`
		} `json:"counts"`
		States string `json:"states"`
	}
	if json.Unmarshal(r.body, &b) != nil {
		return nil
	}
	bits, err := base64.StdEncoding.DecodeString(b.States)
	if err != nil {
		return nil
	}
	free := freeSeats(bits, b.Counts.Capacity)
	if len(free) < qty {
		return nil
	}
	u.p.rng.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })
	return free[:qty]
}

// freeSeats decodes API.md §3.8 packing: 2 bits per seat, MSB first, 00 = available.
func freeSeats(bits []byte, capacity int) []int {
	var free []int
	for i := 0; i < capacity && i/4 < len(bits); i++ {
		if bits[i/4]>>(6-2*(i%4))&0b11 == 0 {
			free = append(free, i)
		}
	}
	return free
}

// orderView is the subset of the order view (API.md §3.5) the swarm uses.
type orderView struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
	Payment *struct {
		Attempt int    `json:"attempt"`
		Status  string `json:"status"`
	} `json:"payment"`
}

type payState int

const (
	psTicket payState = iota
	psFailed
	psDeclined
	psPending
)

func (v *orderView) state() payState {
	switch v.Status {
	case "CONFIRMED":
		return psTicket
	case "FAILED", "EXPIRED", "REFUNDED":
		return psFailed
	}
	if v.Payment != nil && (v.Payment.Status == "FAILED" || v.Payment.Status == "VOIDED") {
		return psDeclined
	}
	return psPending
}

// payFlow: pay → CONFIRMED | decline → new attempt (70%) | pending → poll /confirm.
func (u *user) payFlow(ctx context.Context, orderID string) Outcome {
	pm := u.p.pm
	for attempt := 1; ; attempt++ {
		v, out, ok := u.pay(ctx, orderID, pm, attempt == 1)
		if !ok {
			return out
		}
		switch v.state() {
		case psTicket:
			return OutTicket
		case psFailed:
			return OutPaymentFailed
		case psPending:
			return u.pollConfirm(ctx, orderID)
		case psDeclined:
		}
		if !u.p.newAttempt || attempt >= maxPayAttempts {
			return OutPaymentFailed
		}
		r, ok := u.send(ctx, req{rt: rPayments, method: http.MethodPost, path: "/orders/" + orderID + "/payments", token: u.session,
			ik: newIK(), timeout: callTimeout, intended: time.Now()})
		if !ok {
			return OutUnresolved
		}
		if r.status != http.StatusCreated && r.status != http.StatusOK {
			if c := r.code(); c != "HOLD_EXPIRED" && c != "PAYMENT_ATTEMPTS_EXHAUSTED" {
				u.s.anomaly("payments", &r)
			}
			return OutPaymentFailed
		}
		pm = pmOK // the user fixes their card
	}
}

// pay sends /pay (plus the 5% concurrent double-submit with the same IK and
// the 1% rogue retry with a new IK) and returns the primary order view.
func (u *user) pay(ctx context.Context, orderID, pm string, first bool) (orderView, Outcome, bool) {
	q := req{rt: rPay, method: http.MethodPost, path: "/orders/" + orderID + "/pay", token: u.session, ik: newIK(),
		body: map[string]string{"payment_method": pm}, timeout: callTimeout, intended: time.Now()}
	var wg sync.WaitGroup
	if first && u.p.double {
		twin := u.s.twinClient(ctx, u.c.xff)
		wg.Go(func() { _, _ = sendWith(ctx, twin, q) })
	}
	r, ok := u.send(ctx, q)
	wg.Wait()
	if first && u.p.rogue {
		rq := q
		rq.ik, rq.intended = newIK(), time.Now()
		_, _ = u.send(ctx, rq)
	}
	var v orderView
	if !ok {
		return v, OutUnresolved, false
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &v) != nil {
		switch r.code() {
		case "HOLD_EXPIRED", "PAYMENT_ATTEMPTS_EXHAUSTED":
		case "ORDER_STATE":
			return v, u.pollConfirm(ctx, orderID), false
		default:
			u.s.anomaly("pay", &r)
		}
		return v, OutPaymentFailed, false
	}
	return v, 0, true
}

// pollConfirm calls /confirm (a new IK per poll: each poll is a new intent)
// until the order settles. Backs off to stay inside 10/min/user.
func (u *user) pollConfirm(ctx context.Context, orderID string) Outcome {
	deadline := time.Now().Add(confirmBudget)
	wait := time.Second
	next := time.Now()
	for time.Now().Before(deadline) {
		next = next.Add(jitter(u.p.rng, wait, 0.2))
		if !u.sleepUntil(ctx, next) {
			return OutUnresolved
		}
		wait = min(wait*3/2, 6*time.Second)
		r, ok := u.send(ctx, req{rt: rConfirm, method: http.MethodPost, path: "/orders/" + orderID + "/confirm", token: u.session,
			ik: newIK(), timeout: callTimeout, intended: next})
		if !ok {
			return OutUnresolved
		}
		next = latest(next, time.Now())
		var v orderView
		if r.status != http.StatusOK || json.Unmarshal(r.body, &v) != nil {
			u.s.anomaly("confirm", &r)
			continue
		}
		switch v.state() {
		case psTicket:
			return OutTicket
		case psFailed, psDeclined:
			return OutPaymentFailed // not charged
		case psPending:
		}
	}
	return OutUnresolved
}

// anomalyKey labels an unexpected answer for the report.
func anomalyKey(step string, r *result) string {
	if r.status == 0 {
		return step + ":no-response"
	}
	if c := r.code(); c != "" {
		return fmt.Sprintf("%s:%d %s", step, r.status, c)
	}
	return fmt.Sprintf("%s:%d", step, r.status)
}
