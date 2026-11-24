package redisx_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
	"github.com/sheshakanthra/sluice/internal/testutil/redistest"
)

var rs *redistest.Server

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	s, err := redistest.Start(ctx)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rs = s
	code := m.Run()
	s.Close()
	os.Exit(code)
}

const nowMS = int64(1_800_000_000_000)

type fixture struct {
	t   *testing.T
	ctx context.Context
	rdb *redis.Client
	sc  *redisx.Scripts
	eid uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	rdb := rs.NewClient(t)
	sc, err := redisx.NewScripts(rdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return &fixture{t: t, ctx: ctx, rdb: rdb, sc: sc, eid: uuid.New()}
}

func (f *fixture) run(name string, ks []string, args ...any) redisx.Reply {
	f.t.Helper()
	r, err := f.sc.Run(f.ctx, name, ks, args...)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	return r
}

type loadHold struct {
	hid, uid   uuid.UUID
	exp, fence int64
	state      string
	seats      string
}

func (f *fixture) loadKeys() []string {
	e := f.eid
	return []string{keys.Meta(e), keys.Avail(e), keys.Sold(e), keys.Holds(e), keys.Holding(e), keys.Fence(e), keys.Ver(e)}
}

// load runs inventory_load with seats [0,capacity) minus sold and held.
func (f *fixture) load(capacity int, sold []int, fenceFloor int64, holds ...loadHold) {
	f.t.Helper()
	busy := map[int]bool{}
	for _, s := range sold {
		busy[s] = true
	}
	for _, h := range holds {
		for _, s := range strings.Split(h.seats, ",") {
			n, _ := strconv.Atoi(s)
			busy[n] = true
		}
	}
	args := []any{keys.Prefix(f.eid), nowMS, fenceFloor, capacity, 4, nowMS, 50000, "ON_SALE"}
	var avail []any
	for s := range capacity {
		if !busy[s] {
			avail = append(avail, s)
		}
	}
	args = append(args, len(avail))
	args = append(args, avail...)
	args = append(args, len(sold))
	for _, s := range sold {
		args = append(args, s)
	}
	for _, h := range holds {
		args = append(args, h.hid.String(), h.uid.String(), h.exp, h.fence, h.state, h.seats)
	}
	if r := f.run(redisx.InventoryLoad, f.loadKeys(), args...); r.Status != "OK" {
		f.t.Fatalf("inventory_load: %+v", r)
	}
}

func (f *fixture) holdKeys(hid uuid.UUID) []string {
	e := f.eid
	return []string{keys.Meta(e), keys.Avail(e), keys.Holds(e), keys.Holding(e), keys.Fence(e), keys.Ver(e), keys.QSoldOut(e), keys.Hold(e, hid)}
}

func (f *fixture) holdSeats(hid, uid uuid.UUID, seats ...int) redisx.Reply {
	f.t.Helper()
	args := []any{hid.String(), uid.String(), nowMS, 60_000}
	for _, s := range seats {
		args = append(args, s)
	}
	return f.run(redisx.HoldSeats, f.holdKeys(hid), args...)
}

func (f *fixture) holdBest(hid, uid uuid.UUID, qty int) redisx.Reply {
	f.t.Helper()
	return f.run(redisx.HoldBest, f.holdKeys(hid), hid.String(), uid.String(), nowMS, 60_000, qty)
}

// release runs hold_release without touching the admission (a
// compensation); releaseFree also frees uid's admission ("" = the hash's uid).
func (f *fixture) release(hid uuid.UUID) redisx.Reply {
	f.t.Helper()
	return f.releaseArgs(hid, "", 0)
}

func (f *fixture) releaseFree(hid uuid.UUID, uid string) redisx.Reply {
	f.t.Helper()
	return f.releaseArgs(hid, uid, 1)
}

func (f *fixture) releaseArgs(hid uuid.UUID, uid string, free int) redisx.Reply {
	f.t.Helper()
	e := f.eid
	return f.run(redisx.HoldRelease, []string{keys.Hold(e, hid), keys.Holds(e), keys.Holding(e), keys.Avail(e), keys.Ver(e),
		keys.Sold(e), keys.QActive(e)}, hid.String(), uid, free)
}

func (f *fixture) consume(hid uuid.UUID) redisx.Reply {
	f.t.Helper()
	return f.consumeAs(hid, "")
}

func (f *fixture) consumeAs(hid uuid.UUID, uid string) redisx.Reply {
	f.t.Helper()
	e := f.eid
	return f.run(redisx.HoldConsume, []string{keys.Hold(e, hid), keys.Holds(e), keys.Holding(e), keys.Sold(e), keys.Ver(e),
		keys.Avail(e), keys.QActive(e)}, hid.String(), uid)
}

// admit puts uid in q:active (an admission that expires far in the future).
func (f *fixture) admit(uid uuid.UUID) {
	f.t.Helper()
	if err := f.rdb.ZAdd(f.ctx, keys.QActive(f.eid), redis.Z{Score: float64(nowMS + 600_000), Member: uid.String()}).Err(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) active(uid uuid.UUID) bool {
	f.t.Helper()
	err := f.rdb.ZScore(f.ctx, keys.QActive(f.eid), uid.String()).Err()
	if err != nil && !errors.Is(err, redis.Nil) {
		f.t.Fatal(err)
	}
	return err == nil
}

func (f *fixture) members(key string) []int {
	f.t.Helper()
	ss, err := f.rdb.SMembers(f.ctx, key).Result()
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]int, 0, len(ss))
	for _, s := range ss {
		n, err := strconv.Atoi(s)
		if err != nil {
			f.t.Fatalf("non-int member %q in %s", s, key)
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (f *fixture) isMember(key string, v any) bool {
	f.t.Helper()
	ok, err := f.rdb.SIsMember(f.ctx, key, v).Result()
	if err != nil {
		f.t.Fatal(err)
	}
	return ok
}

func (f *fixture) get(key string) string {
	f.t.Helper()
	v, err := f.rdb.Get(f.ctx, key).Result()
	if err != nil {
		f.t.Fatalf("get %s: %v", key, err)
	}
	return v
}

func TestHoldSeats(t *testing.T) {
	f := newFixture(t)
	hid, uid := uuid.New(), uuid.New()

	if r := f.holdSeats(hid, uid, 1); r.Status != "NOT_LOADED" {
		t.Fatalf("before load: got %+v, want NOT_LOADED", r)
	}
	f.load(10, nil, 0)
	verBefore := f.get(keys.Ver(f.eid))

	r := f.holdSeats(hid, uid, 1, 2)
	if r.Status != "OK" {
		t.Fatalf("got %+v, want OK", r)
	}
	if fence, _ := r.Int(0); fence != 1 {
		t.Errorf("fence = %d, want 1", fence)
	}
	if exp, _ := r.Int(1); exp != nowMS+60_000 {
		t.Errorf("exp = %d, want %d", exp, nowMS+60_000)
	}
	h, _ := f.rdb.HGetAll(f.ctx, keys.Hold(f.eid, hid)).Result()
	want := map[string]string{"uid": uid.String(), "seats": "1,2", "exp_ms": strconv.FormatInt(nowMS+60_000, 10), "fence": "1", "state": "ACTIVE"}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("hold.%s = %q, want %q", k, h[k], v)
		}
	}
	if pexp, _ := f.rdb.PExpireTime(f.ctx, keys.Hold(f.eid, hid)).Result(); pexp.Milliseconds() != nowMS+60_000+600_000 {
		t.Errorf("hold PEXPIRETIME = %d, want exp + 10 min", pexp.Milliseconds())
	}
	if score, _ := f.rdb.ZScore(f.ctx, keys.Holds(f.eid), hid.String()).Result(); int64(score) != nowMS+60_000 {
		t.Errorf("holds zset score = %v", score)
	}
	if !f.isMember(keys.Holding(f.eid), uid.String()) {
		t.Error("user not in holding")
	}
	if got := f.members(keys.Avail(f.eid)); slices.Contains(got, 1) || slices.Contains(got, 2) || len(got) != 8 {
		t.Errorf("avail = %v", got)
	}
	if f.get(keys.Ver(f.eid)) == verBefore {
		t.Error("ver did not bump")
	}

	tests := []struct {
		name   string
		hid    uuid.UUID
		uid    uuid.UUID
		seats  []int
		status string
		fields []string
	}{
		{"same hid replays", hid, uid, []int{1, 2}, "REPLAY", []string{"1", strconv.FormatInt(nowMS+60_000, 10), "1,2", uid.String()}},
		{"same user, new hid", uuid.New(), uid, []int{5}, "HOLD_LIMIT", nil},
		{"taken seat", uuid.New(), uuid.New(), []int{2}, "TAKEN", []string{"2"}},
		{"all or nothing: 2 of 3 taken", uuid.New(), uuid.New(), []int{3, 1, 4, 2}, "TAKEN", []string{"1", "2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := f.holdSeats(tc.hid, tc.uid, tc.seats...)
			if r.Status != tc.status || (tc.fields != nil && !slices.Equal(r.Fields, tc.fields)) {
				t.Fatalf("got %+v, want %s %v", r, tc.status, tc.fields)
			}
		})
	}
	// The failed multi-seat hold left 3 and 4 available.
	if !f.isMember(keys.Avail(f.eid), 3) || !f.isMember(keys.Avail(f.eid), 4) {
		t.Error("all-or-nothing violated: 3 or 4 left avail")
	}

	if err := f.rdb.Set(f.ctx, keys.QSoldOut(f.eid), "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if r := f.holdSeats(uuid.New(), uuid.New(), 7); r.Status != "SOLD_OUT" {
		t.Errorf("sold out: got %+v", r)
	}
}

func TestHoldBest(t *testing.T) {
	f := newFixture(t)
	f.load(5, []int{0}, 41)

	hid, uid := uuid.New(), uuid.New()
	r := f.holdBest(hid, uid, 3)
	if r.Status != "OK" || len(r.Fields) != 5 {
		t.Fatalf("got %+v", r)
	}
	if fence, _ := r.Int(0); fence != 42 {
		t.Errorf("fence = %d, want 42 (floor 41 + 1)", fence)
	}
	seats := r.Fields[2:]
	if !slices.IsSortedFunc(seats, func(a, b string) int { x, _ := strconv.Atoi(a); y, _ := strconv.Atoi(b); return x - y }) {
		t.Errorf("seats not sorted: %v", seats)
	}
	if slices.Contains(seats, "0") {
		t.Error("sold seat 0 was handed out")
	}
	if got := len(f.members(keys.Avail(f.eid))); got != 1 {
		t.Errorf("avail has %d, want 1", got)
	}
	if r := f.holdBest(uuid.New(), uuid.New(), 2); r.Status != "INSUFFICIENT" || r.Fields[0] != "1" {
		t.Errorf("got %+v, want INSUFFICIENT 1", r)
	}
	if r := f.holdBest(hid, uid, 3); r.Status != "REPLAY" || r.Fields[2] != strings.Join(seats, ",") {
		t.Errorf("replay: got %+v", r)
	}
	if r := f.holdBest(uuid.New(), uid, 1); r.Status != "HOLD_LIMIT" {
		t.Errorf("got %+v, want HOLD_LIMIT", r)
	}
}

func TestHoldExtend(t *testing.T) {
	f := newFixture(t)
	f.load(5, nil, 0)
	hid := uuid.New()
	ks := []string{keys.Hold(f.eid, hid), keys.Holds(f.eid)}
	if r := f.run(redisx.HoldExtend, ks, hid.String(), nowMS+1); r.Status != "MISSING" {
		t.Fatalf("got %+v, want MISSING", r)
	}
	f.holdSeats(hid, uuid.New(), 1)
	newExp := nowMS + 600_000
	if r := f.run(redisx.HoldExtend, ks, hid.String(), newExp); r.Status != "OK" {
		t.Fatalf("got %+v", r)
	}
	h, _ := f.rdb.HMGet(f.ctx, keys.Hold(f.eid, hid), "state", "exp_ms").Result()
	if h[0] != "PAYMENT" || h[1] != strconv.FormatInt(newExp, 10) {
		t.Errorf("hold = %v", h)
	}
	if score, _ := f.rdb.ZScore(f.ctx, keys.Holds(f.eid), hid.String()).Result(); int64(score) != newExp {
		t.Errorf("zset score %v, want %d", score, newExp)
	}
	if pexp, _ := f.rdb.PExpireTime(f.ctx, keys.Hold(f.eid, hid)).Result(); pexp.Milliseconds() != newExp+600_000 {
		t.Errorf("PEXPIRETIME %d", pexp.Milliseconds())
	}
}

func TestHoldConsume(t *testing.T) {
	f := newFixture(t)
	f.load(5, nil, 0)
	hid, uid := uuid.New(), uuid.New()
	f.holdSeats(hid, uid, 1, 3)
	ver := f.get(keys.Ver(f.eid))

	if r := f.consume(hid); r.Status != "OK" || r.Fields[0] != "1,3" {
		t.Fatalf("got %+v", r)
	}
	if got := f.members(keys.Sold(f.eid)); !slices.Equal(got, []int{1, 3}) {
		t.Errorf("sold = %v", got)
	}
	if n, _ := f.rdb.Exists(f.ctx, keys.Hold(f.eid, hid)).Result(); n != 0 {
		t.Error("hold hash not deleted")
	}
	if f.isMember(keys.Holding(f.eid), uid.String()) {
		t.Error("user still holding")
	}
	if f.get(keys.Ver(f.eid)) == ver {
		t.Error("ver did not bump")
	}
	if r := f.consume(hid); r.Status != "MISSING" {
		t.Errorf("second consume: %+v, want MISSING", r)
	}
	if r := f.release(hid); r.Status != "MISSING" {
		t.Errorf("release after consume: %+v, want MISSING", r)
	}
	if f.isMember(keys.Avail(f.eid), 1) {
		t.Error("consumed seat came back to avail")
	}
}

func TestHoldRelease(t *testing.T) {
	f := newFixture(t)
	f.load(5, nil, 0)
	hid, uid := uuid.New(), uuid.New()
	f.holdSeats(hid, uid, 2, 4)
	// A seat that is somehow also in sold is never handed back.
	if err := f.rdb.SAdd(f.ctx, keys.Sold(f.eid), 4).Err(); err != nil {
		t.Fatal(err)
	}
	if r := f.release(hid); r.Status != "OK" || r.Fields[0] != "2,4" {
		t.Fatalf("got %+v", r)
	}
	if got := f.members(keys.Avail(f.eid)); !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Errorf("avail = %v, want [0 1 2 3]", got)
	}
	if f.isMember(keys.Holding(f.eid), uid.String()) {
		t.Error("user still holding")
	}
	if n, _ := f.rdb.ZCard(f.ctx, keys.Holds(f.eid)).Result(); n != 0 {
		t.Errorf("holds zset has %d", n)
	}
	if r := f.release(hid); r.Status != "MISSING" {
		t.Errorf("second release: %+v", r)
	}
	// The user can hold again.
	if r := f.holdSeats(uuid.New(), uid, 2); r.Status != "OK" {
		t.Errorf("re-hold: %+v", r)
	}
}

// TestHoldEndFreesAdmission: hold_consume always, and hold_release with
// free_admission = 1, remove the user from q:active in the same script
// (ADR 0010 addendum); a compensation (free = 0) keeps it; a user who holds
// again keeps it; a repeat is a no-op.
func TestHoldEndFreesAdmission(t *testing.T) {
	cases := []struct {
		name      string
		end       func(f *fixture, hid, uid uuid.UUID) redisx.Reply
		wantFreed bool
	}{
		{"consume (CONFIRMED)", func(f *fixture, hid, _ uuid.UUID) redisx.Reply { return f.consume(hid) }, true},
		{"consume, explicit uid", func(f *fixture, hid, uid uuid.UUID) redisx.Reply { return f.consumeAs(hid, uid.String()) }, true},
		{"release, free (FAILED / EXPIRED / user release)", func(f *fixture, hid, _ uuid.UUID) redisx.Reply { return f.releaseFree(hid, "") }, true},
		{"release, compensation keeps it", func(f *fixture, hid, _ uuid.UUID) redisx.Reply { return f.release(hid) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.load(10, nil, 0)
			hid, uid, other := uuid.New(), uuid.New(), uuid.New()
			f.admit(uid)
			f.admit(other)
			if r := f.holdSeats(hid, uid, 1, 2); r.Status != "OK" {
				t.Fatalf("hold: %+v", r)
			}
			r := tc.end(f, hid, uid)
			if r.Status != "OK" {
				t.Fatalf("end: %+v", r)
			}
			freed, err := r.Int(len(r.Fields) - 1)
			mustNil(t, err)
			if (freed == 1) != tc.wantFreed || f.active(uid) == tc.wantFreed {
				t.Fatalf("freed=%d active=%v, want freed %v", freed, f.active(uid), tc.wantFreed)
			}
			if !f.active(other) {
				t.Fatal("another user's admission was touched")
			}
			// Idempotent: the repeat finds the hold MISSING and frees nothing new.
			if r := tc.end(f, hid, uid); r.Status != "MISSING" || r.Fields[0] != "0" {
				t.Fatalf("repeat: %+v", r)
			}
		})
	}

	t.Run("MISSING hold, known uid", func(t *testing.T) {
		f := newFixture(t)
		f.load(10, nil, 0)
		uid := uuid.New()
		f.admit(uid)
		if r := f.releaseFree(uuid.New(), uid.String()); r.Status != "MISSING" || r.Fields[0] != "1" || f.active(uid) {
			t.Fatalf("got %+v, active=%v", r, f.active(uid))
		}
	})

	t.Run("user holds again: admission kept", func(t *testing.T) {
		f := newFixture(t)
		f.load(10, nil, 0)
		old, cur, uid := uuid.New(), uuid.New(), uuid.New()
		f.admit(uid)
		f.holdSeats(old, uid, 1)
		f.release(old) // e.g. a compensation; then the user holds other seats
		if r := f.holdSeats(cur, uid, 3); r.Status != "OK" {
			t.Fatalf("re-hold: %+v", r)
		}
		// A late mirror of the old hold's end must not free the admission of
		// a user who is holding again.
		if r := f.releaseFree(old, uid.String()); r.Status != "MISSING" || r.Fields[0] != "0" || !f.active(uid) {
			t.Fatalf("late release: %+v active=%v", r, f.active(uid))
		}
		if r := f.consumeAs(old, uid.String()); r.Status != "MISSING" || r.Fields[0] != "0" || !f.active(uid) {
			t.Fatalf("late consume: %+v active=%v", r, f.active(uid))
		}
		// When the current hold ends, the admission goes.
		if r := f.consume(cur); r.Status != "OK" || f.active(uid) {
			t.Fatalf("consume current: %+v active=%v", r, f.active(uid))
		}
	})
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestInventoryLoad(t *testing.T) {
	f := newFixture(t)
	// Stale state that the load must wipe.
	f.load(20, nil, 0)
	stale := uuid.New()
	f.holdSeats(stale, uuid.New(), 5)
	if err := f.rdb.Set(f.ctx, keys.Fence(f.eid), 500, 0).Err(); err != nil {
		t.Fatal(err)
	}

	const capacity = 12_000 // > 5000 to cross the SADD chunk boundary
	h1 := loadHold{hid: uuid.New(), uid: uuid.New(), exp: nowMS + 1000, fence: 77, state: "ACTIVE", seats: "10,11"}
	h2 := loadHold{hid: uuid.New(), uid: uuid.New(), exp: nowMS + 2000, fence: 78, state: "PAYMENT", seats: "7000"}
	f.load(capacity, []int{3, 9_999}, 100, h1, h2)

	if n, _ := f.rdb.SCard(f.ctx, keys.Avail(f.eid)).Result(); n != capacity-5 {
		t.Errorf("avail = %d, want %d", n, capacity-5)
	}
	if got := f.members(keys.Sold(f.eid)); !slices.Equal(got, []int{3, 9_999}) {
		t.Errorf("sold = %v", got)
	}
	if n, _ := f.rdb.Exists(f.ctx, keys.Hold(f.eid, stale)).Result(); n != 0 {
		t.Error("stale hold hash survived the load")
	}
	ids, _ := f.rdb.ZRange(f.ctx, keys.Holds(f.eid), 0, -1).Result()
	if !slices.Equal(ids, []string{h1.hid.String(), h2.hid.String()}) {
		t.Errorf("holds zset = %v", ids)
	}
	if hs, _ := f.rdb.HGetAll(f.ctx, keys.Hold(f.eid, h2.hid)).Result(); hs["state"] != "PAYMENT" || hs["seats"] != "7000" || hs["fence"] != "78" {
		t.Errorf("h2 = %v", hs)
	}
	if got := f.get(keys.Fence(f.eid)); got != "500" {
		t.Errorf("fence = %s, want 500 (never goes backwards)", got)
	}
	// First load set ver = now_ms, the stale hold bumped it to now_ms+1, so
	// this load can only INCR (ver never goes backwards).
	if got := f.get(keys.Ver(f.eid)); got != strconv.FormatInt(nowMS+2, 10) {
		t.Errorf("ver = %s, want now_ms+2", got)
	}
	if v, _ := f.rdb.HGet(f.ctx, keys.Meta(f.eid), "loaded").Result(); v != "1" {
		t.Error("meta.loaded not set")
	}
	if !f.isMember(keys.Holding(f.eid), h1.uid.String()) {
		t.Error("h1 user not in holding")
	}
	// Loaded holds behave like live ones.
	if r := f.release(h1.hid); r.Status != "OK" || !f.isMember(keys.Avail(f.eid), 10) {
		t.Errorf("release loaded hold: %+v", r)
	}

	// Fence floor above the current value wins.
	f.load(5, nil, 1_000)
	if got := f.get(keys.Fence(f.eid)); got != "1000" {
		t.Errorf("fence = %s, want 1000", got)
	}
}

func TestInventoryRepair(t *testing.T) {
	f := newFixture(t)
	f.load(10, []int{9}, 0)
	hid, uid := uuid.New(), uuid.New()
	f.holdSeats(hid, uid, 1, 2)
	e := f.eid
	f.admit(uid)
	ks := []string{keys.Avail(e), keys.Sold(e), keys.Holds(e), keys.Holding(e), keys.Ver(e), keys.QActive(e)}
	// Forget hid without touching avail; then 1->A, 3->H, 4->S, 9->A.
	r := f.run(redisx.InventoryRepair, ks, keys.Prefix(e), 1, hid.String(), 1, "A", 3, "H", 4, "S", 9, "A")
	if r.Status != "OK" {
		t.Fatalf("got %+v", r)
	}
	if n, _ := f.rdb.Exists(f.ctx, keys.Hold(e, hid)).Result(); n != 0 {
		t.Error("hold not dropped")
	}
	if f.isMember(keys.Holding(e), uid.String()) {
		t.Error("user still holding")
	}
	if f.active(uid) {
		t.Error("forgotten (taken-over) hold kept its user's admission")
	}
	if got := f.members(keys.Avail(e)); !slices.Equal(got, []int{0, 1, 5, 6, 7, 8, 9}) {
		t.Errorf("avail = %v", got)
	}
	if got := f.members(keys.Sold(e)); !slices.Equal(got, []int{4}) {
		t.Errorf("sold = %v", got)
	}
}

func TestSnapshot(t *testing.T) {
	f := newFixture(t)
	f.load(10, []int{0, 1}, 0)
	f.holdSeats(uuid.New(), uuid.New(), 5)
	e := f.eid
	r := f.run(redisx.Snapshot, keys.SnapshotKeys(e))
	got := append([]string{r.Status}, r.Fields...)
	want := []string{"0", "0", "0", "0", "7", "1", "2", strconv.FormatInt(nowMS+1, 10), "0", "0", "0", "0"}
	if !slices.Equal(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

// TestNoScriptFallback: after SCRIPT FLUSH, EVALSHA fails with NOSCRIPT and
// the loader falls back to EVAL transparently.
func TestNoScriptFallback(t *testing.T) {
	f := newFixture(t)
	if err := f.sc.Load(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.load(3, nil, 0)
	if err := f.rdb.ScriptFlush(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if r := f.holdSeats(uuid.New(), uuid.New(), 1); r.Status != "OK" {
		t.Fatalf("after SCRIPT FLUSH: %+v", r)
	}
	if _, err := f.sc.Run(f.ctx, "nope", nil); err == nil {
		t.Error("unknown script: want error")
	}
}
