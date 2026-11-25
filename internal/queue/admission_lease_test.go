package queue_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

const idle = 60 * time.Second // ADMISSION_IDLE_TTL in rush.env

// admitted is a claimed admission: the user and their admission token.
type admitted struct {
	u      *user
	token  string
	claims auth.Claims
}

// admitFirst ticks once and claims the first n tickets (in order).
func (e *env) admitFirst(users []*user, n int) []admitted {
	e.t.Helper()
	byTicket := make(map[int64]*user, len(users))
	for _, u := range users {
		byTicket[u.ticket] = u
	}
	if res := e.tick(); res.Admitted < int64(n) {
		e.t.Fatalf("tick offered %d, want >= %d", res.Admitted, n)
	}
	e.refresh()
	out := make([]admitted, n)
	for i := range n {
		u := byTicket[int64(i+1)]
		r := e.status(u)
		if r.State != queue.StateAdmitted || r.AdmissionToken == nil {
			e.t.Fatalf("ticket %d: %s", i+1, r.State)
		}
		c, err := e.keys.Verify(*r.AdmissionToken, auth.AudAdmission)
		mustNil(e.t, err)
		out[i] = admitted{u: u, token: *r.AdmissionToken, claims: c}
	}
	return out
}

func (e *env) leaseOf(uid uuid.UUID) (time.Time, bool) {
	e.t.Helper()
	v, err := e.rdb.ZScore(e.ctx, keys.QActive(e.ev.ID), uid.String()).Result()
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(v)).UTC(), true
}

// TestIdleAdmissionExpiresAndSlotRefills (ADR 0010 addendum): 15 users are
// admitted (10 seats, target 15). Five keep making requests (every 20 s);
// ten go idle. Nobody is dropped before ADMISSION_IDLE_TTL; just after it,
// the ten idle admissions leave q:active on their own (no client call) and
// the next tick offers their ten slots. Nothing waits for ADMISSION_TTL.
func TestIdleAdmissionExpiresAndSlotRefills(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(10), idleTTL(idle))
	e.toT0(0)
	users := e.joinAll(40, 8)
	adm := e.admitFirst(users, 15) // 10 seats x 1.5
	if res := e.tick(); res.Admitted != 0 || res.Seeking != 15 {
		t.Fatalf("full tick = %+v", res)
	}
	active, idleUsers := adm[:5], adm[5:]
	for step := 1; step <= 4; step++ { // t = 20, 40, 60, 80 s
		e.clk.Advance(20 * time.Second)
		for _, a := range active {
			e.lease.Touch(e.ctx, a.claims)
		}
		res := e.tick()
		elapsed := time.Duration(step) * 20 * time.Second
		n := e.rdb.ZCard(e.ctx, keys.QActive(e.ev.ID)).Val()
		switch {
		case elapsed < idle: // t = 20, 40: everyone still admitted, nothing offered
			if n != 15 || res.Admitted != 0 {
				t.Fatalf("t=%s: %d admitted, tick %+v; nobody may expire before the idle TTL", elapsed, n, res)
			}
		case elapsed == idle: // t = 60: the idle leases end exactly now (score <= now)
			if n != 5 || res.Admitted != 10 {
				t.Fatalf("t=%s: %d admitted, tick %+v; want the 10 idle slots re-offered", elapsed, n, res)
			}
		default:
			if n != 5 {
				t.Fatalf("t=%s: %d admitted, want the 5 active users", elapsed, n)
			}
		}
	}
	for _, a := range idleUsers {
		if _, ok := e.leaseOf(a.u.id); ok {
			t.Errorf("idle ticket %d still admitted", a.u.ticket)
		}
	}
	for _, a := range active {
		l, ok := e.leaseOf(a.u.id)
		if !ok || !l.Equal(e.clk.Now().Add(idle)) {
			t.Errorf("active ticket %d: lease %s (present %v), want now + idle", a.u.ticket, l, ok)
		}
	}
	if cursor := e.get(keys.QCursor(e.ev.ID)); cursor != "25" {
		t.Errorf("cursor = %s, want 25 (15 + the 10 refilled slots)", cursor)
	}
	if got := e.metric("queue_admission_touches_total"); got != 20 {
		t.Errorf("touches = %.0f, want 20 (5 users x 4 rounds)", got)
	}
}

// TestActiveAdmissionLivesToHardCap: a user who makes a request every 50 s
// (inside the 60 s idle TTL) is never expired before ADMISSION_TTL; the
// lease stops at the cap, a re-claim reports the same cap, and the first
// tick after the cap removes the admission.
func TestActiveAdmissionLivesToHardCap(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(4), idleTTL(idle))
	e.toT0(0)
	users := e.joinAll(12, 4)
	a := e.admitFirst(users, 1)[0]
	capAt := a.claims.ExpiresAt()
	if d := capAt.Sub(e.clk.Now()); d < admissionTTL-time.Second || d > admissionTTL {
		t.Fatalf("token exp in %s, want the %s hard cap", d, admissionTTL)
	}
	if l, _ := e.leaseOf(a.u.id); !l.Equal(e.clk.Now().Add(idle)) {
		t.Fatalf("initial lease %s, want claim + idle", l)
	}
	steps := 0
	for e.clk.Now().Add(50 * time.Second).Before(capAt) {
		e.clk.Advance(50 * time.Second)
		e.lease.Touch(e.ctx, a.claims)
		e.tick()
		steps++
		l, ok := e.leaseOf(a.u.id)
		if !ok {
			t.Fatalf("active user expired at +%s, before the hard cap", time.Duration(steps)*50*time.Second)
		}
		if l.After(capAt) {
			t.Fatalf("lease %s passed the cap %s", l, capAt)
		}
	}
	if r := e.status(a.u); r.State != queue.StateAdmitted || r.AdmissionExpiresAt == nil || !r.AdmissionExpiresAt.Equal(capAt) {
		t.Fatalf("re-claim near the cap: %s exp %v, want ADMITTED with the same cap %s", r.State, r.AdmissionExpiresAt, capAt)
	}
	e.clk.Set(capAt.Add(time.Second))
	e.lease.Touch(e.ctx, a.claims) // past the cap: no renewal
	e.tick()
	if _, ok := e.leaseOf(a.u.id); ok {
		t.Fatal("admission survived its hard cap")
	}
	t.Logf("active user renewed %d times, kept until the %s cap", steps, admissionTTL)
}

// TestConcurrentTouchesNeverResurrect: 300 admitted users renew their
// leases from many goroutines while 150 of them finish checkout (hold, then
// consume or release) and two controllers tick. ZADD XX never re-creates a
// released admission, whatever the interleaving; the others end up with
// lease = now + idle.
func TestConcurrentTouchesNeverResurrect(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(200), idleTTL(idle), tickCfg(queue.TickConfig{MaxActive: 1000, Overadmit: 1.5, MinAdmit: 10,
		MaxPerTick: 1000, OfferTTL: time.Minute}))
	e.toT0(0)
	users := e.joinAll(400, 16)
	adm := e.admitFirst(users, 300)

	ctx, stop := context.WithCancel(e.ctx)
	var ctl sync.WaitGroup
	for range 2 {
		c := e.newController()
		ctl.Go(func() { _ = c.Run(ctx) })
	}
	var wg sync.WaitGroup
	errs := make(chan error, 300)
	ended := make([]atomic.Bool, len(adm)) // checkout really ended (hold, then consume or release, both OK)
	for i, a := range adm {
		wg.Go(func() { // every user renews 20 times
			for range 20 {
				e.lease.Touch(e.ctx, a.claims)
			}
		})
		if i >= 150 {
			continue
		}
		wg.Go(func() { // the first 150 also check out
			h, err := e.inv.Hold(e.ctx, e.ev.ID, a.u.id, uuid.NewString(), inventory.Request{Quantity: 1})
			if err != nil {
				errs <- fmt.Errorf("hold %d: %w", a.u.ticket, err)
				return
			}
			if i%2 == 0 {
				err = e.inv.Consume(e.ctx, e.ev.ID, h.ID, a.u.id)
			} else {
				_, err = e.inv.Release(e.ctx, h.ID, a.u.id)
			}
			if err != nil {
				errs <- fmt.Errorf("end %d: %w", a.u.ticket, err)
			} else {
				ended[i].Store(true)
			}
			for range 5 { // a late request with the old token after checkout
				e.lease.Touch(e.ctx, a.claims)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	stop()
	ctl.Wait()
	// Only checkouts that really ended must be gone (a hold error above
	// already fails the test; such a user was never released).
	resurrected, missing := 0, 0
	for i, a := range adm {
		l, ok := e.leaseOf(a.u.id)
		switch {
		case ended[i].Load() && ok:
			resurrected++
		case i >= 150 && (!ok || !l.Equal(e.clk.Now().Add(idle))):
			missing++
		}
	}
	if resurrected != 0 || missing != 0 {
		t.Fatalf("%d finished admissions re-created by a touch, %d shoppers without a renewed lease", resurrected, missing)
	}
}

// TestAdmissionRequestRenewsLease: the API path. A POST /holds with the
// admission token goes through Keyring.Require with the lease hook (as the
// API wires it) and renews the lease to now + idle.
func TestAdmissionRequestRenewsLease(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(10), idleTTL(idle))
	e.toT0(0)
	users := e.joinAll(5, 2)
	a := e.admitFirst(users, 1)[0]
	rt := httpx.NewRouter()
	inventory.RegisterRoutes(rt, inventory.HandlerDeps{Svc: e.inv, Keys: e.keys.WithAdmissionTouch(e.lease.Touch),
		Idem: httpx.NewIdempotency(e.q, auth.UserID, slog.New(slog.DiscardHandler), nil), QueueEnforced: true})

	e.clk.Advance(45 * time.Second) // 15 s of the lease left
	req := httptest.NewRequestWithContext(e.ctx, http.MethodPost, "/api/v1/events/"+e.ev.ID.String()+"/holds",
		bytes.NewBufferString(`{"quantity":1}`))
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set(httpx.IdempotencyHeader, uuid.NewString())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("hold: HTTP %d %s", rec.Code, rec.Body)
	}
	if l, ok := e.leaseOf(a.u.id); !ok || !l.Equal(e.clk.Now().Add(idle)) {
		t.Fatalf("lease %s (present %v), want renewed to now + idle", l, ok)
	}
}

// TestLapsedAdmissionCannotHold (ADR 0010 addendum part 3): POST /holds
// goes through Keyring.RequireLive with the lease check, as the API wires
// it. A user whose lease lapsed gets 403 ADMISSION_EXPIRED even though
// their token is still valid, whether the tick has pruned the lease yet or
// not, and the refusal does not revive the lease. An active user can hold.
// The lapsed user's status answers OFFER_EXPIRED and a rejoin is honoured.
// A user whose checkout ended (hold released, admission freed) cannot hold
// again on the same token.
func TestLapsedAdmissionCannotHold(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(10), idleTTL(idle))
	e.toT0(0)
	users := e.joinAll(6, 2)
	adm := e.admitFirst(users, 2)
	active, lapsed := adm[0], adm[1]
	rt := httpx.NewRouter()
	inventory.RegisterRoutes(rt, inventory.HandlerDeps{Svc: e.inv, Keys: e.keys.WithAdmissionTouch(e.lease.Touch),
		Idem: httpx.NewIdempotency(e.q, auth.UserID, slog.New(slog.DiscardHandler), nil), QueueEnforced: true})
	hold := func(a admitted) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(e.ctx, http.MethodPost, "/api/v1/events/"+e.ev.ID.String()+"/holds",
			bytes.NewBufferString(`{"quantity":1}`))
		req.Header.Set("Authorization", "Bearer "+a.token)
		req.Header.Set(httpx.IdempotencyHeader, uuid.NewString())
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec
	}
	wantExpired := func(step string, a admitted) {
		t.Helper()
		rec := hold(a)
		if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"ADMISSION_EXPIRED"`)) {
			t.Fatalf("%s: hold = HTTP %d %s, want 403 ADMISSION_EXPIRED", step, rec.Code, rec.Body)
		}
		if n := e.rdb.SIsMember(e.ctx, keys.Holding(e.ev.ID), a.u.id.String()).Val(); n {
			t.Fatalf("%s: a refused user is in holding", step)
		}
	}

	e.clk.Advance(45 * time.Second)
	e.lease.Touch(e.ctx, active.claims) // active: lease to t = 105 s
	e.clk.Advance(20 * time.Second)     // t = 65 s: the other lease ended at 60 s; no tick yet

	wantExpired("lapsed, not yet pruned", lapsed)
	if l, ok := e.leaseOf(lapsed.u.id); !ok || !l.Equal(e.ev.SaleOpensAt.Add(idle)) {
		t.Fatalf("refused request moved the lapsed lease to %s (present %v)", l, ok)
	}
	rec := hold(active)
	if rec.Code != http.StatusCreated {
		t.Fatalf("active user: hold = HTTP %d %s, want 201", rec.Code, rec.Body)
	}
	e.tick() // prunes the lapsed lease
	wantExpired("lapsed and pruned", lapsed)

	// The client may rejoin: status says OFFER_EXPIRED and the rejoin is honoured.
	if r := e.status(lapsed.u); r.State != queue.StateOfferExpired {
		t.Fatalf("lapsed user's status = %s, want OFFER_EXPIRED", r.State)
	}
	old := lapsed.u.ticket
	r, err := e.join(lapsed.u, true)
	mustNil(t, err)
	if r.TicketNo == nil || *r.TicketNo <= old {
		t.Fatalf("rejoin gave ticket %v, want a new ticket after %d", r.TicketNo, old)
	}

	// A finished checkout ends the admission: release the hold, then the
	// same (still valid) token cannot hold again.
	var b struct {
		HoldID string `json:"hold_id"`
	}
	mustNil(t, json.Unmarshal(rec.Body.Bytes(), &b))
	rel := httptest.NewRequestWithContext(e.ctx, http.MethodDelete, "/api/v1/holds/"+b.HoldID, nil)
	rel.Header.Set("Authorization", "Bearer "+active.token)
	relRec := httptest.NewRecorder()
	rt.ServeHTTP(relRec, rel)
	if relRec.Code >= 300 {
		t.Fatalf("release: HTTP %d %s", relRec.Code, relRec.Body)
	}
	if _, ok := e.leaseOf(active.u.id); ok {
		t.Fatal("released hold did not free the admission")
	}
	wantExpired("checkout ended", active)
}

// TestAvailabilityPollRenewsLease (ADR 0010 addendum part 4): the rider's
// seat-map poll (GET /availability, public) renews the idle lease when it
// carries the admission token, which the web client sends only while the
// page is visible. Three admitted users browse for 140 s without a hold:
// one polls every 20 s with the token and stays admitted with lease =
// now + idle; one polls without the token (hidden tab) and one does not
// poll at all, and both lapse at ADMISSION_IDLE_TTL. Every poll is a 200 or
// 304, whatever the token. A token for another event renews nothing, and
// the poll never renews past ADMISSION_TTL (the hard cap, checked by
// TestActiveUserKeptUntilCap through the same Touch).
func TestAvailabilityPollRenewsLease(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(10), idleTTL(idle))
	e.toT0(0)
	users := e.joinAll(6, 2)
	adm := e.admitFirst(users, 3)
	visible, hidden, gone := adm[0], adm[1], adm[2]
	rt := httpx.NewRouter()
	inventory.RegisterRoutes(rt, inventory.HandlerDeps{Svc: e.inv, Avail: e.inv.NewAvailabilityCache(0),
		Keys: e.keys.WithAdmissionTouch(e.lease.Touch),
		Idem: httpx.NewIdempotency(e.q, auth.UserID, slog.New(slog.DiscardHandler), nil), QueueEnforced: true})
	poll := func(path, token string) {
		t.Helper()
		req := httptest.NewRequestWithContext(e.ctx, http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotModified {
			t.Fatalf("availability (token %v): HTTP %d %s", token != "", rec.Code, rec.Body)
		}
	}
	path := "/api/v1/events/" + e.ev.ID.String() + "/availability"
	other := e.newEvent(10, e.clk.Now().Add(-time.Minute))
	_, err := e.inv.Rebuild(e.ctx, other)
	mustNil(t, err)
	foreign, _, err := e.keys.Issue(auth.AudAdmission, gone.u.id, other, nil, time.Hour)
	mustNil(t, err)

	for step := 1; step <= 7; step++ { // t = 20 .. 140 s, well past the 60 s idle TTL
		e.clk.Advance(20 * time.Second)
		poll(path, visible.token)
		poll(path, "")                                                     // the hidden tab polls without the token
		poll(path, foreign)                                                // a token for another event: ignored
		poll(path, "not-a-token")                                          // garbage: ignored, still a 200
		poll("/api/v1/events/"+other.String()+"/availability", gone.token) // wrong event
		e.tick()
	}
	if l, ok := e.leaseOf(visible.u.id); !ok || !l.Equal(e.clk.Now().Add(idle)) {
		t.Fatalf("polling user: lease %s (present %v), want now + idle", l, ok)
	}
	if r := e.status(visible.u); r.State != queue.StateAdmitted {
		t.Fatalf("polling user's status = %s, want ADMITTED", r.State)
	}
	for name, a := range map[string]admitted{"hidden tab": hidden, "not polling": gone} {
		if _, ok := e.leaseOf(a.u.id); ok {
			t.Errorf("%s: still in q:active after %s without a token request", name, 140*time.Second)
		}
		if r := e.status(a.u); r.State != queue.StateOfferExpired {
			t.Errorf("%s: status = %s, want OFFER_EXPIRED", name, r.State)
		}
	}
	if got := e.metric("queue_admission_touches_total"); got != 7 {
		t.Errorf("touches = %.0f, want 7 (one per visible poll)", got)
	}
}
