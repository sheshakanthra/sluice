-- hold_consume.lua: the hold's seats are sold (DATA-MODEL §5). Idempotent.
-- KEYS: 1 hold:<hid>, 2 holds, 3 holding, 4 sold, 5 ver, 6 avail
-- ARGV: 1 hid
-- Returns {'OK', seats_csv} | {'MISSING'}
if redis.call('EXISTS', KEYS[1]) == 0 then
  redis.call('ZREM', KEYS[2], ARGV[1])
  return {'MISSING'}
end
local h = redis.call('HMGET', KEYS[1], 'uid', 'seats')
local seats = {}
for s in string.gmatch(h[2], '[^,]+') do seats[#seats + 1] = s end
redis.call('SADD', KEYS[4], unpack(seats))
redis.call('SREM', KEYS[6], unpack(seats))
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('SREM', KEYS[3], h[1])
redis.call('INCR', KEYS[5])
return {'OK', h[2]}
