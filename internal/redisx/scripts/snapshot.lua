-- snapshot.lua: one consistent read of an event's counters (DATA-MODEL §5).
-- KEYS: 1 q:cursor, 2 q:presize, 3 q:tail, 4 q:soldout, 5 avail, 6 holds,
--       7 sold, 8 ver, 9 q:rate, 10 q:offers, 11 q:active, 12 q:pre
-- Returns {cursor, presize, tail, soldout, avail, holds, sold, ver, rate,
--          offers, active, pre_count} as strings; missing values are '0'.
local function get(k) return redis.call('GET', k) or '0' end
return {
  get(KEYS[1]), get(KEYS[2]), get(KEYS[3]), get(KEYS[4]),
  tostring(redis.call('SCARD', KEYS[5])), tostring(redis.call('ZCARD', KEYS[6])),
  tostring(redis.call('SCARD', KEYS[7])), get(KEYS[8]), get(KEYS[9]),
  tostring(redis.call('ZCARD', KEYS[10])), tostring(redis.call('ZCARD', KEYS[11])),
  tostring(redis.call('ZCARD', KEYS[12])),
}
