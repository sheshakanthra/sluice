# Rush p5b-smoke (scenario SMOKE)

Commit `570aefc`, started 2026-11-24T13:27:05Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 5000 | 500 | rush-1124-185604 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | false |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 376 | 4128 | 452 | 7 | 37 | **0** | 500 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 4253 | 148846 | 7001 | 579.2 | 1.5 ms |

## Timing (from T0 = 2026-11-24T13:27:35Z)
| first sale | sold out | drain |
|---|---|---|
| 3.7 s | 231.0 s | 246.9 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 35.1 | 329.7 | 2125.8 | 2242.6 | 1712 |
| status | 2.0 | 3.7 | 10.1 | 127.5 | 136375 |
| join | 2.9 | 13.3 | 35.4 | 106.3 | 5000 |
| session | 3.2 | 17.2 | 37.8 | 66.0 | 5000 |
| availability | 2.8 | 9.3 | 24.4 | 55.7 | 260 |
| hold | 21.2 | 40.4 | 70.6 | 146.3 | 896 |
| order | 150.7 | 316.9 | 2144.3 | 2234.4 | 383 |
| pay | 215.6 | 462.6 | 2166.8 | 2242.6 | 433 |
| payments | 109.4 | 177.2 | 177.2 | 177.2 | 10 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 4.5 | 14.3 | 30.8 | 48.2 | 489 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 0.0000% | 0 | 0 | 0 | 494 | 0 | 0 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 1.5 ms | 32.5 ms | 1.85 / 0.36 | 12973 | 275 (0) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 19.5 s | **true** | 0 |
