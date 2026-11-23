-- queue_claim.lua: turn an offer into an admission (DATA-MODEL §5).
-- KEYS: 1 q:soldout, 2 q:cursor, 3 q:offers, 4 q:active
-- ARGV: ticket, uid, now_ms, admission_ttl_ms
-- Returns {'ADMITTED', exp_ms} | {'OFFER_EXPIRED'} | {'SOLD_OUT'} |
--         {'WAITING'} (ticket not offered yet: the caller's snapshot is ahead)
-- Idempotent: an already-active uid gets its existing expiry back.
local ticket = ARGV[1]
local uid = ARGV[2]
local now = tonumber(ARGV[3])
if redis.call('GET', KEYS[1]) == '1' then return {'SOLD_OUT'} end
local exp = redis.call('ZSCORE', KEYS[4], uid)
if exp and tonumber(exp) > now then return {'ADMITTED', exp} end
if tonumber(ticket) > tonumber(redis.call('GET', KEYS[2]) or '0') then return {'WAITING'} end
local dl = redis.call('ZSCORE', KEYS[3], ticket)
redis.call('ZREM', KEYS[3], ticket)
if not dl or tonumber(dl) <= now then
  if exp then redis.call('ZREM', KEYS[4], uid) end
  return {'OFFER_EXPIRED'}
end
local e = now + tonumber(ARGV[4])
redis.call('ZADD', KEYS[4], e, uid)
return {'ADMITTED', e}
