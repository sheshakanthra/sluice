# Rush p7b-ctl-2 (scenario SMOKE)

Commit `5138893`, started 2026-11-25T16:03:01Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-213200 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 387 | 4158 | 414 | 5 | 36 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4284 | 113982 | 3585 | 572.8 | 11.4 ms |

## Timing (from T0 = 2026-11-25T16:03:31Z)
| first sale | sold out | drain |
|---|---|---|
| 4.5 s | 171.0 s | 188.8 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 106.4 | 1300.5 | 2457.6 | 3092.5 | 1719 |
| status | 2.1 | 178.6 | 896.0 | 1786.9 | 101556 |
| join | 6.4 | 332.3 | 859.6 | 1400.8 | 5000 |
| session | 7.2 | 550.4 | 1029.1 | 1546.2 | 5000 |
| availability | 7.0 | 73.9 | 120.8 | 167.0 | 251 |
| hold | 45.9 | 145.4 | 225.2 | 333.1 | 868 |
| order | 632.3 | 1264.6 | 2474.0 | 2945.0 | 392 |
| pay | 837.1 | 1727.5 | 2662.4 | 3092.5 | 459 |
| payments | 348.2 | 515.8 | 515.8 | 515.8 | 6 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 24.2 | 119.6 | 185.3 | 228.6 | 450 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0000% | 0 | 0 | 0 | 471 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 11.4 ms | 153.1 ms | 2.18 / 0.37 | 13073 | 218 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 17.6 s | **true** | 0 |
