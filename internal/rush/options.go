// Package rush is the SLUICE load generator ("the swarm", docs/LOADTEST.md §3).
//
// Every simulated user is one goroutine with its own anonymous session, its
// own keep-alive connection (--conn dedicated), its own Idempotency-Keys and a
// complete journey from session to a terminal outcome. Latency is recorded in
// HdrHistograms against each request's *intended* start time, so a swarm (or
// server) that falls behind schedule shows up as latency instead of hiding it
// (coordinated omission).
package rush

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Conn modes.
const (
	ConnDedicated = "dedicated" // one http.Transport per user, MaxConnsPerHost=1
	ConnPooled    = "pooled"    // one shared transport (laptop-friendly)
)

// ArrivalWindow places Share of the users uniformly in [From, To) relative to T0.
type ArrivalWindow struct {
	Name  string
	Share float64
	From  time.Duration
	To    time.Duration
}

// Options are the `sluice rush` flags.
type Options struct {
	Target      string
	Event       string
	Users       int
	Scenario    Scenario
	Arrival     []ArrivalWindow
	OpensIn     time.Duration
	Conn        string
	Out         string
	Seed        uint64
	MaxDuration time.Duration
	RunID       string
	AdminToken  string // in memory only; never serialised or logged
	// Reported context (summary.json "env"/"config"); the swarm cannot see
	// the servers' config, so the compose file passes it in.
	Runner         string
	APIReplicas    int
	HoldTTL        time.Duration
	PaymentHoldTTL time.Duration
	PGVersion      string
	RedisVersion   string
	UsersPerIP     int
	ProgressEvery  time.Duration
}

// DefaultArrival is LOADTEST §3.1: 70% in the 10 s before T0, 30% in the 10 s after.
const DefaultArrival = "pre=0.7:-10s..0s,post=0.3:0s..10s"

// Getenv reads an environment variable ("" when unset).
type Getenv func(string) string

// ParseArgs parses `sluice rush` flags. Defaults for secrets and reported
// server settings come from the environment (the same rush.env the servers use).
func ParseArgs(args []string, getenv Getenv, stderr io.Writer) (*Options, error) {
	fs := flag.NewFlagSet("sluice rush", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := &Options{}
	var scenario, arrival string
	fs.StringVar(&o.Target, "target", "http://caddy:8000", "front door base URL")
	fs.StringVar(&o.Event, "event", "tatkal-chn-sbc", "event slug or id")
	fs.IntVar(&o.Users, "users", 0, "simulated users (0 = the scenario default)")
	fs.StringVar(&scenario, "scenario", "SMOKE", "H | SMOKE | B0 | B1 | B2")
	fs.StringVar(&arrival, "arrival", DefaultArrival, "arrival profile: name=share:from..to,... relative to T0")
	fs.DurationVar(&o.OpensIn, "opens-in", 60*time.Second, "T0 = now + this (calls admin open-sale)")
	fs.StringVar(&o.Conn, "conn", ConnDedicated, "dedicated | pooled")
	fs.StringVar(&o.Out, "out", "", "output directory (default loadtest/results/<run_id>)")
	fs.Uint64Var(&o.Seed, "seed", 42, "behaviour-mix seed")
	fs.DurationVar(&o.MaxDuration, "max-duration", 15*time.Minute, "hard stop after T0")
	fs.StringVar(&o.RunID, "run-id", "", "run id (default <scenario>-<utc timestamp>)")
	fs.StringVar(&o.Runner, "runner", envOr(getenv, "RUSH_RUNNER", "local"), "runner label for the report")
	fs.IntVar(&o.APIReplicas, "api-replicas", 3, "API replicas behind the front door (reported)")
	fs.IntVar(&o.UsersPerIP, "users-per-ip", 10, "users sharing one synthetic X-Forwarded-For IP")
	fs.DurationVar(&o.ProgressEvery, "progress", 5*time.Second, "progress line interval")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	sc, ok := Scenarios[strings.ToUpper(scenario)]
	if !ok {
		return nil, fmt.Errorf("--scenario %q: want H, SMOKE, B0, B1 or B2", scenario)
	}
	o.Scenario = sc
	if o.Users == 0 {
		o.Users = sc.Users
	}
	var err error
	if o.Arrival, err = ParseArrival(arrival); err != nil {
		return nil, fmt.Errorf("--arrival: %w", err)
	}
	o.AdminToken = getenv("ADMIN_TOKEN")
	o.HoldTTL = envDuration(getenv, "HOLD_TTL")
	o.PaymentHoldTTL = envDuration(getenv, "PAYMENT_HOLD_TTL")
	o.PGVersion = envOr(getenv, "RUSH_PG_VERSION", "unknown")
	o.RedisVersion = envOr(getenv, "RUSH_REDIS_VERSION", "unknown")
	if o.RunID == "" {
		o.RunID = strings.ToLower(sc.Name) + "-" + time.Now().UTC().Format("20060102-150405")
	}
	if o.Out == "" {
		// The swarm container mounts loadtest/results at RUSH_RESULTS_DIR.
		o.Out = envOr(getenv, "RUSH_RESULTS_DIR", "loadtest/results") + "/" + o.RunID
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Options) validate() error {
	var errs []error
	if o.Users < 1 || o.Users > 200_000 {
		errs = append(errs, fmt.Errorf("--users %d: want 1..200000", o.Users))
	}
	if o.Conn != ConnDedicated && o.Conn != ConnPooled {
		errs = append(errs, fmt.Errorf("--conn %q: want dedicated or pooled", o.Conn))
	}
	if o.OpensIn < 0 || o.OpensIn > 24*time.Hour {
		errs = append(errs, fmt.Errorf("--opens-in %s: want 0..24h", o.OpensIn))
	}
	if o.MaxDuration <= 0 {
		errs = append(errs, errors.New("--max-duration must be positive"))
	}
	if o.AdminToken == "" {
		errs = append(errs, errors.New("ADMIN_TOKEN must be set (the swarm calls admin open-sale)"))
	}
	if o.UsersPerIP < 1 {
		errs = append(errs, errors.New("--users-per-ip must be ≥ 1"))
	}
	if o.ProgressEvery <= 0 {
		errs = append(errs, errors.New("--progress must be positive"))
	}
	for _, w := range o.Arrival {
		if w.From < -o.OpensIn {
			errs = append(errs, fmt.Errorf("--arrival %s starts %s before T0 but --opens-in is only %s", w.Name, -w.From, o.OpensIn))
		}
	}
	return errors.Join(errs...)
}

// ParseArrival parses "pre=0.7:-10s..0s,post=0.3:0s..10s". Shares must sum to 1 (±0.001).
func ParseArrival(s string) ([]ArrivalWindow, error) {
	var out []ArrivalWindow
	sum := 0.0
	for part := range strings.SplitSeq(s, ",") {
		name, rest, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%q: want name=share:from..to", part)
		}
		shareS, span, ok := strings.Cut(rest, ":")
		if !ok {
			return nil, fmt.Errorf("%q: want name=share:from..to", part)
		}
		share, err := strconv.ParseFloat(shareS, 64)
		if err != nil || share <= 0 || share > 1 {
			return nil, fmt.Errorf("%q: share must be in (0, 1]", part)
		}
		fromS, toS, ok := strings.Cut(span, "..")
		if !ok {
			return nil, fmt.Errorf("%q: want from..to", part)
		}
		from, err1 := time.ParseDuration(fromS)
		to, err2 := time.ParseDuration(toS)
		if err1 != nil || err2 != nil || to <= from {
			return nil, fmt.Errorf("%q: want from..to durations with to > from", part)
		}
		out = append(out, ArrivalWindow{Name: name, Share: share, From: from, To: to})
		sum += share
	}
	if sum < 0.999 || sum > 1.001 {
		return nil, fmt.Errorf("shares sum to %.3f, want 1", sum)
	}
	return out, nil
}

func envOr(getenv Getenv, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// envDuration reads a duration env var for reporting; unset or invalid is 0.
func envDuration(getenv Getenv, key string) time.Duration {
	d, err := time.ParseDuration(getenv(key))
	if err != nil {
		return 0
	}
	return d
}
