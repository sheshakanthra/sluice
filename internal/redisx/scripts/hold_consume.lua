-- hold_consume.lua: the hold's seats are sold (DATA-MODEL §5). Idempotent.
-- KEYS: 1 hold:<hid>, 2 holds, 3 holding, 4 sold, 5 ver, 6 avail, 7 q:active
-- ARGV: 1 hid, 2 uid ('' = take it from the hold hash)
-- Returns {'OK', seats_csv, freed (0|1)} | {'MISSING', freed (0|1)}
--
-- The order is CONFIRMED, so the buyer's admission is released in the same
-- step: ZREM q:active uid, unless the user is still in holding (another live
-- hold). The controller can then offer that slot at once (ADR 0010 addendum).
local uid = ARGV[2]
local freed = 0
local function freeAdmission()
  if uid ~= '' and redis.call('SISMEMBER', KEYS[3], uid) == 0 then
    freed = redis.call('ZREM', KEYS[7], uid)
  end
end
if redis.call('EXISTS', KEYS[1]) == 0 then
  redis.call('ZREM', KEYS[2], ARGV[1])
  freeAdmission()
  return {'MISSING', freed}
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
if uid == '' then uid = h[1] end
freeAdmission()
return {'OK', h[2], freed}
