package orders_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// TestOrderCreateRefusesLapsedAdmission (ADR 0010 addendum part 3): POST
// /orders goes through Keyring.RequireLive with the real lease check
// (queue_touch.lua on real Redis), as the API wires it. A user whose lease
// lapsed gets 403 ADMISSION_EXPIRED and no order is created, though their
// token is valid and they hold seats; a user with a live lease gets 201.
func TestOrderCreateRefusesLapsedAdmission(t *testing.T) {
	h := newHarness(t)
	sc, err := redisx.NewScripts(h.rdb, nil)
	mustNil(t, err)
	lease, err := queue.NewLease(sc, time.Minute, h.clk.now, nil, nil)
	mustNil(t, err)
	log := slog.New(slog.DiscardHandler)
	rt := httpx.NewRouter()
	orders.RegisterRoutes(rt, orders.HandlerDeps{Svc: h.svc, Keys: h.keys.WithAdmissionTouch(lease.Touch),
		Idem: httpx.NewIdempotency(h.q, auth.UserID, log, nil), QueueEnforced: true, Log: log})
	mustNil(t, h.rdb.Set(h.ctx, keys.QInit(h.eventID), "1", 0).Err()) // the queue state exists

	now := h.clk.now()
	live, lapsed := h.newUser(), h.newUser()
	for u, exp := range map[uuid.UUID]time.Time{live.id: now.Add(time.Minute), lapsed.id: now.Add(-time.Second)} {
		mustNil(t, h.rdb.ZAdd(h.ctx, keys.QActive(h.eventID), redis.Z{Score: float64(exp.UnixMilli()), Member: u.String()}).Err())
	}
	create := func(u user, seat int32) *httptest.ResponseRecorder {
		hd := h.hold(u, seat)
		b, err := json.Marshal(map[string]any{"hold_id": hd.ID})
		mustNil(t, err)
		req := httptest.NewRequestWithContext(h.ctx, http.MethodPost, "/api/v1/orders", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+u.admission)
		req.Header.Set(httpx.IdempotencyHeader, uuid.NewString())
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec
	}

	rec := create(lapsed, 1)
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"ADMISSION_EXPIRED"`)) {
		t.Fatalf("lapsed user: POST /orders = HTTP %d %s, want 403 ADMISSION_EXPIRED", rec.Code, rec.Body)
	}
	var n int
	mustNil(t, h.pool.QueryRow(h.ctx, `SELECT count(*) FROM orders WHERE user_id = $1`, lapsed.id).Scan(&n))
	if n != 0 {
		t.Fatalf("lapsed user has %d orders, want 0", n)
	}
	if rec := create(live, 2); rec.Code != http.StatusCreated {
		t.Fatalf("live user: POST /orders = HTTP %d %s, want 201", rec.Code, rec.Body)
	}
}

// TestOrderCreatePastAdmissionCap (ADR 0018): a hold taken just before the
// admission's cap lives HOLD_TTL, so the order for it must still be
// possible after the cap. Past the cap (inside the spend grace) POST
// /orders turns the user's own live hold into an order, and nothing else:
// an expired hold, someone else's hold or a token past cap + grace is
// refused, and no order is created for them.
func TestOrderCreatePastAdmissionCap(t *testing.T) {
	h := newHarness(t)
	sc, err := redisx.NewScripts(h.rdb, nil)
	mustNil(t, err)
	lease, err := queue.NewLease(sc, time.Minute, h.clk.now, nil, nil)
	mustNil(t, err)
	const capTTL, holdTTL = 30 * time.Second, time.Minute // harness HOLD_TTL is 1 minute
	kr, err := auth.NewKeyring([]config.TokenKey{{KID: "t1", Secret: bytes.Repeat([]byte{7}, 32)}}, h.clk.now)
	mustNil(t, err)
	kr = kr.WithSpendGrace(holdTTL).WithAdmissionTouch(lease.Touch)
	log := slog.New(slog.DiscardHandler)
	rt := httpx.NewRouter()
	orders.RegisterRoutes(rt, orders.HandlerDeps{Svc: h.svc, Keys: kr, Now: h.clk.now,
		Idem: httpx.NewIdempotency(h.q, auth.UserID, log, nil), QueueEnforced: true, Log: log})
	mustNil(t, h.rdb.Set(h.ctx, keys.QInit(h.eventID), "1", 0).Err())

	admit := func(u user) string {
		tok, c, err := kr.Issue(auth.AudAdmission, u.id, h.eventID, nil, capTTL)
		mustNil(t, err)
		// The claim's lease, capped at the token's exp, as queue_claim.lua writes it.
		mustNil(t, h.rdb.ZAdd(h.ctx, keys.QActive(h.eventID), redis.Z{Score: float64(c.Expires.UnixMilli()), Member: u.id.String()}).Err())
		return tok
	}
	create := func(tok string, holdID uuid.UUID) *httptest.ResponseRecorder {
		b, err := json.Marshal(map[string]any{"hold_id": holdID})
		mustNil(t, err)
		req := httptest.NewRequestWithContext(h.ctx, http.MethodPost, "/api/v1/orders", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set(httpx.IdempotencyHeader, uuid.NewString())
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec
	}
	orderCount := func(u user) int {
		var n int
		mustNil(t, h.pool.QueryRow(h.ctx, `SELECT count(*) FROM orders WHERE user_id = $1`, u.id).Scan(&n))
		return n
	}

	buyer, other, late, stale := h.newUser(), h.newUser(), h.newUser(), h.newUser()
	buyerTok, otherTok, lateTok, staleTok := admit(buyer), admit(other), admit(late), admit(stale)
	h.clk.advance(capTTL - 2*time.Second) // holds taken just before the cap
	buyerHold, otherHold, lateHold := h.hold(buyer, 1), h.hold(other, 2), h.hold(late, 3)
	staleHold := h.hold(stale, 4)
	h.clk.advance(10 * time.Second) // 8 s past the cap, past ClockSkew too

	if rec := create(buyerTok, buyerHold.ID); rec.Code != http.StatusCreated {
		t.Fatalf("own live hold past the cap: HTTP %d %s, want 201", rec.Code, rec.Body)
	}
	if rec := create(otherTok, buyerHold.ID); rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"ADMISSION_EXPIRED"`)) {
		t.Fatalf("someone else's hold past the cap: HTTP %d %s, want 403 ADMISSION_EXPIRED", rec.Code, rec.Body)
	}
	_ = otherHold
	// Issue at T: cap T+30 s, holds taken at T+28 s expire at T+88 s, and the
	// grace ends at cap + ClockSkew + HOLD_TTL = T+95 s. Now T+90 s.
	h.clk.advance(52 * time.Second)
	if rec := create(staleTok, staleHold.ID); rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"ADMISSION_EXPIRED"`)) {
		t.Fatalf("expired hold past the cap: HTTP %d %s, want 403 ADMISSION_EXPIRED", rec.Code, rec.Body)
	}
	h.clk.advance(6 * time.Second) // T+96 s: past cap + ClockSkew + grace
	if rec := create(lateTok, lateHold.ID); rec.Code != http.StatusUnauthorized || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"TOKEN_EXPIRED"`)) {
		t.Fatalf("past cap + grace: HTTP %d %s, want 401 TOKEN_EXPIRED", rec.Code, rec.Body)
	}
	if orderCount(buyer) != 1 || orderCount(other) != 0 || orderCount(late) != 0 || orderCount(stale) != 0 {
		t.Fatalf("orders: buyer %d other %d late %d stale %d, want 1 0 0 0", orderCount(buyer), orderCount(other), orderCount(late), orderCount(stale))
	}
}
