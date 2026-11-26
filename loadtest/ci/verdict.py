#!/usr/bin/env python3
"""CI gate for one rush run (LOADTEST §7, ADR 0015).

usage: verdict.py <results_dir> <scenario> <rush_exit_code>

Prints a Markdown verdict section (appended to $GITHUB_STEP_SUMMARY) and
exits 0 when the run meets its scenario's rule, 1 otherwise:

- H, SMOKE, B2 (the correct system): every invariant check is 0, unresolved
  users 0, the audit settled and passed, and `sluice rush --audit` exited 0.
- B0, B1 (naive baselines that exist to show the bugs): the auditor must
  detect the expected violations (B0: oversold > 0 and duplicate charges > 0;
  B1: oversold = 0 and duplicate charges > 0), and unresolved users must be 0.
  A baseline that shows no bug means the auditor is blind, so it fails.

Latency SLOs are goals, not gates (SPEC §7): they are reported, never fail CI.
Reads only summary.json; it never changes anything in the repo.
"""

import json
import os
import sys

INVARIANTS = [
    "oversold",
    "duplicate_charges",
    "orphan_charges",
    "quota_violations",
    "stuck_holds",
    "inventory_drift",
    "ticket_order_consistency",
    "admitted_after_soldout",
    "unresolved_users",
]

SLOS = [  # (label, summary path, target, unit)
    ("Error rate", ("errors", "rate"), 0.001, "ratio"),
    ("Checkout p95", ("latency_ms", "checkout", "p95"), 250, "ms"),
    ("Queue status p95", ("latency_ms", "status", "p95"), 50, "ms"),
    ("Queue join p95", ("latency_ms", "join", "p95"), 100, "ms"),
    ("Drain", ("timing", "drain_s"), 300, "s"),
]


def get(d, path):
    for k in path:
        if not isinstance(d, dict):
            return None
        d = d.get(k)
    return d


def main() -> int:
    if len(sys.argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    results, scenario, rush_exit = sys.argv[1], sys.argv[2].upper(), int(sys.argv[3])
    path = os.path.join(results, "summary.json")
    print(f"## CI verdict ({scenario})\n")
    if not os.path.exists(path):
        print(f"**FAIL**: `{path}` was not written (rush exit {rush_exit}).")
        return 1
    with open(path, encoding="utf-8") as f:
        s = json.load(f)
    a = s.get("audit") or {}
    reasons = []

    print("| check | value |\n|---|---|")
    for k in INVARIANTS:
        print(f"| {k} | {a.get(k)} |")
    print(f"| audit settled | {not a.get('settle_timed_out', True)} ({a.get('settle_s')} s) |")
    print(f"| audit passed | {a.get('passed')} |")
    print(f"| rush exit code | {rush_exit} |\n")

    unresolved = get(s, ("outcomes", "UNRESOLVED"))
    if unresolved != 0 or a.get("unresolved_users") not in (0, None):
        reasons.append(f"unresolved users: outcomes={unresolved}, audit={a.get('unresolved_users')}")
    if a.get("settle_timed_out", True):
        reasons.append("audit did not settle")

    if scenario in ("H", "SMOKE", "B2"):
        direct = bool(get(s, ("config", "direct")))
        for k in INVARIANTS:
            v = a.get(k)
            # null = check not applicable; only admitted_after_soldout may be
            # null, and only in direct mode (no waiting room, B2).
            if v is None and k == "admitted_after_soldout" and direct:
                continue
            if v is None or v != 0:
                reasons.append(f"{k} = {v}")
        if not a.get("passed"):
            reasons.append("audit passed = false")
        if rush_exit != 0:
            reasons.append(f"sluice rush exited {rush_exit}")
    elif scenario == "B0":
        if not (a.get("oversold") or 0) > 0:
            reasons.append("B0 expects oversold > 0 (auditor sensitivity)")
        if not (a.get("duplicate_charges") or 0) > 0:
            reasons.append("B0 expects duplicate charges > 0 (auditor sensitivity)")
    elif scenario == "B1":
        if a.get("oversold") != 0:
            reasons.append(f"B1 expects oversold = 0, got {a.get('oversold')}")
        if not (a.get("duplicate_charges") or 0) > 0:
            reasons.append("B1 expects duplicate charges > 0 (auditor sensitivity)")
    else:
        reasons.append(f"unknown scenario {scenario}")

    print("**SLOs** (goals, reported, never gating; SPEC §7):\n")
    print("| SLO | target | measured | met |\n|---|---|---|---|")
    for label, p, target, unit in SLOS:
        v = get(s, p)
        shown = f"{v * 100:.4f}%" if unit == "ratio" and isinstance(v, (int, float)) else f"{v} {unit}"
        tgt = f"< {target * 100:g}%" if unit == "ratio" else f"<= {target} {unit}"
        met = "n/a" if v is None else ("yes" if (v < target if unit == "ratio" else v <= target) else "no")
        print(f"| {label} | {tgt} | {shown} | {met} |")
    gen = s.get("generator") or {}
    print(f"\nGenerator lag p99 {gen.get('lag_p99_ms')} ms, generator-bound: {gen.get('generator_bound')}.")
    print(f"Peak in system {get(s, ('load', 'peak_in_system'))} (H needs >= 49,000 for the concurrency claim).\n")

    if reasons:
        print("**FAIL**:\n")
        for r in reasons:
            print(f"- {r}")
        return 1
    print("**PASS**")
    return 0


if __name__ == "__main__":
    sys.exit(main())
