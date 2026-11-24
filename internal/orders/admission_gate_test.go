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
