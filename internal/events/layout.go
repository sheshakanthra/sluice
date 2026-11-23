// Package events is the event catalogue: the seat layout model, the
// idempotent seed, and the read endpoints GET /api/v1/events and
// GET /api/v1/events/{eventId}.
package events

import (
	"fmt"
	"strconv"
)

// Train geometry from docs/DATA-MODEL.md section 7: coaches of 10 rows x 5
// seats in a 3+2 arrangement with the aisle after column 3.
const (
	SeatsPerCoach = 50
	Cols          = 5
	RowsPerCoach  = SeatsPerCoach / Cols
	AisleAfterCol = 3
)

// Layout is stored in events.layout (jsonb) and returned by
// GET /api/v1/events/{eventId}. For any seat in a coach:
//
//	seat_no = first_seat_no + (row-1)*cols + (col-1)
//	label   = "<coach label>-<seat_no - first_seat_no + 1>"
type Layout struct {
	Version       int     `json:"version"`
	Kind          string  `json:"kind"`
	Cols          int     `json:"cols"`
	AisleAfterCol int     `json:"aisle_after_col"`
	SeatsPerCoach int     `json:"seats_per_coach"`
	Coaches       []Coach `json:"coaches"`
}

// Coach is one coach (section) of the layout. Only the last coach of an
// event whose capacity is not a multiple of 50 has fewer seats.
type Coach struct {
	Coach       int    `json:"coach"`
	Label       string `json:"label"`
	FirstSeatNo int    `json:"first_seat_no"`
	Rows        int    `json:"rows"`
	Seats       int    `json:"seats"`
}

// Seat is one generated seat row.
type Seat struct {
	SeatNo  int32
	Label   string
	Section string
	Row     int32
	Col     int32
}

// TrainLayout builds the layout for capacity seats.
func TrainLayout(capacity int) (Layout, error) {
	if capacity <= 0 {
		return Layout{}, fmt.Errorf("capacity must be > 0, got %d", capacity)
	}
	l := Layout{Version: 1, Kind: "train", Cols: Cols, AisleAfterCol: AisleAfterCol, SeatsPerCoach: SeatsPerCoach}
	for first, c := 0, 1; first < capacity; first, c = first+SeatsPerCoach, c+1 {
		n := min(SeatsPerCoach, capacity-first)
		l.Coaches = append(l.Coaches, Coach{
			Coach: c, Label: "C" + strconv.Itoa(c), FirstSeatNo: first,
			Rows: (n + Cols - 1) / Cols, Seats: n,
		})
	}
	return l, nil
}

// Seats expands the layout into seat rows ordered by seat_no.
func (l Layout) Seats() []Seat {
	var out []Seat
	for _, c := range l.Coaches {
		for i := range c.Seats {
			out = append(out, Seat{
				SeatNo:  int32(c.FirstSeatNo + i), //nolint:gosec // G115: capacity is bounded by int32 events.capacity
				Label:   c.Label + "-" + strconv.Itoa(i+1),
				Section: c.Label,
				Row:     int32(i/l.Cols + 1), //nolint:gosec // G115: <= RowsPerCoach
				Col:     int32(i%l.Cols + 1), //nolint:gosec // G115: <= Cols
			})
		}
	}
	return out
}

// Capacity is the total number of seats in the layout.
func (l Layout) Capacity() int {
	n := 0
	for _, c := range l.Coaches {
		n += c.Seats
	}
	return n
}
