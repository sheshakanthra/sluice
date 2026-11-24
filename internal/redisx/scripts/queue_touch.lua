-- queue_touch.lua: check and renew an admission's idle lease (DATA-MODEL §5,
-- ADR 0010 addendum part 3). Run on every request that presents an
-- admission token; POST /holds and POST /orders refuse a LAPSED admission
-- (403 ADMISSION_EXPIRED), so the check costs no extra round trip.
-- KEYS: 1 q:active, 2 q:init
-- ARGV: uid, now_ms, idle_ttl_ms, cap_ms (the admission token's exp)
-- Returns {'OK', renewed (1|0), 'LIVE' | 'LAPSED' | 'UNKNOWN'}
--
-- LIVE: the lease is in q:active with a score > now. LAPSED: it is gone
-- (released, left, pruned) or past (idle or capped, not yet pruned by the
-- tick); a lapsed lease is never renewed. UNKNOWN: q:init is missing, so
-- Redis lost the queue state (R1) and q:active cannot answer until
-- queue_restore runs; the token alone decides.
-- ZADD XX GT: XX never re-creates an admission, GT never shortens a lease,
-- and the lease never passes the hard cap (ADMISSION_TTL).
if redis.call('EXISTS', KEYS[2]) == 0 then return {'OK', 0, 'UNKNOWN'} end
local now = tonumber(ARGV[2])
local cur = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not cur or tonumber(cur) <= now then return {'OK', 0, 'LAPSED'} end
local lease = math.min(now + tonumber(ARGV[3]), tonumber(ARGV[4]))
return {'OK', redis.call('ZADD', KEYS[1], 'XX', 'GT', 'CH', lease, ARGV[1]), 'LIVE'}
