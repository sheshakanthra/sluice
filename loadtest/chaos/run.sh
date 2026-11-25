#!/usr/bin/env bash
# SLUICE chaos suite (P7 task 3). Each case: fresh stack with the Toxiproxy
# overlay, a SMOKE-sized rush, one fault, then the audit (sluice rush --audit).
#
#   bash loadtest/chaos/run.sh [C1 C2 C3 C4]      (task chaos runs all four)
#
# Env: USERS (5000), SEATS (500), SLUICE_ENV (rush). Results land in
# loadtest/results/chaos-<id>-<stamp>/ and the suite table in
# loadtest/results/chaos-<stamp>.md. Exit 0 only if every case passes.
#
# Pass rules (phase P7 table):
#   all:  oversold, duplicate_charges, orphan_charges, quota_violations = 0 (I1-I4)
#   C1:   + unresolved 0 (worker rebuilds inventory, queue restored)
#   C3:   duplicate and orphan charges 0 after settle (covered by "all")
#   C4:   + stuck_holds 0
#   The error rate around each fault is reported, not held to the budget.
set -euo pipefail

cd "$(dirname "$0")/../.."
USERS="${USERS:-5000}"
SEATS="${SEATS:-500}"
export SLUICE_ENV="${SLUICE_ENV:-rush}"
export SLUICE_SCENARIO=none
export SLUICE_VERSION="${SLUICE_VERSION:-dev}"
export SLUICE_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
STACK=(docker compose -f deploy/compose.yml -f deploy/compose.app.yml -f deploy/compose.rush.yml -f deploy/compose.chaos.yml)
TOXI="http://127.0.0.1:8474"
STAMP="$(date -u +%Y%m%d-%H%M%S)"
CASES=("$@")
[ ${#CASES[@]} -eq 0 ] && CASES=(C1 C2 C3 C4)

log() { printf '[chaos %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

# wait_for_t0 LOG SECONDS: block until the swarm's progress line for
# T0+SECONDS appears (lines are printed every 5 s: "T0 +15s | ...").
wait_for_t0() {
	local file="$1" secs="$2" pat
	pat="$(printf 'T0%+4ds |' "$secs")"
	for _ in $(seq 1 600); do
		if grep -qF -- "$pat" "$file" 2>/dev/null; then return 0; fi
		sleep 0.5
	done
	log "timed out waiting for '$pat'"
	return 1
}

toxic() { # toxic PROXY JSON
	curl -fsS -X POST -H 'Content-Type: application/json' -d "$2" "$TOXI/proxies/$1/toxics" >/dev/null
}

run_case() {
	local id="$1" run="chaos-$(echo "$1" | tr 'A-Z' 'a-z')-$STAMP" event="chaos-$(echo "$1" | tr 'A-Z' 'a-z')-$(date -u +%H%M%S)"
	local out="loadtest/results/$run.log" fault_at="" fault_note="" fault_t=""
	log "$id: fresh stack"
	"${STACK[@]}" --profile rush down -v --remove-orphans >/dev/null 2>&1 || true
	"${STACK[@]}" build migrate >/dev/null
	"${STACK[@]}" up -d --wait >/dev/null
	"${STACK[@]}" run --rm -T seed seed --event "$event" --capacity "$SEATS" --opens-in 30m >/dev/null

	if [ "$id" = C3 ]; then
		# Whole run: +2 s on 30% of API->MockPay connections, and 5% of
		# connections stall then close after 3 s (the PSP client times out).
		toxic mockpay '{"name":"slow","type":"latency","stream":"downstream","toxicity":0.3,"attributes":{"latency":2000,"jitter":0}}'
		toxic mockpay '{"name":"stall","type":"timeout","stream":"downstream","toxicity":0.05,"attributes":{"timeout":3000}}'
		fault_note="mockpay +2000 ms on 30% of connections, timeout(3000 ms) on 5%, whole run"
	fi

	log "$id: swarm $USERS users -> $run"
	mkdir -p loadtest/results
	set +e
	"${STACK[@]}" run --rm -T rush --target http://caddy:8000 --event "$event" --scenario SMOKE \
		--users "$USERS" --conn dedicated --opens-in 30s --run-id "$run" --audit >"$out" 2>&1 &
	local pid=$!
	set -e

	case "$id" in
	C1)
		wait_for_t0 "$out" 15
		fault_at="$(date -u +%H:%M:%S)"
		"${STACK[@]}" exec -T redis redis-cli FLUSHALL
		fault_note="redis-cli FLUSHALL at T0+15s" fault_t=15
		;;
	C2)
		wait_for_t0 "$out" 10
		fault_at="$(date -u +%H:%M:%S)"
		"${STACK[@]}" kill api2 >/dev/null
		fault_note="docker compose kill api2 at T0+10s (left down)" fault_t=10
		;;
	C4)
		wait_for_t0 "$out" 20
		fault_at="$(date -u +%H:%M:%S)"
		"${STACK[@]}" pause postgres >/dev/null
		sleep 3
		"${STACK[@]}" unpause postgres >/dev/null
		fault_note="docker compose pause postgres for 3 s at T0+20s" fault_t=20
		;;
	esac
	[ -n "$fault_at" ] && log "$id: fault injected at $fault_at UTC: $fault_note"

	set +e
	wait "$pid"
	local code=$?
	set -e
	log "$id: swarm+audit exit $code"
	python loadtest/chaos/verdict.py "$id" "loadtest/results/$run" "$code" "$fault_note" $fault_t >>"$TABLE"
	printf -- '- %s: %s; swarm log %s\n' "$id" "${fault_note:-none}" "$out" >>"$NOTES"
	if [ "$id" = C2 ]; then
		# Caddy's view of the killed replica, for the report.
		local nu hc
		nu="$("${STACK[@]}" logs caddy 2>/dev/null | grep -c 'no upstreams available' || true)"
		hc="$("${STACK[@]}" logs caddy 2>/dev/null | grep 'health_checker' | grep -c 'api2:8080' || true)"
		printf -- '  - caddy log: %s lines "no upstreams available", %s health-check lines naming api2\n' "$nu" "$hc" >>"$NOTES"
	fi
	if [ "$id" = C1 ]; then
		local rb
		rb="$("${STACK[@]}" logs worker 2>/dev/null | grep -ciE 'rebuil|restor' || true)"
		printf -- '  - worker log: %s lines mentioning rebuild/restore\n' "$rb" >>"$NOTES"
	fi
}

REPORT="loadtest/results/chaos-$STAMP.md"
TABLE="$(mktemp)"
NOTES="$(mktemp)"
python loadtest/chaos/verdict.py --header >"$TABLE"
for c in "${CASES[@]}"; do run_case "$c"; done
{
	printf '# Chaos suite %s (users %s, seats %s, commit %s)\n\n' "$STAMP" "$USERS" "$SEATS" "$SLUICE_COMMIT"
	cat "$TABLE"
	printf '\n## Faults\n'
	cat "$NOTES"
} >"$REPORT"
rm -f "$TABLE" "$NOTES"
cat "$REPORT"
if grep -q '| FAIL |' "$REPORT"; then exit 1; fi
