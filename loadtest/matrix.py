"""Baseline matrix for a phase report, read only from summary.json files.

usage: python loadtest/matrix.py <run_id> [<run_id> ...]
Prints a markdown table; every cell is a value from
loadtest/results/<run_id>/summary.json (n/a where the value is null).
"""
import json
import sys


def g(d, *path):
    for p in path:
        if d is None:
            return None
        d = d.get(p)
    return d


def f(v, fmt="{}"):
    if v is None:
        return "n/a"
    return fmt.format(v)


COLS = [
    ("run", lambda s: s["run_id"]),
    ("scenario", lambda s: s["scenario"]),
    ("users", lambda s: g(s, "config", "users")),
    ("TICKET", lambda s: g(s, "outcomes", "TICKET")),
    ("UNRESOLVED", lambda s: g(s, "outcomes", "UNRESOLVED")),
    ("oversold", lambda s: g(s, "audit", "oversold")),
    ("duplicate charges", lambda s: g(s, "audit", "duplicate_charges")),
    ("orphan charges", lambda s: g(s, "audit", "orphan_charges")),
    ("quota", lambda s: g(s, "audit", "quota_violations")),
    ("stuck holds", lambda s: g(s, "audit", "stuck_holds")),
    ("drift", lambda s: g(s, "audit", "inventory_drift")),
    ("ticket/order", lambda s: g(s, "audit", "ticket_order_consistency")),
    ("admitted after sold out", lambda s: g(s, "audit", "admitted_after_soldout")),
    ("audit exit", lambda s: g(s, "audit", "exit_code")),
    ("checkout p95 ms", lambda s: f(g(s, "headline", "checkout_p95_ms"), "{:.1f}")),
    ("error rate", lambda s: f(g(s, "errors", "rate"), "{:.4%}")),
    ("peak in system", lambda s: g(s, "load", "peak_in_system")),
    ("gen lag p99 ms", lambda s: f(g(s, "load", "generator_lag_p99_ms"), "{:.1f}")),
]


def main():
    rows = []
    for rid in sys.argv[1:]:
        with open(f"loadtest/results/{rid}/summary.json", encoding="utf-8") as fh:
            s = json.load(fh)
        rows.append([f(fn(s)) for _, fn in COLS])
    print("| " + " | ".join(c for c, _ in COLS) + " |")
    print("|" + "---|" * len(COLS))
    for r in rows:
        print("| " + " | ".join(r) + " |")


if __name__ == "__main__":
    main()
