package zerodowntime

import (
	"context"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// The two migrations a case is: its setup, and the change that is measured.
const (
	setupFile  = "20261101T000000_zd_setup.sql"
	changeFile = "20261101T000100_zd_change.sql"
)

// The defaults a migr.yaml naming no safety key resolves to, which is what an operator's first run uses --
// all but statementBound. The suite judges what a client waits and never how long a statement runs, and
// measured, MySQL's online NOT NULL rebuild of SeededRows under the load runs past the default 30s.
const (
	chunkRows       = 1000
	chunkPause      = 100 * time.Millisecond
	lockBackoff     = 500 * time.Millisecond
	lockRetries     = 5
	statementBound  = 10 * time.Minute
	captureLagBound = 5 * time.Second
)

// unattendedBound is how long an unattended shadow case may hold before it is called stuck: ADR 0096 found it never cut over.
const unattendedBound = 3 * time.Minute

// staged is a case's two migrations, answering a down with the engine's derivation unless the case authors one.
type staged struct {
	// setup and change are what each migration emits going up.
	setup, change ir.Operations
	// authored reports a change whose down is down, rather than derived.
	authored bool
	// down is that authored down.
	down ir.Operations
	// undo is the setup's authored down, nil where the engine derives it.
	undo ir.Operations
}

// Emit returns what a staged migration performs in a direction.
func (s staged) Emit(_ context.Context, m migration.Migration, dir ir.Direction, _ engine.Mode,
) (engine.Emitted, error) {
	isChange := m.Filename() == changeFile

	switch {
	case dir == ir.Down && isChange && s.authored:
		return engine.Emitted{Operations: s.down}, nil
	case dir == ir.Down && !isChange && s.undo != nil:
		return engine.Emitted{Operations: s.undo}, nil
	case dir == ir.Down:
		return engine.Emitted{}, engine.ErrNoDownAuthored
	case isChange:
		return engine.Emitted{Operations: s.change}, nil
	default:
		return engine.Emitted{Operations: s.setup}, nil
	}
}

// files are the migrations a case is made of, the setup only where it has one.
func (s staged) files() ([]history.File, migration.Migration, error) {
	var found []history.File

	change, err := migration.Parse(changeFile)
	if err != nil {
		return nil, migration.Migration{}, fmt.Errorf("parsing %s: %w", changeFile, err)
	}

	if len(s.setup) > 0 {
		setup, err := migration.Parse(setupFile)
		if err != nil {
			return nil, migration.Migration{}, fmt.Errorf("parsing %s: %w", setupFile, err)
		}

		found = append(found, history.NewFile(setup, []byte("-- zero-downtime setup\n")))
	}

	return append(found, history.NewFile(change, []byte("-- zero-downtime change\n"))), change, nil
}

// optionsFor builds the options migr up builds from a migr.yaml that names no safety key.
func optionsFor(d driver.Driver, conn driver.Conn, src staged, files []history.File, online migration.Online,
	captured func(context.Context, capture.Request) (capture.Stream, error),
) engine.Options {
	caps := conn.Capabilities()
	expander, _ := conn.(driver.Expander)
	rewriter, _ := conn.(driver.Rewriter)
	chunker, _ := conn.(driver.Chunker)
	repairer, _ := conn.(driver.Repairer)
	shadower, _ := conn.(driver.Shadower)

	return engine.Options{
		Planner: plan.Planner{Compiler: d, Expander: expander, Rewriter: rewriter, Rewrites: driver.RewriteNames(),
			Capabilities: &caps, ChunkRows: chunkRows},
		Source:     src,
		Migrations: files,
		Clock:      steadyClock(),
		Runtime:    func(migration.Migration) string { return "conformance" },
		Allow:      engine.Allowance{All: true},
		Chunker:    chunker,
		Pacer:      engine.Steady{Rows: chunkRows, Pause: chunkPause},
		Backoff: engine.Exponential{Base: lockBackoff, Cap: driver.MaxLockBackoff, Retries: lockRetries,
			Jitter: func(span time.Duration) time.Duration { return span / 2 }},
		StatementTimeout: statementBound,
		Repairer:         repairer,
		Online:           online,
		Shadower:         shadower,
		Capture:          captured,
		CaptureLag:       captureLagBound,
	}
}

// steadyClock is the instant a run records, advancing a step a reading, since the engine may not read a clock.
func steadyClock() engine.Clock {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	return func() time.Time {
		now := at
		at = at.Add(time.Millisecond)

		return now
	}
}
