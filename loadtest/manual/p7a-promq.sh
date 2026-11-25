#!/usr/bin/env bash
# usage: promq.sh <window e.g. 4m>  — run-level pool/shed/readyz stats from Prometheus
W=${1:-4m}
q(){ printf '%s\n  ' "$1"; curl -s --data-urlencode "query=$2" http://localhost:9090/api/v1/query | python -c "import json,sys; print('; '.join(f\"{r['metric'].get('instance','')}{'/'+r['metric']['pool'] if 'pool' in r['metric'] else ''}={float(r['value'][1]):.3f}\" for r in json.load(sys.stdin)['data']['result']))"; }
q "max acquired conns (main)" "max_over_time(sluice_pgxpool_acquired_conns{pool=\"main\"}[$W])"
q "max acquired conns (health)" "max_over_time(sluice_pgxpool_acquired_conns{pool=\"health\"}[$W])"
q "acquires in window (main, api)" "sum(increase(sluice_pgxpool_acquire_total{pool=\"main\",role=\"api\"}[$W]))"
q "avg wait per empty acquire s (api main)" "sum(increase(sluice_pgxpool_empty_acquire_wait_seconds_total{pool=\"main\",role=\"api\"}[$W]))/sum(increase(sluice_pgxpool_empty_acquire_total{pool=\"main\",role=\"api\"}[$W]))"
q "share of acquires that waited (api main)" "sum(increase(sluice_pgxpool_empty_acquire_total{pool=\"main\",role=\"api\"}[$W]))/sum(increase(sluice_pgxpool_acquire_total{pool=\"main\",role=\"api\"}[$W]))"
q "canceled acquires (api main)" "sum(increase(sluice_pgxpool_canceled_acquire_total{pool=\"main\",role=\"api\"}[$W]))"
q "shed total" "sum(increase(sluice_http_shed_total[$W]))"
q "max in-flight" "max_over_time(sluice_http_requests_in_flight{server=\"api\"}[$W])"
q "readyz p99 s" "histogram_quantile(0.99, sum by (le) (increase(sluice_http_request_duration_seconds_bucket{route=\"GET /readyz\"}[$W])))"
q "readyz max-bucket count >0.5s" "sum(increase(sluice_http_request_duration_seconds_count{route=\"GET /readyz\"}[$W])) - sum(increase(sluice_http_request_duration_seconds_bucket{route=\"GET /readyz\",le=\"0.5\"}[$W]))"
q "readyz non-200" "sum(increase(sluice_http_requests_total{route=\"GET /readyz\",code!=\"200\"}[$W]))"
q "min upstream healthy" "min_over_time(caddy_reverse_proxy_upstreams_healthy[$W])"
q "redis main pool timeouts" "sum(increase(sluice_redis_pool_timeouts_total{pool=\"main\"}[$W]))"
q "max PG backends" "max_over_time(sluice_pgxpool_total_conns[$W])"
