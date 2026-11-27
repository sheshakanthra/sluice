#!/usr/bin/env python3
"""Compare what the server saw in two rush runs (ADR 0019).

    python -I loadtest/ci/shape_compare.py <run_dir_a> <run_dir_b>

Prints, as Markdown: requests per route per user, outcomes, and the rps
shape (rps_total per 5 s bucket relative to T0, from timeseries.csv) with
the correlation between the two curves. Reads summary.json and
timeseries.csv only; standard library only.
"""

import csv
import json
import math
import sys
from pathlib import Path

ROUTES = ["session", "join", "status", "leave", "availability", "hold", "order", "pay", "payments", "confirm", "release"]


def load(d):
    d = Path(d)
    s = json.loads((d / "summary.json").read_text(encoding="utf-8"))
    rows = list(csv.DictReader((d / "timeseries.csv").open(encoding="utf-8")))
    return s, rows


def buckets(rows, width=5):
    out = {}
    for r in rows:
        b = math.floor(float(r["t"]) / width) * width
        out[b] = out.get(b, 0) + int(r["rps_total"])
    return {k: v / width for k, v in out.items()}


def corr(xs, ys):
    n = len(xs)
    if n < 2:
        return float("nan")
    mx, my = sum(xs) / n, sum(ys) / n
    sx = math.sqrt(sum((x - mx) ** 2 for x in xs))
    sy = math.sqrt(sum((y - my) ** 2 for y in ys))
    if sx == 0 or sy == 0:
        return float("nan")
    return sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / (sx * sy)


def main(a, b):
    sa, ra = load(a)
    sb, rb = load(b)
    na, nb = Path(a).name, Path(b).name
    ua, ub = sa["config"]["users"], sb["config"]["users"]
    print(f"## Requests per route per user ({na}: {ua} users, {nb}: {ub} users)\n")
    print(f"| route | {na} n | {nb} n | {na} /user | {nb} /user | diff |")
    print("|---|---|---|---|---|---|")
    ta = tb = 0
    for rt in ROUTES:
        x, y = sa["latency_ms"][rt]["n"], sb["latency_ms"][rt]["n"]
        ta, tb = ta + x, tb + y
        px, py = x / ua, y / ub
        d = f"{(py - px) / px * 100:+.1f}%" if px else ("0" if py == 0 else "new")
        print(f"| {rt} | {x} | {y} | {px:.3f} | {py:.3f} | {d} |")
    print(f"| **total** | {ta} | {tb} | {ta / ua:.3f} | {tb / ub:.3f} | {((tb / ub) - (ta / ua)) / (ta / ua) * 100:+.1f}% |\n")

    print("## Outcomes\n")
    print(f"| outcome | {na} | {nb} |\n|---|---|---|")
    for k in sa["outcomes"]:
        print(f"| {k} | {sa['outcomes'][k]} | {sb['outcomes'].get(k)} |")
    print()

    print("## Load and timing\n")
    print(f"| field | {na} | {nb} |\n|---|---|---|")
    for sec, k in [("load", "peak_in_system"), ("load", "requests"), ("load", "peak_rps"), ("load", "mean_rps"),
                   ("load", "generator_lag_p99_ms"), ("timing", "first_sale_s"), ("timing", "sold_out_s"), ("timing", "drain_s")]:
        print(f"| {sec}.{k} | {sa[sec][k]} | {sb[sec][k]} |")
    print()

    ba, bb = buckets(ra), buckets(rb)
    keys = sorted(set(ba) | set(bb))
    xs = [ba.get(k, 0.0) for k in keys]
    ys = [bb.get(k, 0.0) for k in keys]
    print(f"## rps shape (mean rps per 5 s bucket, t relative to T0); Pearson r = {corr(xs, ys):.3f}\n")
    print(f"| t (s) | {na} | {nb} |\n|---|---|---|")
    for k, x, y in zip(keys, xs, ys):
        print(f"| {k} | {x:.0f} | {y:.0f} |")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
