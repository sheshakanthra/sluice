// Package naive is the B0 hold path.
//
// ##########################################################################
// ##  BASELINE ONLY. NEVER USE IN THE REAL SYSTEM.                        ##
// ##  This is the hold code a hurried team writes (LOADTEST §4, B0; ADR   ##
// ##  0011). It reserves nothing: a "hold" is a read-only availability    ##
// ##  check against naive_sales plus a cart row. It exists so the rush    ##
// ##  can show the bugs the real system prevents, and so the auditor can  ##
// ##  prove it catches them. The API mounts it only when BASELINE=naive,  ##
// ##  which the config refuses outside APP_ENV rush/test.                 ##
// ##########################################################################
package naive

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
)

// Hold is the naive POST /events/{eventId}/holds handler.
type Hold struct {
	Inv      *inventory.Service // event lookup only (capacity, price, labels)
	Q        *gen.Queries
	HoldTTL  time.Duration // cosmetic: the pseudo-hold's expires_at
	MaxSeats int
	Log      *slog.Logger
}

type holdBody struct {
	SeatNos  []int32 `json:"seat_nos"`
	Quantity *int    `json:"quantity"`
}

// ServeHTTP reads what is sold, picks seats, writes a cart row and answers
// like a hold. Nothing stops another buyer from picking the same seats.
func (h *Hold) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ev, err := h.Inv.Event(ctx, r.PathValue("eventId"))
	if errors.Is(err, inventory.ErrEventNotFound) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
		return
	}
	if err != nil {
		h.unavailable(w, r, "event", err)
		return
	}
	var b holdBody
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || (b.Quantity == nil) == (len(b.SeatNos) == 0) {
		httpx.WriteError(w, r, httpx.CodeValidation, `body must be {"seat_nos": [..]} or {"quantity": n}`)
		return
	}
	qty := len(b.SeatNos)
	if b.Quantity != nil {
		qty = *b.Quantity
	}
	if qty < 1 || qty > h.MaxSeats {
		httpx.WriteError(w, r, httpx.CodeValidation, "too many or too few seats")
		return
	}
	for _, s := range b.SeatNos {
		if s < 0 || s >= ev.Capacity {
			httpx.WriteError(w, r, httpx.CodeValidation, "unknown seat")
			return
		}
	}

	soldRows, err := h.Q.NaiveSoldSeats(ctx, ev.ID)
	if err != nil {
		h.unavailable(w, r, "sold seats", err)
		return
	}
	sold := make(map[int32]bool, len(soldRows))
	for _, s := range soldRows {
		sold[s] = true
	}
	if len(sold) >= int(ev.Capacity) {
		httpx.WriteError(w, r, httpx.CodeSoldOut, "")
		return
	}
	seats := b.SeatNos
	if b.Quantity != nil {
		// Best available = the lowest seat numbers that were unsold a moment ago.
		for s := int32(0); s < ev.Capacity && len(seats) < qty; s++ {
			if !sold[s] {
				seats = append(seats, s)
			}
		}
		if len(seats) < qty {
			httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeInsufficientSeats, "").With("available", len(seats)))
			return
		}
	} else {
		var taken []int32
		for _, s := range seats {
			if sold[s] {
				taken = append(taken, s)
			}
		}
		if len(taken) > 0 {
			httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeSeatsTaken, "").With("taken", taken))
			return
		}
	}
	slices.Sort(seats)

	c, _ := auth.ClaimsFrom(ctx)
	ref, err := uuid.NewV7()
	if err != nil {
		h.unavailable(w, r, "order ref", err)
		return
	}
	if err := h.Q.NaiveInsertOrder(ctx, gen.NaiveInsertOrderParams{
		OrderRef: ref, EventID: ev.ID, UserID: c.Subject, SeatNos: seats, AmountPaise: ev.PricePaise * int64(len(seats)),
	}); err != nil {
		h.unavailable(w, r, "insert cart", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, inventory.HoldResponse{
		HoldID: ref, Status: "ACTIVE", SeatNos: seats, Labels: ev.Labels(seats),
		ExpiresAt: time.Now().Add(h.HoldTTL).UTC().Truncate(time.Millisecond),
	})
}

func (h *Hold) unavailable(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.Log.ErrorContext(r.Context(), "naive hold: "+what, slog.String("error", err.Error()))
	httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
}
