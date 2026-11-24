package rush

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheshakanthra/sluice/internal/audit"
)

// TestAdmissionExpiredRejoins (ADR 0010 addendum part 3): a hold refused
// with 403 ADMISSION_EXPIRED is not an anomaly and not the end. The user
// rejoins (POST /queue?rejoin=true), uses the new admission token, and the
// rejoins share the two-rejoin budget: a user refused every time ends as
// NO_SEAT after two rejoins and leaves the queue.
func TestAdmissionExpiredRejoins(t *testing.T) {
	tests := []struct {
		name        string
		expireHolds int // holds answered ADMISSION_EXPIRED before one succeeds
		want        Outcome
		wantJoins   int
		wantHolds   int
	}{
		{"one lapse, then a hold", 1, OutAbandoned, 2, 2},
		{"lapses every time", 99, OutNoSeat, 3, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu         sync.Mutex
				joins      []string
				holdTokens []string
				leaves     int
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/sessions":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"session_token":"s"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/events/e/queue":
					joins = append(joins, r.URL.RawQuery)
					_, _ = w.Write([]byte(`{"state":"ADMITTED","admission_token":"a` + string(rune('0'+len(joins))) + `"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/events/e/holds":
					holdTokens = append(holdTokens, r.Header.Get("Authorization"))
					if len(holdTokens) <= tc.expireHolds {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"code":"ADMISSION_EXPIRED"}`))
						return
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"hold_id":"h1"}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/events/e/queue":
					leaves++
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusTeapot)
				}
			}))
			defer srv.Close()
			rec := NewRecorder(time.Now(), time.Minute)
			s := &swarm{o: &Options{Scenario: Scenarios["SMOKE"]}, rec: rec, eventPath: "/events/e", t0: time.Now(),
				anomalies: map[string]int64{}}
			u := &user{s: s, p: plan{qty: 1, abandon: true, rng: rand.New(rand.NewPCG(1, 2))}, //nolint:gosec // test
				c: &client{hc: &http.Client{Transport: newTransport(ConnDedicated)}, base: srv.URL, xff: "198.18.0.1", rec: rec}}
			got := u.run(t.Context())
			mu.Lock()
			defer mu.Unlock()
			if got != tc.want || len(joins) != tc.wantJoins || len(holdTokens) != tc.wantHolds {
				t.Fatalf("outcome %s, %d joins %v, %d holds; want %s, %d, %d", got, len(joins), joins, len(holdTokens), tc.want, tc.wantJoins, tc.wantHolds)
			}
			for i, q := range joins {
				if want := map[bool]string{true: "", false: "rejoin=true"}[i == 0]; q != want {
					t.Errorf("join %d query %q, want %q", i, q, want)
				}
			}
			for i, tok := range holdTokens {
				if want := "Bearer a" + string(rune('1'+i)); tok != want {
					t.Errorf("hold %d sent %q, want the admission from join %d (%q)", i, tok, i+1, want)
				}
			}
			if leaves != 1 {
				t.Errorf("leaves = %d, want 1 (NO_SEAT and ABANDONED leave)", leaves)
			}
			if len(s.anomalies) != 0 {
				t.Errorf("anomalies %v: ADMISSION_EXPIRED is an expected answer", s.anomalies)
			}
		})
	}
}

// TestRecordsAdmittedAfterSoldout: an ADMITTED answer counts against I7
// only when its request left more than the grace after the swarm first saw
// sold out on /stats; direct mode has no admissions to judge.
func TestRecordsAdmittedAfterSoldout(t *testing.T) {
	soldSeen := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	rows := map[int]statsRow{
		1: {ok: true, At: soldSeen.Add(-time.Second)},
		2: {ok: true, SoldOut: true, At: soldSeen},
		3: {ok: true, SoldOut: true, At: soldSeen.Add(time.Second)},
	}
	s := &swarm{o: &Options{Scenario: Scenarios["SMOKE"]}}
	for _, d := range []time.Duration{-5 * time.Second, 0, soldOutGrace, soldOutGrace + time.Millisecond, 3 * time.Second} {
		s.recordAdmit(soldSeen.Add(d))
	}
	r := s.records(rows)
	if r.AdmittedAfterSoldout == nil || *r.AdmittedAfterSoldout != 2 || r.Admissions != 5 || !r.SoldOutSeenAt.Equal(soldSeen) {
		t.Fatalf("records %+v (after=%v), want 2 of 5 after %s", r, r.AdmittedAfterSoldout, soldSeen)
	}
	never := s.records(map[int]statsRow{1: {ok: true}})
	if never.AdmittedAfterSoldout == nil || *never.AdmittedAfterSoldout != 0 || never.Note == "" {
		t.Errorf("never sold out: %+v", never)
	}
	direct := (&swarm{o: &Options{Scenario: Scenarios["B2"]}}).records(rows)
	if direct.AdmittedAfterSoldout != nil {
		t.Errorf("direct mode must not judge admissions: %+v", direct)
	}
}

// TestApplyAuditSchema: the audit verdict lands in summary.json (every
// LOADTEST §3.5 audit key plus the extras, null for checks that do not
// apply) and in report.md.
func TestApplyAuditSchema(t *testing.T) {
	arr, _ := ParseArrival(DefaultArrival)
	o := &Options{Users: 10, Scenario: Scenarios["B0"], Arrival: arr, Conn: ConnPooled, RunID: "t", APIReplicas: 3}
	t0 := time.Now()
	s := &swarm{o: o, t0: t0, rec: NewRecorder(t0.Add(-15*time.Second), time.Minute), anomalies: map[string]int64{}}
	dir := t.TempDir()
	if err := writeOutputs(dir, s.summarize(time.Now(), eventInfo{Slug: "e", Capacity: 10}, map[int]statsRow{}), nil); err != nil {
		t.Fatal(err)
	}
	one, zero := int64(1), int64(0)
	rep := &audit.Report{Mode: audit.ModeNaive, Passed: false, ExitCode: audit.ExitInvariantFail, Checks: []audit.Check{
		{Name: audit.CheckOversold, Count: &one}, {Name: audit.CheckDuplicateCharges, Count: &one},
		{Name: audit.CheckOrphanCharges, Count: &zero}, {Name: audit.CheckQuotaViolations, Count: &zero},
		{Name: audit.CheckStuckHolds, Pass: true, Note: "naive mode has no holds"},
		{Name: audit.CheckInventoryDrift, Pass: true}, {Name: audit.CheckTicketOrder, Count: &zero},
		{Name: audit.CheckAdmittedAfterSold, Pass: true}, {Name: audit.CheckUnresolvedUsers, Count: &zero},
	}}
	if _, err := ApplyAudit(dir, AuditSummary(rep)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Audit   map[string]json.RawMessage `json:"audit"`
		Records map[string]json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"oversold", "duplicate_charges", "orphan_charges", "quota_violations", "stuck_holds", "inventory_drift", "passed",
		"mode", "ticket_order_consistency", "admitted_after_soldout", "unresolved_users", "settle_s", "exit_code"} {
		if _, ok := m.Audit[k]; !ok {
			t.Errorf("audit block lacks %q", k)
		}
	}
	if string(m.Audit["oversold"]) != "1" || string(m.Audit["stuck_holds"]) != "null" || string(m.Audit["passed"]) != "false" {
		t.Errorf("audit block %v", m.Audit)
	}
	if string(m.Records["admitted_after_soldout"]) != "null" {
		t.Errorf("records %v: B0 is direct, nothing to judge", m.Records)
	}
	md, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil || !strings.Contains(string(md), "## Audit (naive mode") || !strings.Contains(string(md), "| 1 | 1 | 0 | 0 | n/a |") {
		t.Errorf("report.md audit table missing or wrong (%v):\n%s", err, md)
	}
}
