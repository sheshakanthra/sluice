-- queue_touch.lua: renew an admission's idle lease (DATA-MODEL §5, ADR 0010
-- addendum). Run on every request that presents an admission token.
-- KEYS: 1 q:active
-- ARGV: uid, now_ms, idle_ttl_ms, cap_ms (the admission token's exp)
-- Returns {'OK', renewed (1|0)}
--
-- ZADD XX GT: XX never re-creates an admission that ended (released,
-- expired, left), and GT never shortens a lease. The lease never passes
-- the hard cap, so ADMISSION_TTL stays the upper bound.
local lease = math.min(tonumber(ARGV[2]) + tonumber(ARGV[3]), tonumber(ARGV[4]))
if lease <= tonumber(ARGV[2]) then return {'OK', 0} end
return {'OK', redis.call('ZADD', KEYS[1], 'XX', 'GT', 'CH', lease, ARGV[1])}
