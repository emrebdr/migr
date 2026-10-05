package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// restorable refuses a migration the live schema shows cannot be undone as recorded, when running.
func (e *executor) restorable(ctx context.Context, m migration.Migration, dir ir.Direction,
	ops ir.Operations,
) error {
	if e.mode != ModeApply {
		return nil
	}

	found, err := e.disagreements(ctx, ops)
	if err != nil {
		return err
	}

	kept, err := e.permitted(ctx, m, dir, found)
	if err != nil || len(kept) == 0 {
		return err
	}

	return &UnrestorableError{Migration: m, Version: m.Version, Direction: dir, Found: kept}
}

// disagreements reads the schema this half will run against and reports what it contradicts.
func (e *executor) disagreements(ctx context.Context, ops ir.Operations) ([]Disagreement, error) {
	live, err := e.conn.Introspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the schema this migration will run against: %w", err)
	}

	return Unrestorable(ops, live), nil
}

// The source is asked what a migration undoes only once a finding appears, so a migration with none
// pays no extra round trip. migr guarantees what it derives, and an authored down is the author's
// own instruction -- which is §7's reason for judging only the up half of what a run destroys, and
// the reason a rollback is judged on its stated half alone.

// permitted drops the implied disagreements an authored down makes the author's own business.
func (e *executor) permitted(ctx context.Context, m migration.Migration, dir ir.Direction,
	found []Disagreement,
) ([]Disagreement, error) {
	if len(found) == 0 {
		return nil, nil
	}

	if dir == ir.Down {
		return Stated(found), nil
	}

	if !Implied(found) {
		return found, nil
	}

	authored, err := e.authorsDown(ctx, m)
	if err != nil {
		return nil, err
	}

	if authored {
		return Stated(found), nil
	}

	return found, nil
}

// authorsDown asks the source whether a migration undoes itself, which it answers as a question.
func (e *executor) authorsDown(ctx context.Context, m migration.Migration) (bool, error) {
	_, err := e.ask(ctx, m, ir.Down)

	switch {
	case errors.Is(err, ErrNoDownAuthored):
		return false, nil
	case errors.Is(err, ErrDeclaredIrreversible):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("asking whether this migration undoes itself, which is what "+
			"decides whether migr derives its inverse: %w", err)
	default:
		return true, nil
	}
}

// operationsOf returns the operations a compiled plan was built from, in the order they were emitted.
func operationsOf(compiled plan.Plan) ir.Operations {
	ops := make(ir.Operations, 0, len(compiled.Steps))
	for _, step := range compiled.Steps {
		ops = append(ops, step.Op)
	}

	return ops
}
