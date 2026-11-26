# Rush p7b-head-3 (scenario SMOKE)

Commit `9a8cadb`, started 2026-11-25T16:07:53Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-213659 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 385 | 4154 | 421 | 3 | 37 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4453 | 113882 | 3409 | 581.0 | 13.1 ms |

## Timing (from T0 = 2026-11-25T16:08:23Z)
| first sale | sold out | drain |
|---|---|---|
| 4.5 s | 171.0 s | 185.2 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 347.6 | 1709.1 | 2465.8 | 3338.2 | 1732 |
| status | 2.2 | 231.2 | 939.0 | 1586.2 | 101422 |
| join | 7.3 | 417.8 | 1009.7 | 1385.5 | 5000 |
| session | 8.7 | 501.8 | 1008.1 | 1367.0 | 5000 |
| availability | 32.5 | 200.1 | 276.7 | 347.1 | 257 |
| hold | 134.7 | 531.5 | 683.0 | 862.7 | 893 |
| order | 873.5 | 1467.4 | 2680.8 | 3338.2 | 388 |
| pay | 1322.0 | 2261.0 | 2768.9 | 3319.8 | 451 |
| payments | 248.4 | 756.2 | 810.0 | 810.0 | 13 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 24.5 | 179.6 | 241.3 | 290.3 | 458 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0000% | 0 | 0 | 0 | 499 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 13.1 ms | 144.8 ms | 2.52 / 0.37 | 13415 | 215 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 20.7 s | **true** | 0 |
