package inventory_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
)

const adminToken = "test-admin-token-not-secret"

type api struct {
	t   *testing.T
	e   *env
	srv *httptest.Server
}

func newAPI(t *testing.T, e *env) *api {
	t.Helper()
	kr, err := auth.NewKeyring([]config.TokenKey{{KID: "t1", Secret: []byte("0123456789abcdef0123456789abcdef")}}, nil)
	mustNil(t, err)
	log := slog.New(slog.DiscardHandler)
	rt := httpx.NewRouter()
	rt.HandleFunc("POST /api/v1/sessions", kr.SessionsHandler(log))
	rt.HandleFunc("POST /api/v1/test/admit", kr.TestAdmitHandler(func(ctx context.Context, s string) (uuid.UUID, bool, error) {
		ev, err := e.svc.Event(ctx, s)
		if err != nil {
			return uuid.Nil, false, nil //nolint:nilerr // unknown event is "not found" here
		}
		return ev.ID, true, nil
	}, 10*time.Minute, nil, log))
	inventory.RegisterRoutes(rt, inventory.HandlerDeps{
		Svc: e.svc, Avail: e.svc.NewAvailabilityCache(0), Keys: kr,
		Idem: httpx.NewIdempotency(e.q, auth.UserID, log, nil), QueueEnforced: true, AdminToken: adminToken, Log: log,
	})
	srv := httptest.NewServer(httpx.Chain(rt, httpx.RequestID(nil), httpx.Recover(log), httpx.Timeout(10*time.Second)))
	t.Cleanup(srv.Close)
	return &api{t: t, e: e, srv: srv}
}

type resp struct {
	code int
	hdr  http.Header
	body map[string]any
	raw  []byte
}

func (a *api) do(method, path, token, ik string, body any) resp {
	a.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		mustNil(a.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(a.e.ctx, method, a.srv.URL+path, rd)
	mustNil(a.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ik != "" {
		req.Header.Set(httpx.IdempotencyHeader, ik)
	}
	res, err := a.srv.Client().Do(req)
	mustNil(a.t, err)
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	mustNil(a.t, err)
	out := resp{code: res.StatusCode, hdr: res.Header, raw: raw}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

func (a *api) session() (uid, token string) {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/sessions", "", "", nil)
	if r.code != http.StatusCreated {
		a.t.Fatalf("session: %d %s", r.code, r.raw)
	}
	return str(a.t, r.body, "user_id"), str(a.t, r.body, "session_token")
}

func (a *api) admit(event, uid string) string {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/test/admit", "", "", map[string]string{"event_id": event, "user_id": uid})
	if r.code != http.StatusOK {
		a.t.Fatalf("admit: %d %s", r.code, r.raw)
	}
	return str(a.t, r.body, "admission_token")
}

func TestHoldAPI(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	holds := "/api/v1/events/" + e.slug + "/holds"

	uid, session := a.session()
	adm := a.admit(e.slug, uid)

	checks := []struct {
		name  string
		token string
		ik    string
		body  any
		code  int
		ecode string
	}{
		{"no token", "", newKey(), map[string]int{"quantity": 1}, 403, "NOT_ADMITTED"},
		{"session token while queue enforced", session, newKey(), map[string]int{"quantity": 1}, 401, "TOKEN_INVALID"},
		{"garbage token", "x.y.z", newKey(), map[string]int{"quantity": 1}, 401, "TOKEN_INVALID"},
		{"no idempotency key", adm, "", map[string]int{"quantity": 1}, 400, "IDEMPOTENCY_KEY_REQUIRED"},
		{"short idempotency key", adm, "short", map[string]int{"quantity": 1}, 400, "VALIDATION"},
		{"unknown field", adm, newKey(), map[string]int{"qty": 1}, 400, "VALIDATION"},
		{"too many seats", adm, newKey(), map[string]int{"quantity": 5}, 400, "VALIDATION"},
		{"no such seat", adm, newKey(), map[string][]int{"seat_nos": {999}}, 400, "VALIDATION"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			r := a.do(http.MethodPost, holds, c.token, c.ik, c.body)
			if r.code != c.code || r.body["code"] != c.ecode {
				t.Fatalf("got %d %s, want %d %s", r.code, r.raw, c.code, c.ecode)
			}
		})
	}

	ik := newKey()
	first := a.do(http.MethodPost, holds, adm, ik, map[string]int{"quantity": 2})
	if first.code != http.StatusCreated {
		t.Fatalf("hold: %d %s", first.code, first.raw)
	}
	if first.hdr.Get(httpx.ReplayedHeader) != "" {
		t.Error("first response marked as replayed")
	}
	labels, _ := first.body["labels"].([]any)
	if first.body["status"] != "ACTIVE" || len(labels) != 2 || first.body["fence"] == nil || first.body["expires_at"] == nil {
		t.Fatalf("hold body: %s", first.raw)
	}
	again := a.do(http.MethodPost, holds, adm, ik, map[string]int{"quantity": 2})
	if again.code != http.StatusCreated || again.hdr.Get(httpx.ReplayedHeader) != "true" {
		t.Fatalf("replay: %d replayed=%q", again.code, again.hdr.Get(httpx.ReplayedHeader))
	}
	fb, _ := json.Marshal(first.body)
	ab, _ := json.Marshal(again.body)
	if !bytes.Equal(fb, ab) {
		t.Fatalf("replayed body differs:\n%s\n%s", fb, ab)
	}
	if r := a.do(http.MethodPost, holds, adm, ik, map[string]int{"quantity": 1}); r.code != 422 || r.body["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("reused key: %d %s", r.code, r.raw)
	}
	if r := a.do(http.MethodPost, holds, adm, newKey(), map[string]int{"quantity": 1}); r.code != 409 || r.body["code"] != "HOLD_LIMIT" {
		t.Fatalf("second hold: %d %s", r.code, r.raw)
	}

	// Availability reflects the hold; If-None-Match gives 304.
	av := a.do(http.MethodGet, "/api/v1/events/"+e.slug+"/availability", "", "", nil)
	counts, _ := av.body["counts"].(map[string]any)
	if av.code != 200 || counts["held"] != float64(2) || av.hdr.Get("ETag") == "" {
		t.Fatalf("availability: %d %s", av.code, av.raw)
	}
	states, err := base64.StdEncoding.DecodeString(str(t, av.body, "states"))
	mustNil(t, err)
	for _, s := range seatList(t, first.body, "seat_nos") {
		if code := inventory.SeatCode(states, s); code != 1 {
			t.Errorf("seat %v code %02b, want held", s, code)
		}
	}
	req, _ := http.NewRequestWithContext(e.ctx, http.MethodGet, a.srv.URL+"/api/v1/events/"+e.slug+"/availability", http.NoBody)
	req.Header.Set("If-None-Match", av.hdr.Get("ETag"))
	nm, err := a.srv.Client().Do(req)
	mustNil(t, err)
	_ = nm.Body.Close()
	if nm.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", nm.StatusCode)
	}

	// Seats taken: problem+json with taken[].
	uid2, _ := a.session()
	adm2 := a.admit(e.ID(), uid2)
	taken := seatList(t, first.body, "seat_nos")
	r := a.do(http.MethodPost, holds, adm2, newKey(), map[string][]int{"seat_nos": taken})
	if r.code != 409 || r.body["code"] != "SEATS_TAKEN" || len(seatList(t, r.body, "taken")) != 2 || r.hdr.Get("Content-Type") != httpx.ProblemContentType {
		t.Fatalf("taken: %d %s", r.code, r.raw)
	}

	// An admission token for another event is refused.
	_, otherSlug := e.newEvent(10)
	if r := a.do(http.MethodPost, holds, a.admit(otherSlug, uid2), newKey(), map[string]int{"quantity": 1}); r.code != 401 {
		t.Fatalf("token for another event: %d %s", r.code, r.raw)
	}

	// Release: someone else's hold is 404; the owner's is 204 and the seats return.
	holdID := str(t, first.body, "hold_id")
	if r := a.do(http.MethodDelete, "/api/v1/holds/"+holdID, adm2, "", nil); r.code != 404 {
		t.Fatalf("release by other user: %d", r.code)
	}
	if r := a.do(http.MethodDelete, "/api/v1/holds/"+holdID, adm, "", nil); r.code != 204 {
		t.Fatalf("release: %d %s", r.code, r.raw)
	}
	av = a.do(http.MethodGet, "/api/v1/events/"+e.slug+"/availability", "", "", nil)
	if c, _ := av.body["counts"].(map[string]any); c["held"] != float64(0) {
		t.Errorf("after release counts = %v", c)
	}

	// Admin rebuild needs the admin token.
	rb := "/api/v1/admin/events/" + e.slug + "/rebuild-cache"
	if r := a.do(http.MethodPost, rb, adm, "", nil); r.code != 401 {
		t.Errorf("rebuild without admin token: %d", r.code)
	}
	if r := a.do(http.MethodPost, rb, adminToken, "", nil); r.code != 200 || r.body["available"] != float64(50) {
		t.Errorf("rebuild: %d %s", r.code, r.raw)
	}
	if got := metric(e, "holds_requests_total"); got < 4 {
		t.Errorf("holds_requests_total = %v", got)
	}
}

// TestHoldAPIConcurrentSameKey: 20 identical requests with one key at once
// give exactly one executed 201; the rest are replays or 409 in progress.
// Never two holds.
func TestHoldAPIConcurrentSameKey(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	uid, _ := a.session()
	adm := a.admit(e.slug, uid)
	ik := newKey()
	res := race(20, func(int) resp {
		return a.do(http.MethodPost, "/api/v1/events/"+e.slug+"/holds", adm, ik, map[string][]int{"seat_nos": {4, 5}})
	})
	var executed, replayed, inProgress int
	var holdID any
	for _, r := range res {
		switch {
		case r.code == 201 && r.hdr.Get(httpx.ReplayedHeader) == "":
			executed++
			holdID = r.body["hold_id"]
		case r.code == 201:
			replayed++
		case r.code == 409 && r.body["code"] == "REQUEST_IN_PROGRESS" && r.hdr.Get("Retry-After") == "1":
			inProgress++
		default:
			t.Fatalf("unexpected: %d %s", r.code, r.raw)
		}
	}
	t.Logf("executed=%d replayed=%d in_progress=%d", executed, replayed, inProgress)
	if executed != 1 {
		t.Fatalf("executed %d times, want exactly 1", executed)
	}
	if got := e.countHolds(gen.HoldStatusACTIVE); got != 1 {
		t.Fatalf("holds = %d, want 1", got)
	}
	if holdID != inventory.HoldID(uuid.MustParse(uid), ik).String() {
		t.Errorf("hold id %v is not UUIDv5(user:key)", holdID)
	}
}

// ID returns the env's event id as a string.
func (e *env) ID() string { return e.eventID.String() }

func str(t testing.TB, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is not a string in %v", key, m)
	}
	return s
}

func seatList(t testing.TB, m map[string]any, key string) []int {
	t.Helper()
	raw, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array in %v", key, m)
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		f, ok := v.(float64)
		if !ok {
			t.Fatalf("%s holds a non-number: %v", key, raw)
		}
		out = append(out, int(f))
	}
	return out
}
