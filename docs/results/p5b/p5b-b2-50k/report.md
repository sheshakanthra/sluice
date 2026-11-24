# Rush p5b-b2-50k (scenario B2)

Commit `570aefc`, started 2026-11-24T13:40:15Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

> **GENERATOR-BOUND:** schedule lag p99 8650.8 ms > 100 ms. Latencies include the swarm's own delay.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 50000 | 500 | rush-1124-190930 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | true |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 375 | 0 | 49572 | 9 | 40 | **4** | 482 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 33510 | 287165 | 7764 | 2159.1 | 8650.8 ms |

## Timing (from T0 = 2026-11-24T13:40:45Z)
| first sale | sold out | drain |
|---|---|---|
| 25.4 s | n/a | 122.7 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 5161.0 | 21446.7 | 24444.9 | 42696.7 | 148381 |
| status | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| join | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| session | 4718.6 | 20971.5 | 22921.2 | 45908.0 | 99240 |
| availability | 6115.3 | 22593.5 | 25952.3 | 42663.9 | 39531 |
| hold | 5161.0 | 21479.4 | 24477.7 | 42696.7 | 145882 |
| order | 5070.8 | 17645.6 | 22544.4 | 27574.3 | 1378 |
| pay | 4845.6 | 10330.1 | 15958.0 | 20922.4 | 1121 |
| payments | 2990.1 | 5951.5 | 10068.0 | 10068.0 | 13 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 0.0 | 0.0 | 0.0 | 0.0 | 0 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 57.3510% | 12844 | 151840 | 8 | 56158 | 0 | 21 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 8650.8 ms | 11599.9 ms | 26.89 / 4.91 | 127984 | 130 (24) |

## Unexpected answers
| step:answer | count |
|---|---|
| order:409 HOLD_EXPIRED | 44 |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | 4 | 31.7 s | **false** | 1 |
