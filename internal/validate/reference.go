package validate

import (
	"fmt"

	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
)

// M10-T3's done-when is that adding a column to a nonexistent table, or a foreign key to a
// nonexistent column, is caught before execution. internal/fold already answers both: Forward folds
// and does not validate, so each is a fold.Refusal naming the operation and what it could not find.
// What this file decides is what to start the fold from -- and that is the whole of the problem,
// because judging against the live catalog, the obvious answer, calls a correct migration broken
// whenever the migration creating its table has not run yet.
//
// Checking against the live catalog alone calls a correct migration broken whenever the migration
// that creates its table has not run yet. Measured on SQLite, the other direction is just as bad:
// folding a migration that has ALREADY run onto the schema it produced refuses every operation in
// it -- create_table meets a table of that name, add_index meets an index of that name. Neither
// starting point is usable by itself.
//
// So the projection runs once per starting point this run has, and a reference is reported only
// where EVERY run refused it. That is the question stated precisely: a reference that cannot be
// satisfied from the project's own beginning, and cannot be satisfied by the database as it is
// either. Three things fall out of it rather than being arranged:
//
//   - an applied migration is silent, its conflict appearing in the live run alone;
//   - a project that legitimately builds on objects no migration creates is silent once --connect
//     gives the live schema, which is what that flag buys this check;
//   - two migrations creating one table, against a database holding neither, is still caught,
//     because both runs refuse it. Suppressing conflicts outright would have lost that.

// reference reports every reference a migration makes that no projection of this project satisfies.
func (r *runner) reference(m migration.Migration, up, down ir.Operations) {
	if r.projections == nil || r.stopped {
		return
	}

	refused := r.project(up, ir.Up)

	for _, found := range refused {
		r.record(m, ir.Up, found)
	}

	if len(refused) > 0 || r.stopped {
		return
	}

	for _, found := range r.project(down, ir.Down) {
		r.record(m, ir.Down, found)
	}
}

// The down half is judged only where the up half projected cleanly, which is the same rule the other
// checks follow: a check that fails stops the ones depending on it for that migration. A down is
// derived from the up where its author wrote none, so a broken up makes a broken down -- reporting
// both is two complaints about one defect, and the up is the one to fix.

// project folds one half through every projection and returns what all of them refused.
func (r *runner) project(ops ir.Operations, dir ir.Direction) []fold.Refusal {
	if len(ops) == 0 {
		return nil
	}

	refused := make([][]fold.Refusal, 0, len(r.projections))

	for _, held := range r.projections {
		next, refusals := fold.Forward(held.held, ops)
		if dir == ir.Up {
			held.held = next
		}

		refused = append(refused, refusals)
	}

	return r.shared(refused)
}

// A down half is folded onto the state its own up half just left, which is where a rollback would
// meet it: a rollback runs newest first, so when migration N's down runs the schema is the state
// after N. What it leaves is discarded rather than kept -- fold.Forward does not mutate the snapshot
// it is given, measured over every operation that rewrites a table -- because the projection this
// run carries forward is the forward one.

// shared returns the refusals every projection made, which is the only kind this check reports.
func (r *runner) shared(refused [][]fold.Refusal) []fold.Refusal {
	if len(refused) == 0 {
		return nil
	}

	var found []fold.Refusal

	for _, one := range refused[len(refused)-1] {
		if everyRunRefused(refused, one.Index) {
			found = append(found, one)
		}
	}

	return found
}

// The refusal reported is the last projection's, which is the live one where there is a connection:
// two runs can refuse one operation for different reasons, and the state the migration will actually
// meet is the database's rather than the empty schema the project starts from.

// everyRunRefused reports whether every projection refused the operation at one position.
func everyRunRefused(refused [][]fold.Refusal, at int32) bool {
	for _, one := range refused {
		if !refusedAt(one, at) {
			return false
		}
	}

	return true
}

// refusedAt reports whether one projection refused the operation at one position.
func refusedAt(refusals []fold.Refusal, at int32) bool {
	for _, one := range refusals {
		if one.Index == at {
			return true
		}
	}

	return false
}

// record reports one refusal, or stops this check where the refusal is the one that ends a projection.
func (r *runner) record(m migration.Migration, dir ir.Direction, found fold.Refusal) {
	if found.Unfoldable() {
		r.stop(m, dir, found)

		return
	}

	r.report.add(Problem{
		Migration: m,
		Check:     CheckReference,
		Direction: dir,
		Operation: found.Index,
		Kind:      found.Kind,
		Detail:    detailOfRefusal(found),
	})
}

// Engine SQL has no readable effect, so the schema after it is not knowable by folding at all. The
// fold itself carries on -- it refuses that operation and folds the next -- which would leave every
// migration after this one judged against a schema that is quietly wrong. So the check stops, says
// so once, and says that nothing after it was judged either. A .sql project therefore reports one
// entry rather than one per migration, every statement in that format being a raw operation.

// stop ends this check for the rest of the run, recording the one operation that made it unknowable.
func (r *runner) stop(m migration.Migration, dir ir.Direction, found fold.Refusal) {
	r.stopped = true
	r.projections = nil

	r.report.skip(Unchecked{
		Migration: m,
		Direction: dir,
		Operation: found.Index,
		Kind:      found.Kind,
		Reason: fmt.Sprintf("%s -- so the references this migration makes were not checked, "+
			"and neither were any migration's after it", found.Reason),
	})
}

// The position and the kind are the problem's own fields and are rendered either side of this, so
// what is missing from the reason alone is the object: "this schema holds no such table" is not a
// finding anybody can act on until it says which table.

// detailOfRefusal names what one refusal acted on before stating what stopped it.
func detailOfRefusal(found fold.Refusal) string {
	if found.Subject == "" {
		return found.Reason
	}

	return fmt.Sprintf("%q: %s", found.Subject, found.Reason)
}

// projection is one running fold: where it started, and the schema the operations so far leave.
type projection struct {
	// from names the starting point, which is what a report says this check was able to project.
	from string
	// held is the schema the migrations folded so far leave, which the next one is judged against.
	held schema.Snapshot
}

// The two starting points, and a run has both or neither. Both are needed because neither alone is a
// sound basis -- measured, and the measurement is what makes this a connected check:
//
//   - the live schema alone refuses every operation of every migration that has already run, since
//     the schema is what they built. The live-catalog problem, reached from the other side;
//   - the project's own beginning alone refuses every operation of a migration migr diff wrote. A
//     drift correction re-adds an index the project already created and drops a column the project
//     never had, so replaying it from nothing cannot work -- measured on all three of the operations
//     that close one real gap. A check reporting those would have migr diff's own output fail
//     migr validate, which is the tool disagreeing with itself.
//
// So the check runs where it has both and does not run where it has neither. That is the shape
// capability already has: a check needing the target database says so rather than
// answering from what it could reach.
const (
	// FromProject is the projection that starts from nothing and is built by the migrations alone.
	FromProject = "the project's own beginning"
	// FromDatabase is the projection that starts from the schema the target database holds now.
	FromDatabase = "the database as it is"
)

// projectionsFor builds the running folds this run can judge against, and none without a live schema.
func projectionsFor(live *schema.Snapshot) []*projection {
	if live == nil {
		return nil
	}

	return []*projection{{from: FromProject}, {from: FromDatabase, held: *live}}
}

// Projections names every starting point a report's references were judged from, in order.
func (r Report) Projections() []string {
	return r.projections
}
