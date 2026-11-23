package events

import (
	"fmt"
	"testing"
)

func TestTrainLayout500(t *testing.T) {
	l, err := TrainLayout(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Coaches) != 10 || l.Capacity() != 500 {
		t.Fatalf("coaches=%d capacity=%d, want 10 and 500", len(l.Coaches), l.Capacity())
	}
	for i, c := range l.Coaches {
		if c.Coach != i+1 || c.Label != fmt.Sprintf("C%d", i+1) || c.Seats != 50 || c.Rows != 10 || c.FirstSeatNo != i*50 {
			t.Errorf("coach %d = %+v", i, c)
		}
	}

	seats := l.Seats()
	if len(seats) != 500 {
		t.Fatalf("%d seats", len(seats))
	}
	labels := map[string]bool{}
	for i, s := range seats {
		// DATA-MODEL section 7: seat_no = (coach-1)*50 + (row-1)*5 + (col-1).
		var coach int
		if _, err := fmt.Sscanf(s.Section, "C%d", &coach); err != nil {
			t.Fatalf("section %q: %v", s.Section, err)
		}
		want := (coach-1)*50 + int(s.Row-1)*5 + int(s.Col-1)
		if int(s.SeatNo) != i || int(s.SeatNo) != want {
			t.Errorf("seat %d: seat_no=%d, formula gives %d", i, s.SeatNo, want)
		}
		if s.Row < 1 || s.Row > 10 || s.Col < 1 || s.Col > 5 {
			t.Errorf("seat %d: row %d col %d out of range", i, s.Row, s.Col)
		}
		if labels[s.Label] {
			t.Errorf("duplicate label %s", s.Label)
		}
		labels[s.Label] = true
	}

	tests := []struct {
		seatNo   int32
		label    string
		row, col int32
	}{
		{0, "C1-1", 1, 1},
		{4, "C1-5", 1, 5},
		{5, "C1-6", 2, 1},
		{49, "C1-50", 10, 5},
		{50, "C2-1", 1, 1},
		{126, "C3-27", 6, 2},
		{212, "C5-13", 3, 3}, // API.md section 3.4 example
		{499, "C10-50", 10, 5},
	}
	for _, tt := range tests {
		s := seats[tt.seatNo]
		if s.Label != tt.label || s.Row != tt.row || s.Col != tt.col {
			t.Errorf("seat %d = %s r%d c%d, want %s r%d c%d", tt.seatNo, s.Label, s.Row, s.Col, tt.label, tt.row, tt.col)
		}
	}
}

func TestTrainLayoutPartialLastCoach(t *testing.T) {
	tests := []struct {
		capacity, coaches, lastSeats, lastRows int
	}{
		{1, 1, 1, 1},
		{5, 1, 5, 1},
		{6, 1, 6, 2},
		{50, 1, 50, 10},
		{51, 2, 1, 1},
		{123, 3, 23, 5},
		{50000, 1000, 50, 10},
	}
	for _, tt := range tests {
		l, err := TrainLayout(tt.capacity)
		if err != nil {
			t.Fatal(err)
		}
		last := l.Coaches[len(l.Coaches)-1]
		if len(l.Coaches) != tt.coaches || last.Seats != tt.lastSeats || last.Rows != tt.lastRows || len(l.Seats()) != tt.capacity {
			t.Errorf("capacity %d: coaches=%d last=%+v seats=%d", tt.capacity, len(l.Coaches), last, len(l.Seats()))
		}
	}
	for _, bad := range []int{0, -1} {
		if _, err := TrainLayout(bad); err == nil {
			t.Errorf("TrainLayout(%d) should fail", bad)
		}
	}
}
