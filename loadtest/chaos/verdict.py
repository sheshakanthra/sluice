"""One row of the chaos results table from a run directory (summary.json,
timeseries.csv). Usage:
    python loadtest/chaos/verdict.py --header
    python loadtest/chaos/verdict.py <ID> <run_dir> <exit_code> <fault_note> [fault_t_seconds]
Pass rules are documented in loadtest/chaos/run.sh."""
import csv
import json
import os
import sys

COLS = ["case", "run", "oversold", "dup charges", "orphans", "quota", "stuck holds", "drift",
        "unresolved", "TICKET", "error rate (run)", "errors T-2..T+10 s / requests", "peak err/s", "swarm+audit exit", "verdict"]

if sys.argv[1] == "--header":
    print("| " + " | ".join(COLS) + " |")
    print("|" + "---|" * len(COLS))
    sys.exit(0)

cid, run_dir, code, note = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
fault_t = int(sys.argv[5]) if len(sys.argv) > 5 else None
run = os.path.basename(run_dir)
try:
    s = json.load(open(os.path.join(run_dir, "summary.json"), encoding="utf-8"))
except OSError:
    print(f"| {cid} | {run} | " + " | ".join(["n/a"] * (len(COLS) - 4)) + f" | {code} | FAIL |")
    sys.exit(0)
a = s.get("audit") or {}
g = lambda k: a.get(k)

win_err = win_req = peak = 0
with open(os.path.join(run_dir, "timeseries.csv"), encoding="utf-8") as f:
    for r in csv.DictReader(f):
        t, e, n = int(r["t"]), int(r["errors"] or 0), int(r["rps_total"] or 0)
        peak = max(peak, e)
        if fault_t is not None and fault_t - 2 <= t <= fault_t + 10:
            win_err += e
            win_req += n

core = [g("oversold"), g("duplicate_charges"), g("orphan_charges"), g("quota_violations")]
ok = all(v == 0 for v in core)
if cid == "C1":
    ok = ok and g("unresolved_users") == 0
if cid == "C4":
    ok = ok and g("stuck_holds") == 0
window = f"{win_err} / {win_req}" if fault_t is not None else "n/a (whole run)"
tickets = (s.get("outcomes") or {}).get("TICKET")
row = [cid, run] + [str(v) for v in core] + [str(g("stuck_holds")), str(g("inventory_drift")), str(g("unresolved_users")),
       str(tickets), f"{s['errors']['rate'] * 100:.4f}%", window, str(peak), str(code), "PASS" if ok else "FAIL"]
print("| " + " | ".join(row) + " |")
