# Rush p5b-b0 (scenario B0)

Commit `570aefc`, started 2026-11-24T13:33:01Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 10000 | 500 | rush-1124-190215 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | true |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 450 | 0 | 0 | 8730 | 820 | **0** | 0 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 8007 | 42744 | 4332 | 1424.8 | 18.4 ms |

## Timing (from T0 = 2026-11-24T13:33:31Z)
| first sale | sold out | drain |
|---|---|---|
| 3.2 s | n/a | 19.8 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 8.1 | 57.0 | 136.1 | 3221.5 | 29706 |
| status | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| join | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| session | 2.9 | 19.7 | 44.0 | 83.6 | 10000 |
| availability | 2.0 | 22.4 | 46.1 | 73.5 | 3029 |
| hold | 9.0 | 64.7 | 96.8 | 162.8 | 10775 |
| order | 3.4 | 22.4 | 46.2 | 94.9 | 9180 |
| pay | 9.9 | 98.4 | 190.2 | 3221.5 | 9751 |
| payments | 16.2 | 30.5 | 30.5 | 30.5 | 9 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 0.0 | 0.0 | 0.0 | 0.0 | 0 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0421% | 18 | 0 | 0 | 775 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 18.4 ms | 20.5 ms | 1.46 / 0.53 | 25759 | 49 (0) |

## Audit (naive mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 17 | 19 | 0 | 0 | n/a | n/a | 0 | n/a | 0 | 30.2 s | **false** | 1 |
