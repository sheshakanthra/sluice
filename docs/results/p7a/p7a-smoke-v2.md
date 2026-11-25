# Rush p7a-smoke-v2 (scenario SMOKE)

Commit `5138893`, started 2026-11-25T12:05:30Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-173406 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 383 | 4165 | 417 | 4 | 31 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4297 | 97484 | 3751 | 590.8 | 6.9 ms |

## Timing (from T0 = 2026-11-25T12:06:00Z)
| first sale | sold out | drain |
|---|---|---|
| 5.2 s | 140.0 s | 154.9 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 220.8 | 823.8 | 2295.8 | 2875.4 | 1692 |
| status | 2.6 | 259.8 | 1257.5 | 2004.0 | 85075 |
| join | 7.3 | 378.1 | 866.3 | 1532.9 | 5011 |
| session | 8.1 | 796.7 | 1275.9 | 1687.6 | 5003 |
| availability | 19.9 | 224.4 | 408.3 | 466.4 | 241 |
| hold | 87.6 | 350.0 | 558.6 | 699.9 | 859 |
| order | 354.0 | 667.6 | 2328.6 | 2476.0 | 387 |
| pay | 525.8 | 1162.2 | 2441.2 | 2875.4 | 446 |
| payments | 337.4 | 405.8 | 461.3 | 461.3 | 14 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 33.2 | 171.4 | 280.6 | 438.5 | 448 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0154% | 14 | 1 | 0 | 461 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 6.9 ms | 72.8 ms | 2.28 / 0.45 | 13000 | 184 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 19.6 s | **true** | 0 |
