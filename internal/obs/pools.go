package obs

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// poolCollector exports pgxpool and go-redis pool stats on every scrape
// (P7 task 1). The pool label names the pool ("main", "health").
type poolCollector struct {
	pg    map[string]*pgxpool.Pool
	redis map[string]*redis.Client

	pgAcquired, pgIdle, pgTotal, pgMax, pgConstructing *prometheus.Desc
	pgAcquireCount, pgEmptyAcquire, pgCanceled         *prometheus.Desc
	pgAcquireSeconds, pgEmptyWaitSeconds               *prometheus.Desc

	rTotal, rIdle, rStale, rPending   *prometheus.Desc
	rHits, rMisses, rTimeouts, rWaits *prometheus.Desc
	rWaitSeconds                      *prometheus.Desc
}

func desc(sub, name, help string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(Namespace, sub, name), help, []string{"pool"}, nil)
}

// RegisterPoolStats registers sluice_pgxpool_* and sluice_redis_pool_*.
func RegisterPoolStats(reg prometheus.Registerer, pg map[string]*pgxpool.Pool, rc map[string]*redis.Client) error {
	c := &poolCollector{
		pg: pg, redis: rc,
		pgAcquired:         desc("pgxpool", "acquired_conns", "Connections currently checked out."),
		pgIdle:             desc("pgxpool", "idle_conns", "Idle connections."),
		pgTotal:            desc("pgxpool", "total_conns", "Open connections."),
		pgMax:              desc("pgxpool", "max_conns", "Pool size cap."),
		pgConstructing:     desc("pgxpool", "constructing_conns", "Connections being opened."),
		pgAcquireCount:     desc("pgxpool", "acquire_total", "Successful acquires."),
		pgEmptyAcquire:     desc("pgxpool", "empty_acquire_total", "Acquires that had to wait for a connection."),
		pgCanceled:         desc("pgxpool", "canceled_acquire_total", "Acquires cancelled by their context while waiting."),
		pgAcquireSeconds:   desc("pgxpool", "acquire_seconds_total", "Total time spent in successful acquires."),
		pgEmptyWaitSeconds: desc("pgxpool", "empty_acquire_wait_seconds_total", "Total time spent waiting in acquires that found the pool empty."),
		rTotal:             desc("redis_pool", "total_conns", "Open connections."),
		rIdle:              desc("redis_pool", "idle_conns", "Idle connections."),
		rStale:             desc("redis_pool", "stale_conns_total", "Stale connections removed."),
		rPending:           desc("redis_pool", "pending_requests", "Requests waiting for a connection."),
		rHits:              desc("redis_pool", "hits_total", "Gets that found a free connection."),
		rMisses:            desc("redis_pool", "misses_total", "Gets that found no free connection."),
		rTimeouts:          desc("redis_pool", "timeouts_total", "Gets that timed out waiting."),
		rWaits:             desc("redis_pool", "waits_total", "Gets that waited for a connection."),
		rWaitSeconds:       desc("redis_pool", "wait_seconds_total", "Total time spent waiting for a connection."),
	}
	return reg.Register(c)
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.pgAcquired, c.pgIdle, c.pgTotal, c.pgMax, c.pgConstructing,
		c.pgAcquireCount, c.pgEmptyAcquire, c.pgCanceled, c.pgAcquireSeconds, c.pgEmptyWaitSeconds,
		c.rTotal, c.rIdle, c.rStale, c.rPending, c.rHits, c.rMisses, c.rTimeouts, c.rWaits, c.rWaitSeconds,
	} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	g := func(d *prometheus.Desc, v float64, pool string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, pool)
	}
	ctr := func(d *prometheus.Desc, v float64, pool string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, pool)
	}
	for name, p := range c.pg {
		s := p.Stat()
		g(c.pgAcquired, float64(s.AcquiredConns()), name)
		g(c.pgIdle, float64(s.IdleConns()), name)
		g(c.pgTotal, float64(s.TotalConns()), name)
		g(c.pgMax, float64(s.MaxConns()), name)
		g(c.pgConstructing, float64(s.ConstructingConns()), name)
		ctr(c.pgAcquireCount, float64(s.AcquireCount()), name)
		ctr(c.pgEmptyAcquire, float64(s.EmptyAcquireCount()), name)
		ctr(c.pgCanceled, float64(s.CanceledAcquireCount()), name)
		ctr(c.pgAcquireSeconds, s.AcquireDuration().Seconds(), name)
		ctr(c.pgEmptyWaitSeconds, s.EmptyAcquireWaitTime().Seconds(), name)
	}
	for name, r := range c.redis {
		s := r.PoolStats()
		g(c.rTotal, float64(s.TotalConns), name)
		g(c.rIdle, float64(s.IdleConns), name)
		ctr(c.rStale, float64(s.StaleConns), name)
		g(c.rPending, float64(s.PendingRequests), name)
		ctr(c.rHits, float64(s.Hits), name)
		ctr(c.rMisses, float64(s.Misses), name)
		ctr(c.rTimeouts, float64(s.Timeouts), name)
		ctr(c.rWaits, float64(s.WaitCount), name)
		ctr(c.rWaitSeconds, float64(s.WaitDurationNs)/1e9, name)
	}
}
