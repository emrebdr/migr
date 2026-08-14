package host

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// Call is one engine request in flight, and the only window in which callbacks are valid.
type Call struct {
	server    *Server
	migration string
	// mode is whether this call runs the migration or asks it a question, per §4.
	mode protocol.Mode
	// recorder collects what the migration emits, and is nil in a window that emits nothing.
	recorder *ir.Recorder
	// held are the escape-hatch statements a preview recorded rather than ran.
	held []driver.Query
}

// Execute opens the window in which every callback is valid, recording what the migration emits.
func (s *Server) Execute(migration string, dir ir.Direction, mode protocol.Mode) (*Call, error) {
	if !mode.Valid() {
		return nil, fmt.Errorf("%w: %q is not one of %v", ErrNoMode, mode, protocol.Modes())
	}

	recorder, err := ir.NewRecorder(migration, dir)
	if err != nil {
		return nil, err
	}

	return s.open(&Call{server: s, migration: migration, mode: mode, recorder: recorder})
}

// Validate opens the narrower window, where a migration may read and may not change anything.
func (s *Server) Validate(migration string) (*Call, error) {
	if strings.TrimSpace(migration) == "" {
		return nil, ErrNoMigration
	}

	return s.open(&Call{server: s, migration: migration, mode: protocol.ModePlan})
}

// Migration returns which migration this call is in flight for.
func (c *Call) Migration() string {
	return c.migration
}

// Mode reports whether this call runs the migration or asks it a question.
func (c *Call) Mode() protocol.Mode {
	return c.mode
}

// Operations seals what the migration emitted and closes the window.
func (c *Call) Operations() (ir.Operations, error) {
	defer c.Close()

	if c.recorder == nil {
		return nil, fmt.Errorf("%w: %s", ErrNothingRecorded, c.migration)
	}

	recording, err := c.recorder.Seal()
	if err != nil {
		return nil, err
	}

	return recording.Operations(), nil
}

// Statements returns the escape-hatch statements a preview recorded rather than ran.
func (c *Call) Statements() []driver.Query {
	c.server.mu.Lock()
	defer c.server.mu.Unlock()

	return slices.Clone(c.held)
}

// Close ends the window whether or not anything was sealed, and is safe to call more than once.
func (c *Call) Close() {
	c.server.mu.Lock()
	defer c.server.mu.Unlock()

	if c.server.call == c {
		c.server.call = nil
	}
}

// record appends every operation to the migration's list, in the order it arrived.
func (c *Call) record(ops ir.Operations) error {
	for _, op := range ops {
		if err := c.recorder.Append(op, ir.Source{}); err != nil {
			return err
		}
	}

	return nil
}

// hold keeps a statement a preview did not run, so the preview can still show it.
func (c *Call) hold(q driver.Query) {
	c.server.mu.Lock()
	defer c.server.mu.Unlock()

	c.held = append(c.held, q)
}
