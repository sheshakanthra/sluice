// Package fsmtest checks a domain state machine against an independently
// written list of allowed edges: every allowed edge is accepted, every other
// pair is rejected (exhaustively and with rapid), and random event walks only
// ever follow allowed edges. Import it from _test.go files only.
package fsmtest

import (
	"errors"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sheshakanthra/sluice/internal/fsm"
)

// Event is a typed transition function such as SeatState.Hold.
type Event[S ~string] struct {
	Name string
	To   S // the state the event moves to when allowed
	Fn   func(S) (S, error)
}

// Spec is the expected behaviour, written by hand in each domain test.
type Spec[S ~string] struct {
	Machine fsm.Machine[S]
	States  []S           // every state, as listed in DATA-MODEL section 3
	Allowed []fsm.Edge[S] // every allowed edge, as listed in DATA-MODEL section 3
	Initial S
	Events  []Event[S]
}

func (sp Spec[S]) allowed(from, to S) bool {
	return slices.Contains(sp.Allowed, fsm.Edge[S]{From: from, To: to})
}

// Run executes every check as subtests of t.
func Run[S ~string](t *testing.T, sp Spec[S]) {
	t.Helper()

	t.Run("states_match_spec", func(t *testing.T) {
		got := sp.Machine.States()
		if !slices.Equal(got, sp.States) {
			t.Fatalf("machine states %v, spec states %v", got, sp.States)
		}
	})

	t.Run("every_allowed_edge", func(t *testing.T) {
		for _, e := range sp.Allowed {
			if !sp.Machine.CanTransition(e.From, e.To) {
				t.Errorf("%s -> %s should be allowed", e.From, e.To)
			}
			got, err := sp.Machine.Transition(e.From, e.To)
			if err != nil || got != e.To {
				t.Errorf("Transition(%s, %s) = %s, %v", e.From, e.To, got, err)
			}
		}
	})

	t.Run("every_disallowed_pair_exhaustive", func(t *testing.T) {
		for _, from := range sp.States {
			for _, to := range sp.States {
				if sp.allowed(from, to) {
					continue
				}
				checkRejected(t, sp, from, to)
			}
		}
	})

	t.Run("typed_events_match_table", func(t *testing.T) {
		for _, ev := range sp.Events {
			for _, from := range sp.States {
				got, err := ev.Fn(from)
				if sp.allowed(from, ev.To) {
					if err != nil || got != ev.To {
						t.Errorf("%s from %s = %s, %v; want %s", ev.Name, from, got, err, ev.To)
					}
					continue
				}
				if !errors.Is(err, fsm.ErrInvalidTransition) || got != from {
					t.Errorf("%s from %s = %s, %v; want rejection", ev.Name, from, got, err)
				}
			}
		}
	})

	// rapid: arbitrary pairs, including strings that are not states at all.
	t.Run("rapid_disallowed_pairs_rejected", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			from := drawState(rt, sp, "from")
			to := drawState(rt, sp, "to")
			if sp.allowed(from, to) {
				if !sp.Machine.CanTransition(from, to) {
					rt.Fatalf("%s -> %s should be allowed", from, to)
				}
				return
			}
			if sp.Machine.CanTransition(from, to) {
				rt.Fatalf("%s -> %s should be rejected", from, to)
			}
			got, err := sp.Machine.Transition(from, to)
			var te *fsm.TransitionError
			if !errors.As(err, &te) || !errors.Is(err, fsm.ErrInvalidTransition) || got != from {
				rt.Fatalf("Transition(%s, %s) = %s, %v; want *TransitionError and unchanged state", from, to, got, err)
			}
		})
	})

	// rapid: a random sequence of typed events from the initial state only
	// ever moves along allowed edges, and a rejected event never changes state.
	t.Run("rapid_event_walk", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			cur := sp.Initial
			steps := rapid.IntRange(0, 20).Draw(rt, "steps")
			for range steps {
				ev := rapid.SampledFrom(sp.Events).Draw(rt, "event")
				next, err := ev.Fn(cur)
				if err != nil {
					if next != cur || sp.allowed(cur, ev.To) {
						rt.Fatalf("%s from %s rejected (%v) but the edge is allowed or state changed to %s", ev.Name, cur, err, next)
					}
					continue
				}
				if !sp.allowed(cur, next) {
					rt.Fatalf("%s moved %s -> %s, which is not an allowed edge", ev.Name, cur, next)
				}
				cur = next
				if sp.Machine.Terminal(cur) && hasOutgoing(sp, cur) {
					rt.Fatalf("%s reported terminal but has outgoing edges", cur)
				}
			}
		})
	})
}

func checkRejected[S ~string](t *testing.T, sp Spec[S], from, to S) {
	t.Helper()
	if sp.Machine.CanTransition(from, to) {
		t.Errorf("%s -> %s should be rejected", from, to)
	}
	got, err := sp.Machine.Transition(from, to)
	if !errors.Is(err, fsm.ErrInvalidTransition) || got != from {
		t.Errorf("Transition(%s, %s) = %s, %v; want rejection", from, to, got, err)
	}
}

func hasOutgoing[S ~string](sp Spec[S], s S) bool {
	return slices.ContainsFunc(sp.Allowed, func(e fsm.Edge[S]) bool { return e.From == s })
}

// drawState returns a declared state most of the time and an arbitrary
// string otherwise, so unknown states are exercised too.
func drawState[S ~string](rt *rapid.T, sp Spec[S], label string) S {
	return rapid.OneOf(
		rapid.SampledFrom(sp.States),
		rapid.Map(rapid.String(), func(s string) S { return S(s) }),
	).Draw(rt, label)
}
