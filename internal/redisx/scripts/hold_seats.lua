-- hold_seats.lua: hold specific seats, all or nothing (DATA-MODEL §5).
-- KEYS: 1 meta, 2 avail, 3 holds, 4 holding, 5 fence, 6 ver, 7 q:soldout, 8 hold:<hid>
-- ARGV: 1 hid, 2 uid, 3 now_ms, 4 ttl_ms, 5.. seat_no
-- Returns {'OK', fence, exp_ms} | {'REPLAY', fence, exp_ms, seats_csv, uid}
--       | {'TAKEN', seat...} | {'HOLD_LIMIT'} | {'SOLD_OUT'} | {'NOT_LOADED'}
local hold = KEYS[8]
if redis.call('EXISTS', hold) == 1 then
  local h = redis.call('HMGET', hold, 'fence', 'exp_ms', 'seats', 'uid')
  return {'REPLAY', h[1], h[2], h[3], h[4]}
end
if redis.call('HGET', KEYS[1], 'loaded') ~= '1' then return {'NOT_LOADED'} end
if redis.call('EXISTS', KEYS[7]) == 1 then return {'SOLD_OUT'} end
if redis.call('SISMEMBER', KEYS[4], ARGV[2]) == 1 then return {'HOLD_LIMIT'} end

local seats = {}
for i = 5, #ARGV do seats[#seats + 1] = ARGV[i] end
local present = redis.call('SMISMEMBER', KEYS[2], unpack(seats))
local taken = {'TAKEN'}
for i = 1, #present do
  if present[i] == 0 then taken[#taken + 1] = seats[i] end
end
if #taken > 1 then return taken end

redis.call('SREM', KEYS[2], unpack(seats))
local fence = redis.call('INCR', KEYS[5])
local exp = tonumber(ARGV[3]) + tonumber(ARGV[4])
redis.call('HSET', hold, 'uid', ARGV[2], 'seats', table.concat(seats, ','),
  'exp_ms', exp, 'fence', fence, 'state', 'ACTIVE')
redis.call('PEXPIREAT', hold, exp + 600000)
redis.call('ZADD', KEYS[3], exp, ARGV[1])
redis.call('SADD', KEYS[4], ARGV[2])
redis.call('INCR', KEYS[6])
return {'OK', fence, exp}
