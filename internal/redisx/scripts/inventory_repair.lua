-- inventory_repair.lua: make Redis agree with Postgres for a few seats after a
-- compensation or a PG takeover (ARCHITECTURE §4.2 "mark seats for repair").
-- KEYS: 1 avail, 2 sold, 3 holds, 4 holding, 5 ver
-- ARGV: 1 key prefix 'sl:{e:<eid>}:', 2 n_drop, hid... (holds to forget
--       without touching avail), then pairs seat_no, state (A|H|S) from PG.
-- Hold hashes are named from the prefix (same hash tag, same slot).
-- Returns {'OK'}
local prefix = ARGV[1]
local n = tonumber(ARGV[2])
for i = 3, 2 + n do
  local hk = prefix .. 'hold:' .. ARGV[i]
  local uid = redis.call('HGET', hk, 'uid')
  if uid then redis.call('SREM', KEYS[4], uid) end
  redis.call('DEL', hk)
  redis.call('ZREM', KEYS[3], ARGV[i])
end
for i = 3 + n, #ARGV - 1, 2 do
  local seat, st = ARGV[i], ARGV[i + 1]
  if st == 'A' then
    redis.call('SADD', KEYS[1], seat)
    redis.call('SREM', KEYS[2], seat)
  elseif st == 'H' then
    redis.call('SREM', KEYS[1], seat)
    redis.call('SREM', KEYS[2], seat)
  elseif st == 'S' then
    redis.call('SREM', KEYS[1], seat)
    redis.call('SADD', KEYS[2], seat)
  end
end
redis.call('INCR', KEYS[5])
return {'OK'}
