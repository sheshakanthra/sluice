-- queue_resolve.lua: turn a pre-queue entry into its lottery ticket, once
-- per user (DATA-MODEL §5).
-- KEYS: 1 q:user:<uid>, 2 q:pre, 3 q:presize
-- ARGV: uid
-- Returns {'TICKET', n} | {'PENDING'} (presize not frozen yet) | {'NOT_FOUND'}
-- The ticket is ZRANK + 1 in ascending random-score order; it is stored in
-- the user entry, so repeating the call returns the same ticket.
local cur = redis.call('GET', KEYS[1])
if not cur then return {'NOT_FOUND'} end
local pos, rejoins = string.match(cur, '^([^:]+):(%d+)$')
if not pos then return {'NOT_FOUND'} end
if pos ~= 'pre' then return {'TICKET', tonumber(pos)} end
if redis.call('EXISTS', KEYS[3]) == 0 then return {'PENDING'} end
local rank = redis.call('ZRANK', KEYS[2], ARGV[1])
if not rank then return {'NOT_FOUND'} end
redis.call('SET', KEYS[1], (rank + 1) .. ':' .. rejoins)
return {'TICKET', rank + 1}
