package orders_test

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/orders"
)

// TestCreateOrdersConcurrent stresses POST /orders (the path that once
// answered 500 under full-suite load, see docs/reports/P4b-queue.md):
// 200 users each hold one seat, then 200 goroutines create the 200 orders at
// once, and for every other hold a second goroutine races a duplicate create
// with a different Idempotency-Key.
//
// 300 simultaneous intent creates overload the in-test MockPay server on
// Windows (refused dials, timeouts), and the API then answers 503
// DEPENDENCY_UNAVAILABLE with Retry-After, as it should. The goroutines act
// like real clients and retry exactly that response (same key: 5xx answers
// are never stored), which exercises the resume path. Anything else that is
// not 2xx fails the test, and a 500 above all. At the end every request has
// one order, the duplicates got the same order, and PG holds exactly one
// order, one payment and one PSP intent per hold. The harness prints the
// server log (wrapped errors, SQLSTATE) if anything fails.
func TestCreateOrdersConcurrent(t *testing.T) {
	const n = 200
	h := newHarness(t, withCapacity(n))
	type job struct {
		u    user
		hold uuid.UUID
	}
	jobs := make([]job, n)
	for i := range jobs {
		u := h.newUser()
		jobs[i] = job{u: u, hold: h.hold(u, int32(i)).ID}
	}

	type result struct {
		hold     uuid.UUID
		code     int
		order    uuid.UUID
		body     string
		retried  int // 503 DEPENDENCY_UNAVAILABLE answers retried
		gaveUp   bool
		statuses []int
	}
	const maxTries = 40
	results := make(chan result, n+n/2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	create := func(j job) {
		<-start
		r := result{hold: j.hold}
		body, _ := json.Marshal(map[string]any{"hold_id": j.hold})
		key := uuid.NewString()
		var b []byte
		for try := 1; ; try++ {
			// In-process, straight into the API handler: 300 simultaneous
			// TCP dials overflow the Windows listen backlog of
			// httptest.Server and would test the OS, not the order path.
			req := httptest.NewRequestWithContext(h.ctx, http.MethodPost, "/api/v1/orders", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+j.u.admission)
			req.Header.Set(httpx.IdempotencyHeader, key)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.api.Config.Handler.ServeHTTP(rec, req)
			b = rec.Body.Bytes()
			r.code, r.body = rec.Code, string(b)
			r.statuses = append(r.statuses, rec.Code)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(r.body, string(httpx.CodeDependencyUnavailable)) {
				break
			}
			if try == maxTries {
				r.gaveUp = true
				break
			}
			r.retried++
			time.Sleep(time.Duration(50+rand.IntN(200)) * time.Millisecond) // jittered, like a client honouring Retry-After
		}
		var v orders.View
		if json.Unmarshal(b, &v) == nil {
			r.order = v.OrderID
		}
		results <- r
	}
	for i, j := range jobs {
		wg.Go(func() { create(j) })
		if i%2 == 0 {
			wg.Go(func() { create(j) }) // duplicate, racing the first
		}
	}
	close(start)
	wg.Wait()
	close(results)

	byHold := map[uuid.UUID]uuid.UUID{}
	codes := map[int]int{}
	retries := 0
	for r := range results {
		for _, c := range r.statuses {
			codes[c]++
		}
		retries += r.retried
		if r.gaveUp {
			t.Errorf("hold %s: still 503 after %d tries: %s", r.hold, maxTries, r.body)
			continue
		}
		if r.code != http.StatusCreated && r.code != http.StatusOK {
			t.Errorf("hold %s: HTTP %d %s (statuses %v)", r.hold, r.code, r.body, r.statuses)
			continue
		}
		if prev, ok := byHold[r.hold]; ok && prev != r.order {
			t.Errorf("hold %s: two orders %s and %s", r.hold, prev, r.order)
		}
		byHold[r.hold] = r.order
	}
	if len(byHold) != n {
		t.Errorf("%d holds got an order, want %d", len(byHold), n)
	}
	var orderCount, paymentCount, intents int
	mustNil(t, h.pool.QueryRow(h.ctx, `SELECT count(*) FROM orders WHERE event_id = $1`, h.eventID).Scan(&orderCount))
	// Hand-written SQL: test-only invariant counts across tables.
	mustNil(t, h.pool.QueryRow(h.ctx, `SELECT count(*), count(DISTINCT p.provider_intent_id) FROM payments p
		JOIN orders o ON o.id = p.order_id WHERE o.event_id = $1`, h.eventID).Scan(&paymentCount, &intents))
	if orderCount != n || paymentCount != n || intents != n {
		t.Errorf("PG: %d orders, %d payments, %d distinct intents; want %d each", orderCount, paymentCount, intents, n)
	}
	t.Logf("concurrent create: %d clients (%d duplicates), %d retried 503s -> all answers %v; %d orders, %d payments, %d intents",
		n+n/2, n/2, retries, codes, orderCount, paymentCount, intents)
}
