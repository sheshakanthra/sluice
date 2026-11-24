-- hold_release.lua: give a hold's seats back (DATA-MODEL §5). Idempotent.
-- Consumed holds are already deleted, and seats in sold are never re-added.
-- KEYS: 1 hold:<hid>, 2 holds, 3 holding, 4 avail, 5 ver, 6 sold, 7 q:active
-- ARGV: 1 hid, 2 uid ('' = take it from the hold hash), 3 free_admission (0|1)
-- Returns {'OK', seats_csv, freed (0|1)} | {'MISSING', freed (0|1)}
--
-- free_admission = 1 (the hold ended in PG: released, expired, or its order
-- failed) also removes the user from q:active, so the admission controller
-- stops counting them as seeking (ADR 0010 addendum). It is skipped while the
-- user is still in holding (another live hold). A compensation (PG refused
-- the grant; the user is still shopping) passes 0.
local uid = ARGV[2]
local freed = 0
local function freeAdmission()
  if ARGV[3] == '1' and uid ~= '' and redis.call('SISMEMBER', KEYS[3], uid) == 0 then
    freed = redis.call('ZREM', KEYS[7], uid)
  end
end
if redis.call('EXISTS', KEYS[1]) == 0 then
  redis.call('ZREM', KEYS[2], ARGV[1])
  freeAdmission()
  return {'MISSING', freed}
end
local h = redis.call('HMGET', KEYS[1], 'uid', 'seats')
for s in string.gmatch(h[2], '[^,]+') do
  if redis.call('SISMEMBER', KEYS[6], s) == 0 then redis.call('SADD', KEYS[4], s) end
end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('SREM', KEYS[3], h[1])
redis.call('INCR', KEYS[5])
if uid == '' then uid = h[1] end
freeAdmission()
return {'OK', h[2], freed}
