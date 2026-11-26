# Rush p7b-head-1 (scenario SMOKE)

Commit `9a8cadb`, started 2026-11-25T15:46:58Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-211557 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 404 | 4132 | 417 | 8 | 39 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4343 | 126650 | 3417 | 560.4 | 20.1 ms |

## Timing (from T0 = 2026-11-25T15:47:28Z)
| first sale | sold out | drain |
|---|---|---|
| 4.7 s | 200.0 s | 215.4 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 165.5 | 1232.9 | 2314.2 | 3201.0 | 1781 |
| status | 2.7 | 170.0 | 1616.9 | 2136.1 | 113836 |
| join | 8.3 | 588.3 | 1503.2 | 2650.1 | 5180 |
| session | 9.4 | 1519.6 | 1978.4 | 2615.3 | 5144 |
| availability | 10.8 | 145.4 | 265.7 | 312.8 | 246 |
| hold | 72.3 | 275.5 | 470.5 | 718.8 | 894 |
| order | 620.5 | 965.6 | 2646.0 | 2869.2 | 412 |
| pay | 950.3 | 1516.5 | 2502.7 | 3201.0 | 475 |
| payments | 260.0 | 660.5 | 660.5 | 660.5 | 7 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 12.7 | 83.5 | 121.1 | 284.2 | 456 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.5030% | 327 | 310 | 0 | 468 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 20.1 ms | 125.6 ms | 2.55 / 0.46 | 13270 | 245 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 19.7 s | **true** | 0 |
