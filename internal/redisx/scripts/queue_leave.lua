-- queue_leave.lua: leave the queue or give up an admission (DATA-MODEL §5).
-- KEYS: 1 q:active, 2 q:user:<uid>, 3 q:offers, 4 q:pre, 5 q:presize
-- ARGV: uid, ticket (0 = none)
-- Returns {'OK'}. Frees the admission slot and any outstanding offer, so the
-- next tick can admit someone else. Before T0 the lottery entry is removed;
-- after T0 it stays, because removing it would shift everyone's rank.
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('DEL', KEYS[2])
if ARGV[2] ~= '0' then redis.call('ZREM', KEYS[3], ARGV[2]) end
if redis.call('EXISTS', KEYS[5]) == 0 then redis.call('ZREM', KEYS[4], ARGV[1]) end
return {'OK'}
