# Rush p5b-b2 (scenario B2)

Commit `570aefc`, started 2026-11-24T13:37:55Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 10000 | 500 | rush-1124-190709 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | true |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 341 | 0 | 9625 | 1 | 33 | **0** | 451 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 7425 | 23955 | 3702 | 998.1 | 16.9 ms |

## Timing (from T0 = 2026-11-24T13:38:25Z)
| first sale | sold out | drain |
|---|---|---|
| 3.6 s | n/a | 13.9 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 17.3 | 266.8 | 382.2 | 2189.3 | 10917 |
| status | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| join | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| session | 2.8 | 9.1 | 33.0 | 92.7 | 10000 |
| availability | 2.0 | 19.1 | 52.6 | 108.6 | 3029 |
| hold | 16.5 | 269.1 | 377.3 | 493.3 | 10175 |
| order | 82.0 | 135.9 | 2119.7 | 2158.6 | 342 |
| pay | 124.0 | 216.2 | 2111.5 | 2189.3 | 400 |
| payments | 66.4 | 90.5 | 90.5 | 90.5 | 9 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 0.0 | 0.0 | 0.0 | 0.0 | 0 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0000% | 0 | 0 | 0 | 9823 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 16.9 ms | 17.7 ms | 1.38 / 0.48 | 24627 | 43 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | 0 | 32.6 s | **true** | 0 |
