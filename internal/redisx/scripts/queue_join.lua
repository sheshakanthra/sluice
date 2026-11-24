-- queue_join.lua: join an event's waiting room (DATA-MODEL §5).
-- KEYS: 1 q:user:<uid>, 2 q:pre, 3 q:presize, 4 q:tail, 5 q:soldout,
--       6 q:cursor, 7 q:offers, 8 q:active, 9 q:init, 10 q:opens
-- ARGV: uid, now_ms, sale_opens_ms, rand_score, rejoin (0|1), max_rejoins
-- Returns {'PRE'} | {'TICKET', n} | {'SOLD_OUT'} | {'REJOIN_LIMIT'} | {'NOT_INIT'}
--
-- NOT_INIT: q:init is missing (a fresh event, or Redis lost its keys). The
-- caller restores the queue counters from PG (queue_restore.lua) and retries;
-- issuing tickets before that could reuse ticket numbers (R1, ADR 0009).
--
-- The user entry is "<pre|ticket>:<rejoins>". Joining is idempotent: an
-- existing entry is returned as is (a pre entry is resolved to its lottery
-- ticket once presize is frozen). The pre-queue ZADD happens only while
-- presize is unset, and presize is frozen inside a script, so every pre
-- member gets rank <= presize and every post-T0 ticket is presize + INCR
-- tail: no duplicates, no gaps, whichever of join/tick freezes first.
-- A rejoin is honoured only after OFFER_EXPIRED: the ticket was offered
-- (<= cursor), its offer is gone or past its deadline, and the user is not
-- admitted. Otherwise the current entry is returned unchanged.
local uid = ARGV[1]
local now = tonumber(ARGV[2])
-- q:opens (set by an admin re-time) beats the caller's possibly cached T0.
local opens = tonumber(redis.call('GET', KEYS[10]) or ARGV[3])
local score = ARGV[4]
local rejoin = ARGV[5] == '1'
local maxRejoins = tonumber(ARGV[6])

if redis.call('EXISTS', KEYS[9]) == 0 then return {'NOT_INIT'} end
if redis.call('EXISTS', KEYS[3]) == 0 and now >= opens then
  redis.call('SET', KEYS[3], redis.call('ZCARD', KEYS[2]))
end
local frozen = redis.call('EXISTS', KEYS[3]) == 1

local pos, rejoins = nil, 0
local cur = redis.call('GET', KEYS[1])
if cur then
  local p, r = string.match(cur, '^([^:]+):(%d+)$')
  if p then pos, rejoins = p, tonumber(r) end
end

local function newTicket(r)
  local t = tonumber(redis.call('GET', KEYS[3])) + redis.call('INCR', KEYS[4])
  redis.call('SET', KEYS[1], t .. ':' .. r)
  return {'TICKET', t}
end

-- An existing pre entry: resolve it once frozen. A pre entry whose lottery
-- member is gone (Redis restore) is treated as no entry.
if pos == 'pre' then
  if not frozen then return {'PRE'} end
  local rank = redis.call('ZRANK', KEYS[2], uid)
  if rank then
    redis.call('SET', KEYS[1], (rank + 1) .. ':' .. rejoins)
    pos = tostring(rank + 1)
  else
    pos = nil
  end
end

if pos and not rejoin then return {'TICKET', tonumber(pos)} end
if redis.call('GET', KEYS[5]) == '1' then return {'SOLD_OUT'} end

if pos then
  -- rejoin requested: eligible only after OFFER_EXPIRED
  local t = tonumber(pos)
  local cursor = tonumber(redis.call('GET', KEYS[6]) or '0')
  local dl = redis.call('ZSCORE', KEYS[7], pos)
  local exp = redis.call('ZSCORE', KEYS[8], uid)
  local expired = t <= cursor and (not dl or tonumber(dl) <= now) and (not exp or tonumber(exp) <= now)
  if not expired then return {'TICKET', t} end
  if rejoins >= maxRejoins then return {'REJOIN_LIMIT'} end
  redis.call('ZREM', KEYS[7], pos)
  return newTicket(rejoins + 1)
end

if not frozen then
  redis.call('ZADD', KEYS[2], 'NX', score, uid)
  redis.call('SET', KEYS[1], 'pre:' .. rejoins)
  return {'PRE'}
end
return newTicket(rejoins)
