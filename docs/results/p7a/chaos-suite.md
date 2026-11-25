# Chaos suite 20261125-121455 (users 5000, seats 500, commit 5138893)

| case | run | oversold | dup charges | orphans | quota | stuck holds | drift | unresolved | TICKET | error rate (run) | errors T-2..T+10 s / requests | peak err/s | swarm+audit exit | verdict |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| C1 | chaos-c1-20261125-121455 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 379 | 1.7907% | 0 / 7184 | 947 | 0 | PASS |
| C2 | chaos-c2-20261125-121455 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 377 | 0.8328% | 202 / 7829 | 425 | 0 | PASS |
| C3 | chaos-c3-20261125-121455 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 390 | 2.0798% | n/a (whole run) | 908 | 0 | PASS |
| C4 | chaos-c4-20261125-121455 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 392 | 1.1100% | 9 / 7164 | 437 | 0 | PASS |

## Faults
- C1: redis-cli FLUSHALL at T0+15s; swarm log loadtest/results/chaos-c1-20261125-121455.log
  - worker log: 0 lines mentioning rebuild/restore
- C2: docker compose kill api2 at T0+10s (left down); swarm log loadtest/results/chaos-c2-20261125-121455.log
  - caddy log: 0 lines "no upstreams available", 98 health-check lines naming api2
- C3: mockpay +2000 ms on 30% of connections, timeout(3000 ms) on 5%, whole run; swarm log loadtest/results/chaos-c3-20261125-121455.log
- C4: docker compose pause postgres for 3 s at T0+20s; swarm log loadtest/results/chaos-c4-20261125-121455.log
