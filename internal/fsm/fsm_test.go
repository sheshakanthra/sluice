package fsm

import (
	"errors"
	"testing"
)

type st string

func TestMachine(t *testing.T) {
	m := New("demo", []st{"a", "b", "c"}, []Edge[st]{{From: "a", To: "b"}, {From: "b", To: "c"}})

	if m.Name() != "demo" || !m.Valid("a") || m.Valid("z") {
		t.Fatal("name/valid")
	}
	if !m.CanTransition("a", "b") || m.CanTransition("a", "c") || m.CanTransition("c", "a") || m.CanTransition("z", "a") {
		t.Fatal("edges")
	}
	if !m.Terminal("c") || m.Terminal("a") {
		t.Fatal("terminal")
	}

	got, err := m.Transition("a", "c")
	var te *TransitionError
	if got != "a" || !errors.Is(err, ErrInvalidTransition) || !errors.As(err, &te) {
		t.Fatalf("Transition(a, c) = %s, %v", got, err)
	}
	if te.Machine != "demo" || te.From != "a" || te.To != "c" || err.Error() != "demo: a -> c is not allowed" {
		t.Fatalf("error = %+v %q", te, err.Error())
	}

	// States returns a copy.
	s := m.States()
	s[0] = "x"
	if m.States()[0] != "a" {
		t.Fatal("States must not expose internal storage")
	}
}

func TestNewPanicsOnUndeclaredState(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for an edge to an undeclared state")
		}
	}()
	New("bad", []st{"a"}, []Edge[st]{{From: "a", To: "b"}})
}
