#!/usr/bin/env bash
# usage: p7b-gcq.sh <window e.g. 5m> - per-process GC pressure from Prometheus (P7b Step 0)
W=${1:-5m}
q(){ printf '%s\n  ' "$1"; curl -s --data-urlencode "query=$2" http://localhost:9090/api/v1/query | python -c "import json,sys; print('; '.join(f\"{r['metric'].get('instance','?')}={float(r['value'][1]):.4g}\" for r in sorted(json.load(sys.stdin)['data']['result'],key=lambda r:r['metric'].get('instance',''))))"; }
q "GC cycles in window" "increase(go_gc_duration_seconds_count{mode!=\"dev\"}[$W])"
q "peak GC cycles/s (30s rate)" "max_over_time(rate(go_gc_duration_seconds_count{mode!=\"dev\"}[30s])[$W:5s])"
q "GC pause seconds total in window" "increase(go_gc_duration_seconds_sum{mode!=\"dev\"}[$W])"
q "max GC pause s" "max_over_time(go_gc_duration_seconds{quantile=\"1\",mode!=\"dev\"}[$W])"
q "max heap alloc MiB" "max_over_time(go_memstats_heap_alloc_bytes{mode!=\"dev\"}[$W])/1048576"
q "GOMEMLIMIT MiB" "go_gc_gomemlimit_bytes{mode!=\"dev\"}/1048576"
q "GOGC" "go_gc_gogc_percent{mode!=\"dev\"}"
q "max CPU cores (1m rate)" "max_over_time(rate(process_cpu_seconds_total{mode!=\"dev\"}[30s])[$W:5s])"
