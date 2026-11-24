# Rush p5b-b2-20k (scenario B2)

Commit `570aefc`, started 2026-11-24T13:44:24Z. Runner `local`: 16 CPUs, 9.7 GB RAM, go1.27.4, Postgres 18.7, Redis 8.10.3.

The swarm shares CPUs with the system under test, so these numbers are conservative.

> **GENERATOR-BOUND:** schedule lag p99 137.3 ms > 100 ms. Latencies include the swarm's own delay.

## Config
| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |
|---|---|---|---|---|---|---|---|---|---|
| 20000 | 500 | rush-1124-191344 | dedicated | `pre=0.7:-10s..0s,post=0.3:0s..10s` | 30 s | 60 s | 90 s | 3 | true |

## Outcomes
| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |
|---|---|---|---|---|---|---|
| 370 | 0 | 19593 | 0 | 33 | **4** | 460 |

## Load
| peak in system | requests | peak rps | mean rps | generator lag p99 |
|---|---|---|---|---|
| 14984 | 113637 | 13296 | 1721.8 | 137.3 ms |

## Timing (from T0 = 2026-11-24T13:44:54Z)
| first sale | sold out | drain |
|---|---|---|
| 20.2 s | n/a | 55.4 s |

## Latency (ms, client-side, from intended start)
| route | p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|
| checkout | 2345.0 | 5103.6 | 5279.7 | 5799.9 | 60128 |
| status | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| join | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| session | 37.3 | 5038.1 | 5189.6 | 5709.8 | 38147 |
| availability | 2113.5 | 5132.3 | 5304.3 | 5668.9 | 15352 |
| hold | 2359.3 | 5107.7 | 5283.8 | 5799.9 | 57395 |
| order | 2308.1 | 5029.9 | 5148.7 | 5554.2 | 1775 |
| pay | 1417.2 | 5005.3 | 5017.6 | 5050.4 | 958 |
| payments | 606.2 | 2469.9 | 2469.9 | 2469.9 | 10 |
| confirm | 0.0 | 0.0 | 0.0 | 0.0 | 0 |
| leave | 0.0 | 0.0 | 0.0 | 0.0 | 0 |

## Errors and business answers
| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |
|---|---|---|---|---|---|---|
| 57.2789% | 39178 | 25912 | 0 | 21356 | 0 | 5 |

## Generator self-check
| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |
|---|---|---|---|---|
| 137.3 ms | 484.4 ms | 3.81 / 0.76 | 50264 | 75 (13) |

## Audit (normal mode; details in audit.json)
| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | 4 | 35.7 s | **false** | 1 |
