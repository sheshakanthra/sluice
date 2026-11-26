#!/usr/bin/env bash
# usage: p7b-smoke3.sh <prefix> <n...> - run SMOKE once per suffix, GC stats after each (P7b Step 0)
P=$1; shift
for i in "$@"; do
  task rush:smoke RUN_ID=$P-$i > loadtest/results/$P-$i.log 2>&1; echo "$P-$i exit $?"
  bash loadtest/manual/p7b-gcq.sh 6m > loadtest/results/$P-$i.gc.txt 2>&1
done
