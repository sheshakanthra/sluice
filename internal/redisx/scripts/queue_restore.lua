-- queue_restore.lua: initialise an event's queue counters from the PG
-- snapshot (R1, DATA-MODEL §6, ADR 0009). Runs once per Redis lifetime of
-- the event: if q:init exists it does nothing, so concurrent restorers (API
-- replicas and workers) are safe.
-- KEYS: 1 q:init, 2 q:presize, 3 q:tail, 4 q:cursor, 5 q:soldout,
--       6 q:offers, 7 q:active, 8 q:rate, 9 q:rate_at
-- ARGV: now_ms, offer_ttl_ms, presize (-1 = not frozen), tail, cursor,
--       soldout (0|1), rate, n_offers, ticket x n_offers,
--       n_active, (uid, exp_ms) x n_active
-- Returns {'OK'} | {'ALREADY'}
--
-- Counters only move forward (max with whatever is there). Offers that were
-- outstanding at snapshot time get a fresh claim deadline; admissions that
-- were live at snapshot time come back with their original expiry, so a
-- user who had already claimed is never admitted a second time.
if redis.call('EXISTS', KEYS[1]) == 1 then return {'ALREADY'} end
local now = tonumber(ARGV[1])
local offerTTL = tonumber(ARGV[2])
local presize = tonumber(ARGV[3])
local function raise(k, v)
  local cur = tonumber(redis.call('GET', k) or '0')
  if v > cur then redis.call('SET', k, v) end
end
if presize >= 0 then redis.call('SET', KEYS[2], presize, 'NX') end
raise(KEYS[3], tonumber(ARGV[4]))
raise(KEYS[4], tonumber(ARGV[5]))
if ARGV[6] == '1' then redis.call('SET', KEYS[5], '1') end
if tonumber(ARGV[7]) > 0 then
  redis.call('SET', KEYS[8], ARGV[7])
  redis.call('SET', KEYS[9], now)
end
local i = 8
local n = tonumber(ARGV[i])
for j = 1, n do
  redis.call('ZADD', KEYS[6], 'NX', now + offerTTL, ARGV[i + j])
end
i = i + n + 1
n = tonumber(ARGV[i])
for j = 1, n do
  local uid, exp = ARGV[i + 2 * j - 1], tonumber(ARGV[i + 2 * j])
  if exp > now then redis.call('ZADD', KEYS[7], 'NX', exp, uid) end
end
redis.call('SET', KEYS[1], '1')
return {'OK'}
