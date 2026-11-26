#!/usr/bin/env python3
"""Summarise dockerstats.sh output into a Markdown table (cpu.md).

usage: cpu_summary.py <docker-stats.csv>

The rush window is every sample taken while the swarm container
(sluice-rush-run-*) was running. Per container: samples, mean, p95 and max
CPU in cores (docker's CPU% / 100), and peak memory as docker printed it.
"Total" sums every container per sample. Reads only the CSV; changes nothing.
"""

import csv
import re
import sys
from collections import defaultdict


def short(name: str) -> str:
    # sluice-api1-1 -> api1, sluice-rush-run-3f2a... -> rush (swarm)
    if re.match(r"^sluice-rush-run-", name):
        return "rush (swarm)"
    m = re.match(r"^sluice-(.+?)-\d+$", name)
    return m.group(1) if m else name


def pct(values, q):
    v = sorted(values)
    if not v:
        return 0.0
    return v[min(len(v) - 1, int(q * len(v)))]


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    rows = []
    try:
        with open(sys.argv[1], newline="", encoding="utf-8") as f:
            for r in csv.DictReader(f):
                try:
                    rows.append((int(r["ts_unix"]), short(r["name"]), float(r["cpu_pct"].rstrip("%")) / 100, r["mem_usage"]))
                except (KeyError, ValueError, AttributeError):
                    continue
    except OSError as e:
        print(f"## Per-container CPU\n\n**Missing:** {e}\n")
        return 0
    rush_ts = {ts for ts, name, _, _ in rows if name == "rush (swarm)"}
    window = [r for r in rows if r[0] in rush_ts]
    print("## Per-container CPU (docker stats)\n")
    if not window:
        print(f"No samples while the swarm ran ({len(rows)} rows in total).\n")
        return 0
    per = defaultdict(list)
    mem = {}
    total = defaultdict(float)
    for ts, name, cores, m in window:
        per[name].append(cores)
        mem[name] = m
        total[ts] += cores
    t0, t1 = min(rush_ts), max(rush_ts)
    print(f"{len(rush_ts)} samples while the swarm ran ({t1 - t0} s, about one per {max(1, (t1 - t0) // max(1, len(rush_ts) - 1))} s). "
          "Cores = docker CPU% / 100. Source: `docker-stats.csv.gz`, `loadtest/ci/cpu_summary.py`.\n")
    print("| container | samples | mean cores | p95 cores | max cores | last memory |")
    print("|---|---|---|---|---|---|")
    for name in sorted(per, key=lambda n: -sum(per[n]) / len(per[n])):
        v = per[name]
        print(f"| {name} | {len(v)} | {sum(v) / len(v):.2f} | {pct(v, 0.95):.2f} | {max(v):.2f} | {mem[name]} |")
    tv = list(total.values())
    print(f"| **total** | {len(tv)} | {sum(tv) / len(tv):.2f} | {pct(tv, 0.95):.2f} | {max(tv):.2f} | |")
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
