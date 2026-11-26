#!/usr/bin/env bash
# Per-container CPU and memory for a CI rush (LOADTEST §7, ADR 0015 addendum).
#
# usage: dockerstats.sh <out.csv>   (runs until killed)
#
# Appends one row per running container per sample:
#   ts_unix,name,cpu_pct,mem_usage,pids
# cpu_pct is docker's figure: percent of ONE core (400% = all 4 cores).
# Each `docker stats --no-stream` call computes CPU from two readings about
# 1-2 s apart, so samples land every ~2-3 s, not every second. The sampler's
# own cost (one docker CLI call per sample) is small next to the stack but
# not zero; it runs on the same runner and is disclosed in cpu.md.
set -u
out="$1"
echo "ts_unix,name,cpu_pct,mem_usage,pids" > "$out"
while :; do
  ts="$(date +%s)"
  docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.PIDs}}' 2>/dev/null \
    | sed "s/^/${ts},/" >> "$out"
  sleep 1
done
