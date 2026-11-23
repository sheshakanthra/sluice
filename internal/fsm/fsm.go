// Package fsm is a tiny, pure finite-state-machine table used by the domain
// packages (internal/inventory, internal/orders). A Machine only answers
// "may X move to Y?"; persistence enforces the same rule with conditional
// UPDATEs in Postgres.
package fsm

import (
	"errors"
	"fmt"
	"slices"
)

// ErrInvalidTransition is matched (errors.Is) by every *TransitionError.
var ErrInvalidTransition = errors.New("invalid state transition")

// TransitionError describes a rejected transition.
type TransitionError struct {
	Machine string
	From    string
	To      string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s: %s -> %s is not allowed", e.Machine, e.From, e.To)
}

// Is makes errors.Is(err, ErrInvalidTransition) true.
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidTransition }

// Edge is one allowed transition.
type Edge[S ~string] struct{ From, To S }

// Machine is an immutable transition table over states S.
type Machine[S ~string] struct {
	name   string
	states []S
	edges  map[S]map[S]struct{}
}

// New builds a machine. Every edge must use declared states; it panics on a
// malformed table because tables are package-level literals checked by tests.
func New[S ~string](name string, states []S, edges []Edge[S]) Machine[S] {
	m := Machine[S]{name: name, states: slices.Clone(states), edges: make(map[S]map[S]struct{}, len(states))}
	for _, e := range edges {
		if !slices.Contains(states, e.From) || !slices.Contains(states, e.To) {
			panic(fmt.Sprintf("fsm %s: edge %s -> %s uses an undeclared state", name, e.From, e.To))
		}
		if m.edges[e.From] == nil {
			m.edges[e.From] = map[S]struct{}{}
		}
		m.edges[e.From][e.To] = struct{}{}
	}
	return m
}

// Name returns the machine's name.
func (m Machine[S]) Name() string { return m.name }

// States returns every declared state, in declaration order.
func (m Machine[S]) States() []S { return slices.Clone(m.states) }

// Valid reports whether s is a declared state.
func (m Machine[S]) Valid(s S) bool { return slices.Contains(m.states, s) }

// CanTransition reports whether from -> to is an allowed edge.
func (m Machine[S]) CanTransition(from, to S) bool {
	_, ok := m.edges[from][to]
	return ok
}

// Transition returns to when from -> to is allowed, else from and a
// *TransitionError.
func (m Machine[S]) Transition(from, to S) (S, error) {
	if !m.CanTransition(from, to) {
		return from, &TransitionError{Machine: m.name, From: string(from), To: string(to)}
	}
	return to, nil
}

// Terminal reports whether s has no outgoing edges.
func (m Machine[S]) Terminal(s S) bool { return len(m.edges[s]) == 0 }
