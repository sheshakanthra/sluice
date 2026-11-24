# Rush p5b-b1 (scenario B1)

Commit `570aefc`, started 2026-11-24T13:35:38Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 10000 | 500 | rush-1124-190453 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | true |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 344 | 0 | 9618 | 1 | 37 | **0** | 447 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 7449 | 23986 | 3651 | 999.4 | 25.9 ms |

## Timing (from T0 = 2026-11-24T13:36:08Z)
| first sale | sold out | drain |
|---|---|---|
| 3.7 s | n/a | 13.9 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 18.9 | 322.0 | 462.1 | 3229.7 | 10948 |
| status | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| join | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| session | 2.9 | 11.1 | 37.9 | 144.5 | 10000 |
| availability | 2.1 | 34.5 | 71.5 | 111.0 | 3029 |
| hold | 17.5 | 322.3 | 453.6 | 587.8 | 10209 |
| order | 82.4 | 157.1 | 2093.1 | 2117.6 | 345 |
| pay | 180.5 | 3139.6 | 3213.3 | 3229.7 | 394 |
| payments | 87.5 | 93.5 | 93.5 | 93.5 | 9 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 0.0 | 0.0 | 0.0 | 0.0 | 0 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0542% | 13 | 0 | 0 | 9827 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 25.9 ms | 27.0 ms | 1.54 / 0.47 | 24619 | 43 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 27 | 0 | 0 | 0 | 0 | 0 | n/a | 0 | 32.2 s | **false** | 1 |
