-- hold_release.lua: give a hold's seats back (DATA-MODEL §5). Idempotent.
-- Consumed holds are already deleted, and seats in sold are never re-added.
-- KEYS: 1 hold:<hid>, 2 holds, 3 holding, 4 avail, 5 ver, 6 sold
-- ARGV: 1 hid
-- Returns {'OK', seats_csv} | {'MISSING'}
if redis.call('EXISTS', KEYS[1]) == 0 then
  redis.call('ZREM', KEYS[2], ARGV[1])
  return {'MISSING'}
end
local h = redis.call('HMGET', KEYS[1], 'uid', 'seats')
for s in string.gmatch(h[2], '[^,]+') do
  if redis.call('SISMEMBER', KEYS[6], s) == 0 then redis.call('SADD', KEYS[4], s) end
end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('SREM', KEYS[3], h[1])
redis.call('INCR', KEYS[5])
return {'OK', h[2]}
