# Rush p7b-head-2 (scenario SMOKE)

Commit `9a8cadb`, started 2026-11-25T15:56:33Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-212532 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 395 | 4135 | 428 | 3 | 39 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4332 | 160299 | 3392 | 560.5 | 8.3 ms |

## Timing (from T0 = 2026-11-25T15:57:03Z)
| first sale | sold out | drain |
|---|---|---|
| 4.5 s | 259.0 s | 276.0 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 99.1 | 1056.8 | 2240.5 | 2736.1 | 1735 |
| status | 2.1 | 17.6 | 885.8 | 1626.1 | 147744 |
| join | 6.1 | 548.4 | 1105.9 | 1453.1 | 5069 |
| session | 6.7 | 629.2 | 1070.1 | 1472.5 | 5031 |
| availability | 5.8 | 45.2 | 92.8 | 108.7 | 245 |
| hold | 41.1 | 140.4 | 214.5 | 346.1 | 882 |
| order | 470.3 | 956.9 | 2422.8 | 2736.1 | 398 |
| pay | 714.2 | 1222.7 | 2289.7 | 2732.0 | 455 |
| payments | 214.8 | 492.5 | 492.5 | 492.5 | 8 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 8.0 | 47.0 | 76.6 | 155.5 | 467 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0624% | 100 | 0 | 0 | 468 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 8.3 ms | 97.6 ms | 2.13 / 0.36 | 13270 | 304 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 19.5 s | **true** | 0 |
