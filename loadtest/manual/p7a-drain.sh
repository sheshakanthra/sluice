#!/usr/bin/env bash
# Graceful-stop check: 2k SMOKE swarm on the running stack; at T0+10s
# `docker compose stop api2` (SIGTERM -> readyz 503 -> drain -> exit).
set -u
cd "$(dirname "$0")/../.."
STACK=(docker compose -f deploy/compose.yml -f deploy/compose.app.yml -f deploy/compose.rush.yml)
export SLUICE_ENV=rush
EV="drain-$(date -u +%H%M%S)"; RUN="p7a-drain-$(date -u +%H%M%S)"; OUT="loadtest/results/$RUN.log"
"${STACK[@]}" run --rm -T seed seed --event "$EV" --capacity 500 --opens-in 30m >/dev/null
"${STACK[@]}" run --rm -T rush --target http://caddy:8000 --event "$EV" --scenario SMOKE --users 2000 --conn dedicated --opens-in 20s --run-id "$RUN" --audit >"$OUT" 2>&1 &
pid=$!
until grep -qF 'T0 +10s |' "$OUT" 2>/dev/null; do sleep 0.5; done
echo "stop api2 at $(date -u +%T)"; time "${STACK[@]}" stop api2
wait $pid; echo "swarm exit $?"
"${STACK[@]}" logs api2 2>&1 | grep -E "draining|shutting down|api stopped" | cut -c1-200
sed -n '/## Errors/,/## Generator/p;/## Audit/,$p' "$OUT"
"${STACK[@]}" start api2 >/dev/null
