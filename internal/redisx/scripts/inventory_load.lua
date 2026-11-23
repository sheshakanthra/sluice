-- inventory_load.lua: reset an event's inventory keys from Postgres state
-- (DATA-MODEL §6, R1). PG wins: whatever was in Redis is replaced.
-- KEYS: 1 meta, 2 avail, 3 sold, 4 holds, 5 holding, 6 fence, 7 ver
-- ARGV: 1 key prefix 'sl:{e:<eid>}:', 2 now_ms, 3 fence_floor, 4 capacity,
--       5 max_per_user, 6 sale_opens_ms, 7 price, 8 status,
--       9 n_avail, avail..., n_sold, sold...,
--       then per hold 6 fields: hid, uid, exp_ms, fence, state, seats_csv
-- Hold hashes are named from the prefix. They share the event's hash tag, so
-- they live in the same slot as the declared KEYS.
-- fence never goes backwards (max of current and floor) and ver jumps to at
-- least now_ms, so ETags from before a flush are never reused.
-- Returns {'OK'}
local prefix = ARGV[1]
for _, hid in ipairs(redis.call('ZRANGE', KEYS[4], 0, -1)) do
  redis.call('DEL', prefix .. 'hold:' .. hid)
end
redis.call('DEL', KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5])

local i = 9
local function sadd_n(key)
  local n = tonumber(ARGV[i])
  i = i + 1
  local j = 0
  while j < n do
    local k = math.min(j + 5000, n)
    redis.call('SADD', key, unpack(ARGV, i + j, i + k - 1))
    j = k
  end
  i = i + n
end
sadd_n(KEYS[2])
sadd_n(KEYS[3])

while i + 5 <= #ARGV do
  local hk = prefix .. 'hold:' .. ARGV[i]
  local exp = tonumber(ARGV[i + 2])
  redis.call('HSET', hk, 'uid', ARGV[i + 1], 'seats', ARGV[i + 5],
    'exp_ms', ARGV[i + 2], 'fence', ARGV[i + 3], 'state', ARGV[i + 4])
  redis.call('PEXPIREAT', hk, exp + 600000)
  redis.call('ZADD', KEYS[4], exp, ARGV[i])
  redis.call('SADD', KEYS[5], ARGV[i + 1])
  i = i + 6
end

local fence = tonumber(redis.call('GET', KEYS[6]) or '0')
if tonumber(ARGV[3]) > fence then redis.call('SET', KEYS[6], ARGV[3]) end
local ver = tonumber(redis.call('GET', KEYS[7]) or '0')
if tonumber(ARGV[2]) > ver then redis.call('SET', KEYS[7], ARGV[2]) else redis.call('INCR', KEYS[7]) end
redis.call('HSET', KEYS[1], 'capacity', ARGV[4], 'max_per_user', ARGV[5],
  'sale_opens_ms', ARGV[6], 'price', ARGV[7], 'status', ARGV[8], 'loaded', '1')
return {'OK'}
