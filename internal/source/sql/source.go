package sql

import (
	"context"
	"io/fs"
	"strings"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Source emits the operations a .sql migration performs, reading them out of the file itself.
type Source struct {
	// fsys is the migrations directory, which a migration's Filename addresses within.
	fsys fs.FS
	// driver is the engine this file's SQL is written against, and the one every raw op names.
	driver string
}

// compile-time proof that this package satisfies the contract the executor asks its migrations through.
var _ engine.Source = (*Source)(nil)

// New returns a source over a migrations directory, addressing the SQL it finds to one driver.
func New(fsys fs.FS, driverName string) *Source {
	return &Source{fsys: fsys, driver: driverName}
}

// Emit returns the operations one migration performs, or ErrNoDownAuthored for a down it has none of.
func (s *Source) Emit(_ context.Context, m migration.Migration, dir ir.Direction) (ir.Operations, error) {
	src, err := fs.ReadFile(s.fsys, m.Filename())
	if err != nil {
		return nil, unreadable(m.Filename(), err)
	}

	sections, err := split(m.Filename(), string(src))
	if err != nil {
		return nil, err
	}

	statements, err := sections.of(m.Filename(), dir)
	if err != nil {
		return nil, err
	}

	return s.operations(statements), nil
}

// operations turns each statement into a raw operation addressed to the engine it was written for.
func (s *Source) operations(statements []string) ir.Operations {
	ops := make(ir.Operations, 0, len(statements))

	for _, statement := range statements {
		ops = append(ops, ir.Raw{Driver: s.driver, SQL: statement})
	}

	return ops
}

// sections is one file's two halves, already split into the statements each one holds.
type sections struct {
	// up are the statements that run when the migration is applied.
	up []string
	// down are the statements that run when it is rolled back, nil when the file marks no down.
	down []string
	// authored reports whether the file carries a down marker at all.
	authored bool
}

// of returns one direction's statements, reporting a down the file authored none of.
func (s sections) of(file string, dir ir.Direction) ([]string, error) {
	switch dir {
	case ir.Up:
		return s.up, nil
	case ir.Down:
		return s.undo()
	}

	return nil, unknownDirection(file, dir)
}

// undo returns the down statements, or the sentinel that has the engine derive an inverse instead.
func (s sections) undo() ([]string, error) {
	if !s.authored || len(s.down) == 0 {
		return nil, engine.ErrNoDownAuthored
	}

	return s.down, nil
}

// split divides a file at its markers, refusing every shape whose statements could run in the wrong direction.
func split(file, src string) (sections, error) {
	lines := strings.Split(src, "\n")

	up, err := markerAt(file, lines, MarkerUp)
	if err != nil {
		return sections{}, err
	}

	if up < 0 {
		return sections{}, missingMarker(file)
	}

	if err := checkPreamble(file, lines[:up]); err != nil {
		return sections{}, err
	}

	down, err := markerAt(file, lines, MarkerDown)
	if err != nil {
		return sections{}, err
	}

	if down < 0 {
		return sections{up: Statements(join(lines[up+1:]))}, nil
	}

	if down < up {
		return sections{}, markersReversed(file)
	}

	return sections{
		up:       Statements(join(lines[up+1 : down])),
		down:     Statements(join(lines[down+1:])),
		authored: true,
	}, nil
}

// markerAt returns the line a marker sits alone on, refusing a file that carries it more than once.
func markerAt(file string, lines []string, marker string) (int, error) {
	found := -1

	for i, line := range lines {
		if strings.TrimSpace(line) != marker {
			continue
		}

		if found >= 0 {
			return 0, repeatedMarker(file, marker, found+1, i+1)
		}

		found = i
	}

	return found, nil
}

// checkPreamble refuses SQL above the first marker, which belongs to no direction and would be dropped.
func checkPreamble(file string, lines []string) error {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, lineComment) {
			continue
		}

		return strayStatement(file, i+1)
	}

	return nil
}

// join puts a run of lines back together as the text they were read from.
func join(lines []string) string {
	return strings.Join(lines, "\n")
}
