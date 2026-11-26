# Rush p7b-ctl-1 (scenario SMOKE)

Commit `5138893`, started 2026-11-25T15:52:40Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1125-212127 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 387 | 4162 | 410 | 1 | 40 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4298 | 80155 | 4022 | 611.9 | 12.1 ms |

## Timing (from T0 = 2026-11-25T15:53:10Z)
| first sale | sold out | drain |
|---|---|---|
| 5.0 s | 106.0 s | 120.6 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 130.2 | 842.8 | 2162.7 | 2846.7 | 1709 |
| status | 2.7 | 426.5 | 1256.4 | 2007.0 | 67739 |
| join | 9.0 | 605.7 | 1178.6 | 1927.2 | 5000 |
| session | 10.1 | 975.4 | 1299.5 | 1671.2 | 5000 |
| availability | 8.1 | 100.9 | 138.5 | 166.3 | 242 |
| hold | 57.4 | 217.0 | 301.6 | 452.6 | 861 |
| order | 443.4 | 764.9 | 2342.9 | 2578.4 | 388 |
| pay | 664.1 | 971.8 | 2238.5 | 2846.7 | 460 |
| payments | 317.4 | 447.5 | 552.4 | 552.4 | 15 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 24.3 | 128.6 | 201.7 | 264.4 | 450 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0025% | 0 | 2 | 0 | 462 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 12.1 ms | 69.4 ms | 2.24 / 0.49 | 13109 | 150 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 20.5 s | **true** | 0 |
