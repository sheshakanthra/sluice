-- queue_reopen.lua: undo the T0 freeze of a queue nobody has joined, so the
-- admin open-sale endpoint can re-time the sale (API.md §2, ADR 0009).
-- KEYS: 1 q:init, 2 q:presize, 3 q:tail, 4 q:pre, 5 q:soldout, 6 q:cursor,
--       7 q:offers, 8 q:active, 9 q:rate, 10 q:rate_at, 11 q:opens
-- ARGV: sale_opens_ms (the new T0)
-- Returns {'OK'} | {'BUSY', joined}  (somebody holds a ticket or a lottery
--         entry, or the event sold out: re-timing would break their place)
--
-- The caller restores the counters from PG first, so q:init is set and the
-- checks below see every ticket ever issued.
local joined = tonumber(redis.call('GET', KEYS[3]) or '0') + redis.call('ZCARD', KEYS[4])
  + tonumber(redis.call('GET', KEYS[2]) or '0')
if joined > 0 or redis.call('GET', KEYS[5]) == '1' then return {'BUSY', joined} end
redis.call('DEL', KEYS[2], KEYS[3], KEYS[6], KEYS[7], KEYS[8], KEYS[9], KEYS[10])
redis.call('SET', KEYS[1], '1')
-- Joins and ticks use this T0 at once, while event caches catch up.
redis.call('SET', KEYS[11], ARGV[1])
return {'OK'}
