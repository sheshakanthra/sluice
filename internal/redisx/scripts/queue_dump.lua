-- queue_dump.lua: the queue state the worker persists to queue_snapshots
-- every second (R1, ADR 0009).
-- KEYS: 1 q:init, 2 q:presize, 3 q:tail, 4 q:cursor, 5 q:soldout, 6 q:rate,
--       7 q:pre, 8 q:offers, 9 q:active
-- ARGV: none
-- Returns {init (0|1), presize (-1 = not frozen), tail, cursor, soldout (0|1),
--          rate, pre_count, n_offers, ticket x n, n_active, (uid, exp_ms) x n}
-- offers and active are bounded by the admission target (about
-- QUEUE_MAX_ACTIVE x QUEUE_OVERADMIT), so the reply stays small.
local out = {
  tostring(redis.call('EXISTS', KEYS[1])),
  redis.call('GET', KEYS[2]) or '-1',
  redis.call('GET', KEYS[3]) or '0',
  redis.call('GET', KEYS[4]) or '0',
  (redis.call('GET', KEYS[5]) == '1') and '1' or '0',
  redis.call('GET', KEYS[6]) or '0',
  tostring(redis.call('ZCARD', KEYS[7])),
}
local offers = redis.call('ZRANGE', KEYS[8], 0, -1)
out[#out + 1] = tostring(#offers)
for _, t in ipairs(offers) do out[#out + 1] = t end
local active = redis.call('ZRANGE', KEYS[9], 0, -1, 'WITHSCORES')
out[#out + 1] = tostring(#active / 2)
for _, v in ipairs(active) do out[#out + 1] = v end
return out
