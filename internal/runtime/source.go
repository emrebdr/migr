package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// Discovered reports what a runtime loaded, so a project can be checked against what runs it.
func (s *Session) Discovered(ctx context.Context) ([]protocol.Discovered, error) {
	var found protocol.DiscoverResult

	if err := s.call(ctx, protocol.MigrationDiscover, struct{}{}, &found,
		s.bounds.Request); err != nil {
		return nil, fmt.Errorf("asking the %s runtime what it can run: %w",
			s.opts.Runtime.Name(), err)
	}

	return found.Migrations, nil
}

// Loaded reports what a runtime loaded, what each one declared it must follow, and what it is a step of.
func (s *Session) Loaded(ctx context.Context) ([]migration.Migration, migration.Declared,
	migration.Phases, error) {
	found, err := s.Discovered(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	loaded := make([]migration.Migration, 0, len(found))
	declared := make(migration.Declared, len(found))
	phases := make(migration.Phases)

	for _, d := range found {
		parsed, err := migration.Parse(d.File)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the %s runtime loaded %q, which discovery would refuse: %w",
				s.opts.Runtime.Name(), d.File, err)
		}

		loaded = append(loaded, parsed)

		if len(d.Dependencies) > 0 {
			declared[parsed.Version] = d.Dependencies
		}

		if d.Phase != nil {
			phases[parsed.Version] = migration.Phase{Change: d.Phase.Change, Name: d.Phase.Name,
				After: d.Phase.After}
		}
	}

	return loaded, declared, phases, nil
}

// Check runs one migration's own Validate, which emits nothing and reaches no database.
func (s *Session) Check(ctx context.Context, m migration.Migration) error {
	call, err := s.server.Validate(m.Version)
	if err != nil {
		return err
	}

	defer call.Close()

	var nothing struct{}

	if err := s.call(ctx, protocol.MigrationValidate, protocol.ValidateParams{ID: m.Version},
		&nothing, s.bounds.Request); err != nil {
		return fmt.Errorf("%s: %w", m.Filename(), err)
	}

	return nil
}

// Emit asks a runtime what one migration does, in the mode this call is a run or a question in.
func (s *Session) Emit(ctx context.Context, m migration.Migration, dir ir.Direction,
	mode engine.Mode) (engine.Emitted, error) {
	call, err := s.server.Execute(m.Version, dir, wire(mode))
	if err != nil {
		return engine.Emitted{}, err
	}

	defer call.Close()

	var reported protocol.ExecuteResult

	if err := s.call(ctx, protocol.MigrationExecute, protocol.ExecuteParams{
		ID: m.Version, Direction: dir, Mode: wire(mode),
	}, &reported, protocol.NoTimeout); err != nil {
		return engine.Emitted{}, refusal(m, err)
	}

	held := call.Statements()

	ops, err := call.Operations()
	if err != nil {
		return engine.Emitted{}, err
	}

	return engine.Emitted{Operations: ops, Direct: held},
		agree(m, reported.Operations, len(ops))
}

// wire spells an engine mode the way the protocol spells it, which a test holds the two sets to.
func wire(mode engine.Mode) protocol.Mode {
	if mode == engine.ModePlan {
		return protocol.ModePlan
	}

	return protocol.ModeApply
}

// agree holds the runtime's own count to what the host recorded, since a difference means a lost call.
func agree(m migration.Migration, reported, recorded int) error {
	if reported == recorded {
		return nil
	}

	return fmt.Errorf("%w: %s reported %d operations and the host recorded %d, so one side "+
		"dropped a host.schema.apply (§4)", ErrProtocol, m.Filename(), reported, recorded)
}

// refusal tells §7.4's two answers apart from the failures they would otherwise be reported as.
func refusal(m migration.Migration, err error) error {
	var refused *protocol.Error
	if !errors.As(err, &refused) {
		return fmt.Errorf("%s: %w", m.Filename(), err)
	}

	switch refused.Code {
	case protocol.CodeNoDownAuthored:
		return engine.ErrNoDownAuthored
	case protocol.CodeDeclaredIrreversible:
		return engine.DeclaredIrreversible(refused.Reason())
	default:
		return fmt.Errorf("%s: %w", m.Filename(), err)
	}
}
