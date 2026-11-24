// Package keys is the only place SLUICE Redis key names are built
// (DATA-MODEL §4). Every per-event key carries the hash tag {e:<eid>}, so all
// of an event's keys share one cluster slot and a Lua script can touch them
// together.
package keys

import "github.com/google/uuid"

// Prefix is "sl:{e:<eid>}:", the common prefix of every per-event key.
func Prefix(eid uuid.UUID) string { return "sl:{e:" + eid.String() + "}:" }

func k(eid uuid.UUID, name string) string { return Prefix(eid) + name }

// Meta is the event metadata HASH (capacity, ..., loaded=1).
func Meta(eid uuid.UUID) string { return k(eid, "meta") }

// Avail is the SET of seat numbers free to hold.
func Avail(eid uuid.UUID) string { return k(eid, "avail") }

// Sold is the SET of sold seat numbers.
func Sold(eid uuid.UUID) string { return k(eid, "sold") }

// Hold is the HASH of one hold.
func Hold(eid, hid uuid.UUID) string { return k(eid, "hold:"+hid.String()) }

// Holds is the ZSET hid -> exp_ms (sweeper index).
func Holds(eid uuid.UUID) string { return k(eid, "holds") }

// Holding is the SET of users that currently hold seats.
func Holding(eid uuid.UUID) string { return k(eid, "holding") }

// Fence is the monotonic fence counter.
func Fence(eid uuid.UUID) string { return k(eid, "fence") }

// Ver is the seat-map version (bumps on every seat change).
func Ver(eid uuid.UUID) string { return k(eid, "ver") }

// QSoldOut is "1" once the event is sold out (written by the queue, P4).
func QSoldOut(eid uuid.UUID) string { return k(eid, "q:soldout") }

// QCursor is the highest ticket offered.
func QCursor(eid uuid.UUID) string { return k(eid, "q:cursor") }

// QPreSize is ZCARD(pre) frozen at T0.
func QPreSize(eid uuid.UUID) string { return k(eid, "q:presize") }

// QTail is the post-T0 join counter.
func QTail(eid uuid.UUID) string { return k(eid, "q:tail") }

// QRate is the admit-rate EMA.
func QRate(eid uuid.UUID) string { return k(eid, "q:rate") }

// QOffers is the ZSET ticket -> claim deadline.
func QOffers(eid uuid.UUID) string { return k(eid, "q:offers") }

// QActive is the ZSET uid -> admission expiry.
func QActive(eid uuid.UUID) string { return k(eid, "q:active") }

// RebuildLock is a short SET NX lock so only one process rebuilds an
// event's inventory at a time (DATA-MODEL §6).
func RebuildLock(eid uuid.UUID) string { return k(eid, "rebuild_lock") }

// Audit is the live audit cache hash (oversold, duplicates, orphans,
// tickets, at_ms), refreshed every 5 s by the worker (P3, read by /stats).
func Audit(eid uuid.UUID) string { return k(eid, "audit") }

// QPre is the pre-queue lottery ZSET uid -> random score.
func QPre(eid uuid.UUID) string { return k(eid, "q:pre") }

// QUser is a user's queue entry: "<pre|ticket>:<rejoins>".
func QUser(eid, uid uuid.UUID) string { return k(eid, "q:user:"+uid.String()) }

// QRateAt is the ms timestamp of the last admit-rate update (q:rate's clock).
func QRateAt(eid uuid.UUID) string { return k(eid, "q:rate_at") }

// QueueJoinKeys are queue_join.lua's KEYS, in order.
func QueueJoinKeys(eid, uid uuid.UUID) []string {
	return []string{QUser(eid, uid), QPre(eid), QPreSize(eid), QTail(eid), QSoldOut(eid), QCursor(eid), QOffers(eid),
		QActive(eid), QInit(eid), QOpens(eid)}
}

// QueueResolveKeys are queue_resolve.lua's KEYS, in order.
func QueueResolveKeys(eid, uid uuid.UUID) []string {
	return []string{QUser(eid, uid), QPre(eid), QPreSize(eid)}
}

// QueueClaimKeys are queue_claim.lua's KEYS, in order.
func QueueClaimKeys(eid uuid.UUID) []string {
	return []string{QSoldOut(eid), QCursor(eid), QOffers(eid), QActive(eid)}
}

// QueueLeaveKeys are queue_leave.lua's KEYS, in order.
func QueueLeaveKeys(eid, uid uuid.UUID) []string {
	return []string{QActive(eid), QUser(eid, uid), QOffers(eid), QPre(eid), QPreSize(eid)}
}

// QueueTickKeys are queue_tick.lua's KEYS, in order.
func QueueTickKeys(eid uuid.UUID) []string {
	return []string{Meta(eid), Avail(eid), Holds(eid), Holding(eid), QPre(eid), QPreSize(eid), QTail(eid),
		QCursor(eid), QOffers(eid), QActive(eid), QSoldOut(eid), QRate(eid), QRateAt(eid), QInit(eid), QOpens(eid)}
}

// SnapshotKeys are snapshot.lua's KEYS, in order.
func SnapshotKeys(eid uuid.UUID) []string {
	return []string{QCursor(eid), QPreSize(eid), QTail(eid), QSoldOut(eid), Avail(eid), Holds(eid),
		Sold(eid), Ver(eid), QRate(eid), QOffers(eid), QActive(eid), QPre(eid)}
}

// QInit marks an event's queue counters as initialised from the PG snapshot
// (R1, ADR 0009). queue_join and queue_tick refuse to run without it.
func QInit(eid uuid.UUID) string { return k(eid, "q:init") }

// QueueRestoreKeys are queue_restore.lua's KEYS, in order.
func QueueRestoreKeys(eid uuid.UUID) []string {
	return []string{QInit(eid), QPreSize(eid), QTail(eid), QCursor(eid), QSoldOut(eid), QOffers(eid), QActive(eid),
		QRate(eid), QRateAt(eid)}
}

// QueueDumpKeys are queue_dump.lua's KEYS, in order.
func QueueDumpKeys(eid uuid.UUID) []string {
	return []string{QInit(eid), QPreSize(eid), QTail(eid), QCursor(eid), QSoldOut(eid), QRate(eid), QPre(eid),
		QOffers(eid), QActive(eid)}
}

// RateLimitPrefix prefixes the GCRA keys. redis_rate adds its own "rate:"
// prefix in front, so the stored keys are rate:sl:rl:<rule>:<subject>.
const RateLimitPrefix = "sl:rl:"

// QueueReopenKeys are queue_reopen.lua's KEYS, in order.
func QueueReopenKeys(eid uuid.UUID) []string {
	return []string{QInit(eid), QPreSize(eid), QTail(eid), QPre(eid), QSoldOut(eid), QCursor(eid), QOffers(eid),
		QActive(eid), QRate(eid), QRateAt(eid), QOpens(eid)}
}

// QOpens is an admin override of T0 in ms (open-sale), read by queue_join and
// queue_tick in preference to the caller's cached sale_opens_at.
func QOpens(eid uuid.UUID) string { return k(eid, "q:opens") }
