package history

import "slices"

// State is what one migration reconciles to, which is not the same axis as one run's outcome.
type State string

// The reconciled states. The zero State names none of them and is not a state.
const (
	StateApplied State = "applied"
	StatePending State = "pending"
	StateMissing State = "missing"
	StateDrifted State = "drifted"
	StateDirty   State = "dirty"
	// StateWaiting is a shadow-table migration held in sync until someone confirms its cutover.
	StateWaiting State = "waiting"
)

// states lists every reconciled state, from the two that are settled to the four that need acting on.
var states = []State{StateApplied, StatePending, StateMissing, StateDrifted, StateDirty, StateWaiting}

// States returns every state a migration may reconcile to, as a copy.
func States() []State {
	return slices.Clone(states)
}

// Valid reports whether a value names one of the reconciled states.
func (s State) Valid() bool {
	return slices.Contains(states, s)
}

// String returns the state's wire name, which is what --json renders and a table prints.
func (s State) String() string {
	return string(s)
}
