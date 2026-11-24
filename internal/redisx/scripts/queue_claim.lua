-- queue_claim.lua: turn an offer into an admission (DATA-MODEL §5).
-- KEYS: 1 q:soldout, 2 q:cursor, 3 q:offers, 4 q:active, 5 q:cap
-- ARGV: ticket, uid, now_ms, admission_ttl_ms, idle_ttl_ms
-- Returns {'ADMITTED', cap_ms, new (1|0)} | {'OFFER_EXPIRED'} | {'SOLD_OUT'} |
--         {'WAITING'} (ticket not offered yet: the caller's snapshot is ahead)
--
-- An admission is a sliding lease (ADR 0010 addendum): q:active holds the
-- lease expiry min(now + idle_ttl, cap), renewed by queue_touch on every
-- request that presents the admission token; q:cap holds the hard cap
-- (claim + admission_ttl), which is also the admission token's exp.
-- Idempotent: an already-active uid gets its existing cap back (new = 0).
-- After a Redis restore q:cap may be missing; the restored lease expiry is
-- returned instead, so a restore never extends anyone's cap.
local ticket = ARGV[1]
local uid = ARGV[2]
local now = tonumber(ARGV[3])
if redis.call('GET', KEYS[1]) == '1' then return {'SOLD_OUT'} end
local exp = redis.call('ZSCORE', KEYS[4], uid)
if exp and tonumber(exp) > now then
  local cap = redis.call('ZSCORE', KEYS[5], uid) or exp
  return {'ADMITTED', cap, 0}
end
if tonumber(ticket) > tonumber(redis.call('GET', KEYS[2]) or '0') then return {'WAITING'} end
local dl = redis.call('ZSCORE', KEYS[3], ticket)
redis.call('ZREM', KEYS[3], ticket)
if not dl or tonumber(dl) <= now then
  if exp then redis.call('ZREM', KEYS[4], uid) end
  return {'OFFER_EXPIRED'}
end
local cap = now + tonumber(ARGV[4])
local lease = math.min(now + tonumber(ARGV[5]), cap)
redis.call('ZADD', KEYS[4], lease, uid)
redis.call('ZADD', KEYS[5], cap, uid)
return {'ADMITTED', cap, 1}
