package ir

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Direction is which way a migration is being run.
type Direction string

// The two directions every migration defines, per CLAUDE.md invariant #10.
const (
	Up   Direction = "up"
	Down Direction = "down"
)

// directions lists every accepted direction.
var directions = []Direction{Up, Down}

// Valid reports whether a direction is one of the two a migration can run.
func (d Direction) Valid() bool {
	return slices.Contains(directions, d)
}

// ErrSealed reports an attempt to record into a recording that is already closed.
var ErrSealed = errors.New("recording is sealed")

// Source is where in a migration's own source an operation was recorded.
type Source struct {
	File     string
	Line     int
	Function string
}

// String renders the call site as file:line, or empty when nothing was supplied.
func (s Source) String() string {
	if s.File == "" {
		return ""
	}

	if s.Line <= 0 {
		return s.File
	}

	return s.File + ":" + strconv.Itoa(s.Line)
}

// Zero reports whether the SDK supplied no attribution at all.
func (s Source) Zero() bool {
	return s == Source{}
}

// Entry is one recorded operation together with where it came from.
type Entry struct {
	Index  int
	Op     Operation
	Source Source
}

// Recorder accumulates the operations of one migration execution, in arrival order.
type Recorder struct {
	mu        sync.Mutex
	migration string
	direction Direction
	entries   []Entry
	sealed    *Recording
}

// NewRecorder starts a recording for one migration running in one direction.
func NewRecorder(migration string, d Direction) (*Recorder, error) {
	if strings.TrimSpace(migration) == "" {
		return nil, errors.New("migration ID is required to record operations")
	}

	if !d.Valid() {
		return nil, fmt.Errorf("unknown direction %q (expected %s or %s)", string(d), Up, Down)
	}

	return &Recorder{migration: migration, direction: d}, nil
}

// Migration returns the migration whose operations are being recorded.
func (r *Recorder) Migration() string {
	return r.migration
}

// Direction returns the direction being recorded.
func (r *Recorder) Direction() Direction {
	return r.direction
}

// Len returns how many operations have been recorded so far.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.entries)
}

// Append records one operation and the call site it came from.
func (r *Recorder) Append(op Operation, src Source) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed != nil {
		return fmt.Errorf("%s (%s): %w", r.migration, r.direction, ErrSealed)
	}

	entry := Entry{Index: len(r.entries), Op: op, Source: src}

	if op == nil {
		return attribute(r.migration, r.direction, entry, errors.New("operation is nil"))
	}

	if err := op.Validate(); err != nil {
		return attribute(r.migration, r.direction, entry, err)
	}

	clone, err := cloneOp(op)
	if err != nil {
		return attribute(r.migration, r.direction, entry, err)
	}

	entry.Op = clone
	r.entries = append(r.entries, entry)

	return nil
}

// Seal closes the recording and returns it. Calling it again returns the same recording.
func (r *Recorder) Seal() (*Recording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed != nil {
		return r.sealed, nil
	}

	r.sealed = &Recording{
		migration: r.migration,
		direction: r.direction,
		entries:   slices.Clone(r.entries),
	}

	return r.sealed, nil
}

// Recording is a sealed, ordered list of operations with the source of each.
type Recording struct {
	migration string
	direction Direction
	entries   []Entry
}

// Migration returns the migration these operations came from.
func (r *Recording) Migration() string {
	return r.migration
}

// Direction returns the direction that was recorded.
func (r *Recording) Direction() Direction {
	return r.direction
}

// Len returns how many operations the recording holds.
func (r *Recording) Len() int {
	return len(r.entries)
}

// Entries returns every recorded operation with its attribution, in order.
func (r *Recording) Entries() []Entry {
	out := make([]Entry, len(r.entries))

	for i, e := range r.entries {
		out[i] = Entry{Index: e.Index, Op: mustClone(e.Op), Source: e.Source}
	}

	return out
}

// Operations returns just the operations, in order, for compiling or hashing.
func (r *Recording) Operations() Operations {
	out := make(Operations, len(r.entries))
	for i, e := range r.entries {
		out[i] = mustClone(e.Op)
	}

	return out
}

// At returns the entry at an index, and false when the index is out of range.
func (r *Recording) At(index int) (Entry, bool) {
	if index < 0 || index >= len(r.entries) {
		return Entry{}, false
	}

	e := r.entries[index]

	return Entry{Index: e.Index, Op: mustClone(e.Op), Source: e.Source}, true
}

// Wrap attributes a failure found later to the operation at an index.
func (r *Recording) Wrap(index int, err error) error {
	if err == nil {
		return nil
	}

	entry, ok := r.At(index)
	if !ok {
		return fmt.Errorf("%s (%s): %w", r.migration, r.direction, err)
	}

	return attribute(r.migration, r.direction, entry, err)
}

// Document wraps the recorded operations in the versioned envelope that goes on the wire.
func (r *Recording) Document() *Document {
	return NewDocument(r.Operations())
}

// MarshalJSON renders the versioned document. Attribution is engine-side, never on the wire.
func (r *Recording) MarshalJSON() ([]byte, error) {
	return Marshal(r.Document())
}

// cloneOp copies an operation through its canonical form, so no slice stays shared.
func cloneOp(op Operation) (Operation, error) {
	encoded, err := Marshal(op)
	if err != nil {
		return nil, fmt.Errorf("copying the operation: %w", err)
	}

	clone, err := UnmarshalOperation(encoded)
	if err != nil {
		return nil, fmt.Errorf("copying the operation: %w", err)
	}

	return clone, nil
}

// mustClone copies an operation that Append already proved can round-trip.
func mustClone(op Operation) Operation {
	clone, err := cloneOp(op)
	if err != nil {
		return op
	}

	return clone
}
