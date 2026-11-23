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
