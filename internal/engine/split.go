package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/plan"
)

// A migration runs as a sequence of parts (ADR 0084). Consecutive statements that may share a
// transaction are one part and run inside one; a statement that declares it cannot run inside one
// is a part of its own and runs bare. Every part but the last commits on its own, so each one writes
// a dirty row saying how far the migration got -- inside its transaction where it has one, and
// straight after the statement where it does not -- and the last writes the applied row instead.
// Each of those rows takes the next ordinal, as a chunked run's do, since the history key refuses
// two at one.
//
// An operation compiled as it runs is a transactional part's own, because an expansion is held to
// statements a transaction takes. It runs inside that part's transaction, so what it reads is
// exactly what the steps before it left: the parts before it committed, and its own part's
// statements sent and not yet committed.

// errSplit is what the row a part writes records, until the migration's last part writes applied.
var errSplit = errors.New("running in parts: a statement that cannot run inside a transaction " +
	"commits on its own, and so does each run of statements around it -- this row says how far " +
	"the migration got")

// piece is a stretch of one step's statements that a part runs, or a whole step compiled as it runs.
type piece struct {
	// at is the step's place in the plan, counting from zero.
	at int
	// from and to bound the statements this piece runs, and to is -1 for a step compiled as it runs.
	from, to int
}

// part is a run of consecutive statements that agree on whether they share a transaction.
type part struct {
	// transactional reports a part that runs inside one transaction.
	transactional bool
	// pieces are what the part runs, in order.
	pieces []piece
	// last is the position of the part's last statement, counting from one.
	last int32
	// known reports that last is exact, which a step compiled as it runs before or inside it ends.
	known bool
}

// partsOf divides a plan's steps into the parts it runs as, before any of them runs.
func partsOf(steps []plan.Step) []part {
	var (
		parts    []part
		position int32
		known    = true
	)

	for at, step := range steps {
		if step.Deferred {
			parts = extend(parts, true, piece{at: at, to: -1})
			known = false
			parts[len(parts)-1].known = false

			continue
		}

		for i, stmt := range step.Statements {
			position++
			parts = extend(parts, transactional(stmt), piece{at: at, from: i, to: i + 1})
			parts[len(parts)-1].last = position
			parts[len(parts)-1].known = known
		}
	}

	if len(parts) == 0 {
		return []part{{transactional: true, known: true}}
	}

	return parts
}

// extend adds a piece to the part it belongs in, opening one where the transaction requirement changes.
func extend(parts []part, transactional bool, p piece) []part {
	if len(parts) == 0 || parts[len(parts)-1].transactional != transactional {
		parts = append(parts, part{transactional: transactional, known: true})
	}

	last := &parts[len(parts)-1]
	last.pieces = append(last.pieces, p)

	return parts
}

// Parts returns how many parts a compiled migration runs as, each committing on its own (ADR 0084).
func Parts(compiled plan.Plan) int {
	return len(partsOf(compiled.Steps))
}

// splits reports a migration that runs as more than one part, or outside any transaction at all.
func splits(compiled plan.Plan) bool {
	parts := partsOf(compiled.Steps)

	return len(parts) > 1 || !parts[0].transactional
}

// runMigration runs a migration part by part, reporting how many statements a failure left behind.
func (e *executor) runMigration(ctx context.Context, ready *prepared, rec *history.Record) (int32, error) {
	e.committed = e.from
	parts := partsOf(ready.compiled.Steps)

	for i, p := range parts {
		if p.known && p.last > 0 && p.last <= e.from {
			e.position = p.last

			continue
		}

		if err := e.runPart(ctx, ready, rec, p, i == len(parts)-1); err != nil {
			return e.durable(err), err
		}
	}

	return 0, nil
}

// runPart runs one part, in a transaction of its own or bare.
func (e *executor) runPart(ctx context.Context, ready *prepared, rec *history.Record, p part, last bool) error {
	if p.transactional {
		return e.runTransactional(ctx, ready, rec, p, last)
	}

	return e.runBare(ctx, ready, rec, p, last)
}

// runTransactional runs a part inside one transaction, which commits the row saying how far it got.
func (e *executor) runTransactional(ctx context.Context, ready *prepared, rec *history.Record, p part,
	last bool,
) error {
	marked := false

	err := e.conn.Transact(ctx, func(ctx context.Context) error {
		if err := e.runPieces(ctx, ready, p); err != nil {
			return err
		}

		if last {
			return e.recorder(rec)(ctx)
		}

		wrote, err := e.markPart(ctx, rec)
		marked = wrote

		return err
	})
	if err != nil {
		return err
	}

	e.advance(ready, rec, marked)

	return nil
}

// runPieces runs what one part holds, compiling a step against the database where it was deferred.
func (e *executor) runPieces(ctx context.Context, ready *prepared, p part) error {
	for _, pc := range p.pieces {
		if pc.to < 0 {
			if err := e.runStep(ctx, ready, pc.at); err != nil {
				return err
			}

			continue
		}

		if err := e.exec(ctx, ready.compiled.Steps[pc.at].Op, ready.compiled.Steps[pc.at].Statements[pc.from:pc.to]); err != nil {
			return err
		}
	}

	return nil
}

// runBare runs a part outside any transaction, recording how far it got after every statement.
func (e *executor) runBare(ctx context.Context, ready *prepared, rec *history.Record, p part, last bool) error {
	for i, pc := range p.pieces {
		if err := e.exec(ctx, ready.compiled.Steps[pc.at].Op, ready.compiled.Steps[pc.at].Statements[pc.from:pc.to]); err != nil {
			return err
		}

		if e.position <= e.committed {
			continue
		}

		if err := e.recordBare(ctx, ready, rec, last && i == len(p.pieces)-1); err != nil {
			return err
		}
	}

	return nil
}

// recordBare writes the row after a statement that ran outside a transaction: applied where it was the last.
func (e *executor) recordBare(ctx context.Context, ready *prepared, rec *history.Record, final bool) error {
	if final {
		if err := e.recorder(rec)(ctx); err != nil {
			return err
		}

		e.committed = e.position

		return nil
	}

	marked, err := e.markPart(ctx, rec)
	if err != nil {
		return err
	}

	e.advance(ready, rec, marked)

	return nil
}

// markPart writes the dirty row saying how far a migration got, where it got further than the last one said.
func (e *executor) markPart(ctx context.Context, rec *history.Record) (bool, error) {
	if e.position <= e.committed {
		return false, nil
	}

	marker := *rec
	marker.Duration = elapsed(marker.AppliedAt, e.opts.Clock())

	if err := e.store.MarkDirty(ctx, marker, e.position, errSplit); err != nil {
		return false, fmt.Errorf("recording that statement %d of %s ran: %w", e.position,
			marker.Version, err)
	}

	return true, nil
}

// advance notes that what a part ran is committed, and moves the row's ordinal past one it wrote.
func (e *executor) advance(ready *prepared, rec *history.Record, marked bool) {
	e.committed = e.position

	if marked {
		e.shift++
		rec.Ordinal = e.ordinalOf(ready.item)
	}
}

// ranBare reports a failure of a statement that ran outside any transaction, which nothing rolled back.
func ranBare(err error) bool {
	var failure *StatementError

	return errors.As(err, &failure) && failure.Statement.NoTransaction
}

// notRetriedBare explains why a lock wait spent outside a transaction ends the run rather than retrying.
func notRetriedBare(err error) error {
	if !retryable(err) {
		return err
	}

	return fmt.Errorf("%w -- it ran outside a transaction, so nothing rolled back whatever it left "+
		"behind, and it is not tried again (ADR 0084)", err)
}

// checkExpansion refuses an expansion holding a statement that cannot run inside the transaction it expands in.
func checkExpansion(stmts []driver.Statement) error {
	for _, stmt := range stmts {
		if stmt.NoTransaction {
			return fmt.Errorf("%w: an operation compiled as it runs is expanded inside the "+
				"transaction its part runs in, and one of its statements declares it cannot run "+
				"inside a transaction -- nothing can split a migration around a statement that did "+
				"not exist when the migration was divided into parts", ErrNotAtomic)
		}
	}

	return nil
}
