-- queue_tick.lua: one admission-controller step (ARCHITECTURE §5).
-- KEYS: 1 meta, 2 avail, 3 holds, 4 holding, 5 q:pre, 6 q:presize, 7 q:tail,
--       8 q:cursor, 9 q:offers, 10 q:active, 11 q:soldout, 12 q:rate,
--       13 q:rate_at
-- ARGV: now_ms, sale_opens_ms, max_active, overadmit, min_admit,
--       max_per_tick, offer_ttl_ms, rate_tau_ms
-- Returns {'NOT_LOADED'} | {'PREQUEUE'} | {'SOLD_OUT'} | {'PAUSED'} |
--         {'ADMITTING', k, target, seeking, outstanding, expired_offers}
--
-- Atomic, so any number of controllers can run it concurrently: offers go
-- out in ticket order from a monotonic cursor, the sold-out flag is checked
-- before anything is offered, and seeking + outstanding never exceeds the
-- target that this tick computed. The inventory guard matters: with no
-- inventory loaded, avail and holds both read 0, which must not be taken
-- for "sold out".
local now = tonumber(ARGV[1])
local opens = tonumber(ARGV[2])
local maxActive = tonumber(ARGV[3])
local overadmit = tonumber(ARGV[4])
local minAdmit = tonumber(ARGV[5])
local maxPerTick = tonumber(ARGV[6])
local offerTTL = tonumber(ARGV[7])
local tau = tonumber(ARGV[8])

if redis.call('HGET', KEYS[1], 'loaded') ~= '1' then return {'NOT_LOADED'} end
if redis.call('EXISTS', KEYS[6]) == 0 then
  if now < opens then return {'PREQUEUE'} end
  redis.call('SET', KEYS[6], redis.call('ZCARD', KEYS[5]))
end

local expired = redis.call('ZREMRANGEBYSCORE', KEYS[9], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[10], '-inf', now)
if redis.call('GET', KEYS[11]) == '1' then return {'SOLD_OUT'} end

local avail = redis.call('SCARD', KEYS[2])
local held = redis.call('ZCARD', KEYS[3])
if avail == 0 and held == 0 then
  redis.call('SET', KEYS[11], '1')
  return {'SOLD_OUT'}
end

-- admit_rate_ema in admissions per second: a time-decayed counter,
-- rate = rate * exp(-dt / tau) + k / tau. It handles any tick spacing,
-- including two controllers ticking in the same millisecond (dt = 0).
local function updateRate(k)
  local r = tonumber(redis.call('GET', KEYS[12]) or '0')
  local at = tonumber(redis.call('GET', KEYS[13]) or ARGV[1])
  local dt = math.max(0, now - at)
  r = r * math.exp(-dt / tau) + k * 1000 / tau
  redis.call('SET', KEYS[12], string.format('%.4f', r))
  if now > at or not redis.call('GET', KEYS[13]) then redis.call('SET', KEYS[13], ARGV[1]) end
end

if avail == 0 then
  updateRate(0)
  return {'PAUSED'}
end

local seeking = math.max(0, redis.call('ZCARD', KEYS[10]) - redis.call('ZINTERCARD', 2, KEYS[10], KEYS[4]))
local outstanding = redis.call('ZCARD', KEYS[9])
local target = math.min(maxActive, math.max(minAdmit, math.ceil(avail * overadmit)))
local presize = tonumber(redis.call('GET', KEYS[6]))
local tail = tonumber(redis.call('GET', KEYS[7]) or '0')
local cursor = tonumber(redis.call('GET', KEYS[8]) or '0')
local k = math.max(0, math.min(target - seeking - outstanding, maxPerTick, presize + tail - cursor))

local deadline = now + offerTTL
local batch = {}
for t = cursor + 1, cursor + k do
  batch[#batch + 1] = deadline
  batch[#batch + 1] = t
  if #batch >= 1000 then
    redis.call('ZADD', KEYS[9], unpack(batch))
    batch = {}
  end
end
if #batch > 0 then redis.call('ZADD', KEYS[9], unpack(batch)) end
if k > 0 then redis.call('INCRBY', KEYS[8], k) end
updateRate(k)
return {'ADMITTING', k, target, seeking, outstanding, expired}
