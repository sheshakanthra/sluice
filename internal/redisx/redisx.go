// Package redisx owns SLUICE's Redis access: the client constructor and the
// embedded Lua scripts (DATA-MODEL §5). Scripts run with EVALSHA and fall back
// to EVAL on NOSCRIPT (go-redis Script.Run), so a Redis restart or SCRIPT
// FLUSH costs one extra round trip, never an error. Key names come only from
// package keys.
package redisx

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// NewClient builds a go-redis client from REDIS_URL with bounded timeouts.
// The URL is never echoed (it may carry a password).
func NewClient(url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("parse REDIS_URL: invalid URL")
	}
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = 2 * time.Second
	opt.WriteTimeout = 2 * time.Second
	return redis.NewClient(opt), nil
}

//go:embed scripts/*.lua
var scriptFS embed.FS

// Script names (file names without .lua).
const (
	HoldSeats       = "hold_seats"
	HoldBest        = "hold_best"
	HoldExtend      = "hold_extend"
	HoldConsume     = "hold_consume"
	HoldRelease     = "hold_release"
	InventoryLoad   = "inventory_load"
	InventoryRepair = "inventory_repair"
	Snapshot        = "snapshot"
	QueueJoin       = "queue_join"
	QueueResolve    = "queue_resolve"
	QueueClaim      = "queue_claim"
	QueueLeave      = "queue_leave"
	QueueTick       = "queue_tick"
)

var scriptNames = []string{
	HoldSeats, HoldBest, HoldExtend, HoldConsume, HoldRelease, InventoryLoad, InventoryRepair, Snapshot,
	QueueJoin, QueueResolve, QueueClaim, QueueLeave, QueueTick,
}

// Scripts is the set of loaded scripts plus their latency histogram.
type Scripts struct {
	rdb      redis.Scripter
	scripts  map[string]*redis.Script
	duration *prometheus.HistogramVec
}

// NewScripts reads the embedded scripts. reg may be nil (no metrics).
func NewScripts(rdb redis.Scripter, reg prometheus.Registerer) (*Scripts, error) {
	s := &Scripts{rdb: rdb, scripts: make(map[string]*redis.Script, len(scriptNames))}
	for _, name := range scriptNames {
		src, err := scriptFS.ReadFile("scripts/" + name + ".lua")
		if err != nil {
			return nil, fmt.Errorf("redisx: read %s: %w", name, err)
		}
		s.scripts[name] = redis.NewScript(string(src))
	}
	if reg != nil {
		s.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "redis_script_duration_seconds",
			Help:    "Latency of SLUICE Lua scripts, including the NOSCRIPT fallback.",
			Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, 1},
		}, []string{"script"})
		if err := reg.Register(s.duration); err != nil {
			return nil, fmt.Errorf("redisx: register metrics: %w", err)
		}
		for _, name := range scriptNames {
			s.duration.WithLabelValues(name)
		}
	}
	return s, nil
}

// Load preloads every script (SCRIPT LOAD) so the first EVALSHA hits.
func (s *Scripts) Load(ctx context.Context) error {
	for name, sc := range s.scripts {
		if err := sc.Load(ctx, s.rdb).Err(); err != nil {
			return fmt.Errorf("redisx: load %s: %w", name, err)
		}
	}
	return nil
}

// Reply is a script's result: a status word followed by string fields.
type Reply struct {
	Status string
	Fields []string
}

// Int parses field i as an int64.
func (r Reply) Int(i int) (int64, error) {
	if i >= len(r.Fields) {
		return 0, fmt.Errorf("redisx: %s reply has no field %d", r.Status, i)
	}
	return strconv.ParseInt(r.Fields[i], 10, 64)
}

// Run executes script name. Every script returns an array whose first element
// is the status (snapshot returns only values; its Status is the first one).
func (s *Scripts) Run(ctx context.Context, name string, keys []string, args ...any) (Reply, error) {
	sc, ok := s.scripts[name]
	if !ok {
		return Reply{}, fmt.Errorf("redisx: unknown script %q", name)
	}
	start := time.Now()
	res, err := sc.Run(ctx, s.rdb, keys, args...).Slice()
	if s.duration != nil {
		s.duration.WithLabelValues(name).Observe(time.Since(start).Seconds())
	}
	if err != nil {
		return Reply{}, fmt.Errorf("redisx: %s: %w", name, err)
	}
	if len(res) == 0 {
		return Reply{}, fmt.Errorf("redisx: %s: empty reply", name)
	}
	out := make([]string, len(res))
	for i, v := range res {
		switch t := v.(type) {
		case string:
			out[i] = t
		case int64:
			out[i] = strconv.FormatInt(t, 10)
		case nil:
			out[i] = ""
		default:
			return Reply{}, fmt.Errorf("redisx: %s: unexpected %T in reply", name, v)
		}
	}
	return Reply{Status: out[0], Fields: out[1:]}, nil
}
