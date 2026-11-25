# Rush p7a-smoke (scenario SMOKE)

Commit `5138893`, started 2026-11-25T11:56:24Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-172457 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 395 | 4140 | 421 | 7 | 37 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4523 | 163345 | 3964 | 559.4 | 9.8 ms |

## Timing (from T0 = 2026-11-25T11:56:54Z)
| first sale | sold out | drain |
|---|---|---|
| 5.8 s | 265.0 s | 281.1 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 190.1 | 1925.1 | 2363.4 | 3670.0 | 1771 |
| status | 2.6 | 83.8 | 1482.8 | 2166.8 | 150762 |
| join | 12.8 | 427.5 | 1570.8 | 2807.8 | 5059 |
| session | 15.1 | 753.7 | 1837.1 | 2580.5 | 5019 |
| availability | 16.2 | 175.6 | 205.4 | 370.4 | 262 |
| hold | 80.3 | 322.8 | 503.6 | 584.7 | 892 |
| order | 1017.9 | 1688.6 | 2215.9 | 3051.5 | 402 |
| pay | 1478.7 | 2177.0 | 3080.2 | 3670.0 | 477 |
| payments | 299.5 | 610.8 | 770.6 | 770.6 | 14 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 22.9 | 113.9 | 209.4 | 339.5 | 458 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.5369% | 376 | 501 | 0 | 492 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 9.8 ms | 121.0 ms | 2.45 / 0.45 | 13648 | 311 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 20.0 s | **true** | 0 |
