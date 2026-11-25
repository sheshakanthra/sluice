# Rush p7a-smoke-oldcaddy (scenario SMOKE)

Commit `5138893`, started 2026-11-25T12:10:21Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-173915 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 382 | 4143 | 430 | 6 | 39 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4506 | 126142 | 3135 | 560.6 | 12.9 ms |

## Timing (from T0 = 2026-11-25T12:10:51Z)
| first sale | sold out | drain |
|---|---|---|
| 4.5 s | 199.0 s | 214.2 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 109.2 | 1872.9 | 2637.8 | 3352.6 | 1708 |
| status | 2.4 | 97.2 | 1118.2 | 2050.0 | 113501 |
| join | 8.1 | 600.6 | 1419.3 | 1889.3 | 5132 |
| session | 9.0 | 723.5 | 1207.3 | 1691.6 | 5075 |
| availability | 4.8 | 69.7 | 257.8 | 418.3 | 251 |
| hold | 38.8 | 281.3 | 563.7 | 940.0 | 876 |
| order | 1095.7 | 2033.7 | 2908.2 | 3352.6 | 388 |
| pay | 952.3 | 2428.9 | 2861.1 | 3319.8 | 444 |
| payments | 137.6 | 511.7 | 511.7 | 511.7 | 6 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 8.3 | 206.7 | 332.3 | 468.5 | 469 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.2188% | 207 | 69 | 0 | 475 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 12.9 ms | 132.4 ms | 1.78 / 0.43 | 13550 | 243 (1) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 20.6 s | **true** | 0 |
