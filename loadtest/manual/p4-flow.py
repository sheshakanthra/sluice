"""P4 manual check: open-sale -> 2 sessions -> PREQUEUE -> WAITING -> ADMITTED
-> hold -> order -> pay -> ticket, with /stats printed along the way.

Run against `task dev:api`, `task dev:worker`, `task dev:mockpay`. To see a
user WAITING with only two users, run the worker with an admission target of
one: QUEUE_MAX_ACTIVE=1 QUEUE_OVERADMIT=1 QUEUE_MIN_ADMIT=1
QUEUE_MAX_ADMIT_PER_TICK=1. User A is offered first; B waits until A holds
seats (A then counts as holding, not seeking, which frees the slot).

usage: python loadtest/manual/p4-flow.py [event-slug] [opens_in_s]
Environment: API (default http://localhost:8080), ADMIN_TOKEN.
Python standard library only; prints every state transition it sees.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid

API = os.environ.get("API", "http://localhost:8080").rstrip("/") + "/api/v1"
ADMIN = os.environ.get("ADMIN_TOKEN", "dev-admin-token-change-me")
EVENT = sys.argv[1] if len(sys.argv) > 1 else "tatkal-chn-sbc"
OPENS_IN = int(sys.argv[2]) if len(sys.argv) > 2 else 30
T_START = time.monotonic()


def call(method, path, token=None, body=None, idem=False):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if idem:
        req.add_header("Idempotency-Key", str(uuid.uuid4()))
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")


def log(who, msg):
    print(f"[{time.monotonic() - T_START:6.2f}s] {who}: {msg}", flush=True)


def must(status, body, want, what):
    if status != want:
        sys.exit(f"{what}: HTTP {status} {body}")
    return body


def stats(label):
    _, s = call("GET", f"/stats/{EVENT}")
    log("stats", f"{label}: status={s['event_status']} queue={s['queue']} inventory={s['inventory']} "
                 f"correctness={s['correctness']}")


def main():
    stats("before open-sale")
    r = must(*call("POST", f"/admin/events/{EVENT}/open-sale", ADMIN, {"opens_in_s": OPENS_IN}), 200, "open-sale")
    log("admin", f"open-sale -> T0 {r['sale_opens_at']} ({OPENS_IN} s)")

    users = {}
    for name in ("A", "B"):
        s = must(*call("POST", "/sessions"), 201, "session")
        users[name] = {"session": s["session_token"], "uid": s["user_id"], "state": None}
        log(name, f"session {s['user_id']}")
    for name, u in users.items():
        j = must(*call("POST", f"/events/{EVENT}/queue", u["session"]), 200, "join")
        u["queue"] = j["queue_token"]
        u["state"] = j["state"]
        log(name, f"join -> {j['state']} ticket={j['ticket_no']} retry_after_ms={j['retry_after_ms']}")
    stats("after 2 joins")

    seen_waiting = set()
    held = False
    deadline = time.monotonic() + OPENS_IN + 120
    while time.monotonic() < deadline:
        for name, u in users.items():
            if u["state"] == "ADMITTED":
                continue
            code, st = call("GET", f"/events/{EVENT}/queue/status", u["queue"])
            if code == 429:
                continue  # our own status limiter (3/s, burst 6)
            must(code, st, 200, "status")
            if st.get("queue_token"):
                u["queue"] = st["queue_token"]
            if st["state"] != u["state"]:
                extra = f"ticket={st['ticket_no']}"
                if st["state"] == "WAITING":
                    extra += f" position={st.get('position')} ahead={st.get('ahead')} outlook={st.get('outlook')}"
                    seen_waiting.add(name)
                log(name, f"{u['state']} -> {st['state']} {extra}")
                u["state"] = st["state"]
                if st["state"] == "ADMITTED":
                    u["admission"] = st["admission_token"]
                    stats(f"{name} admitted")
        a = users["A"] if users["A"]["state"] == "ADMITTED" else users["B"] if users["B"]["state"] == "ADMITTED" else None
        if a and not held:
            first = "A" if a is users["A"] else "B"
            buy(first, a)
            held = True
        if all(u["state"] == "ADMITTED" for u in users.values()):
            break
        time.sleep(0.5)
    else:
        sys.exit("timed out waiting for both users to be admitted")
    second = "B" if users["A"].get("bought") else "A"
    buy(second, users[second])
    stats("end")
    log("check", f"users seen WAITING: {sorted(seen_waiting) or 'none'}")


def buy(name, u):
    h = must(*call("POST", f"/events/{EVENT}/holds", u["admission"], {"quantity": 2}, idem=True), 201, "hold")
    log(name, f"hold {h['hold_id']} seats={h['labels']} expires={h['expires_at']}")
    stats(f"{name} holding")
    o = must(*call("POST", "/orders", u["admission"], {"hold_id": h["hold_id"]}, idem=True), 201, "order")
    log(name, f"order {o['order_id']} {o['status']} amount_paise={o['amount_paise']} payment={o['payment']['status']}")
    p = must(*call("POST", f"/orders/{o['order_id']}/pay", u["session"], {"payment_method": "pm_ok"}, idem=True),
             200, "pay")
    for _ in range(20):
        if p["status"] != "PENDING_PAYMENT":
            break
        time.sleep(0.5)
        p = must(*call("POST", f"/orders/{o['order_id']}/confirm", u["session"], {}, idem=True), 200, "confirm")
    log(name, f"pay -> {p['status']} charged={p['charged']} tickets={[(t['label'], t['code']) for t in p['tickets']]}")
    u["bought"] = True


if __name__ == "__main__":
    main()
