-- hold_extend.lua: move a hold to PAYMENT with a later expiry (DATA-MODEL §5).
-- KEYS: 1 hold:<hid>, 2 holds
-- ARGV: 1 hid, 2 new_exp_ms
-- Returns {'OK'} | {'MISSING'}
if redis.call('EXISTS', KEYS[1]) == 0 then return {'MISSING'} end
redis.call('HSET', KEYS[1], 'state', 'PAYMENT', 'exp_ms', ARGV[2])
redis.call('PEXPIREAT', KEYS[1], tonumber(ARGV[2]) + 600000)
redis.call('ZADD', KEYS[2], ARGV[2], ARGV[1])
return {'OK'}
