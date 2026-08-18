package sql

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// file is the migration every fixture here is stored under.
var file = migration.Migration{Version: "20260727T153000", Name: "add_users", Ext: Ext}

// sourceOf returns a source over a directory holding this one migration with the given body.
func sourceOf(t *testing.T, body string) *Source {
	t.Helper()

	return New(fstest.MapFS{file.Filename(): &fstest.MapFile{Data: []byte(body)}}, "postgres")
}

// emit asks a source for one direction, failing the test when it refuses.
func emit(t *testing.T, s *Source, dir ir.Direction) ir.Operations {
	t.Helper()

	emitted, err := s.Emit(t.Context(), file, dir, engine.ModeApply)
	if err != nil {
		t.Fatalf("Emit(%s): %v", dir, err)
	}

	return emitted.Operations
}

// rawSQL renders the SQL of each operation, failing the test on anything that is not a raw one.
func rawSQL(t *testing.T, ops ir.Operations) []string {
	t.Helper()

	statements := make([]string, 0, len(ops))

	for _, op := range ops {
		raw, ok := op.(ir.Raw)
		if !ok {
			t.Fatalf("a %s operation reached the plan; a .sql migration emits raw and nothing else",
				op.Kind())
		}

		statements = append(statements, raw.SQL)
	}

	return statements
}

const bothSections = `-- migr:up
CREATE TABLE users (id bigint primary key);
CREATE INDEX idx_users_id ON users (id);

-- migr:down
DROP TABLE users;
`

func TestEachStatementBecomesOneRawOperationForTheDriverItIsWrittenAgainst(t *testing.T) {
	ops := emit(t, sourceOf(t, bothSections), ir.Up)

	if got := rawSQL(t, ops); len(got) != 2 {
		t.Fatalf("the up section emitted %q, want one operation per statement", got)
	}

	for _, op := range ops {
		raw, _ := op.(ir.Raw)
		if raw.Driver != "postgres" {
			t.Errorf("a raw operation names driver %q, want the one the database is configured with",
				raw.Driver)
			t.Log("ir.Raw is driver-scoped, and every driver refuses SQL addressed to another. The")
			t.Log("name comes from databases.<name>.driver, which is where the DSN comes from too.")
		}

		if err := op.Validate(); err != nil {
			t.Errorf("an emitted operation does not validate: %v", err)
		}
	}
}

func TestAFileEmitsTheSameOperationsInEitherMode(t *testing.T) {
	for _, dir := range []ir.Direction{ir.Up, ir.Down} {
		s := sourceOf(t, bothSections)

		asked := make(map[engine.Mode][]byte, len(engine.Modes()))

		for _, mode := range engine.Modes() {
			emitted, err := s.Emit(t.Context(), file, dir, mode)
			if err != nil {
				t.Fatalf("Emit(%s, %s): %v", dir, mode, err)
			}

			if asked[mode], err = ir.Marshal(emitted.Operations); err != nil {
				t.Fatalf("marshaling what %s emitted in %s: %v", dir, mode, err)
			}
		}

		if !bytes.Equal(asked[engine.ModeApply], asked[engine.ModePlan]) {
			t.Errorf("the %s half emitted %s when applying and %s when planning",
				dir, asked[engine.ModeApply], asked[engine.ModePlan])
			t.Log("A .sql migration is a text file: it makes no callbacks, reads nothing, and cannot")
			t.Log("branch, so the mode is a parameter it is right to ignore. This is the floor the")
			t.Log("host's identical-IR proof holds a language runtime to (M6-T4).")
		}
	}
}

func TestTheDownSectionIsTheOneRolledBack(t *testing.T) {
	if got := rawSQL(t, emit(t, sourceOf(t, bothSections), ir.Down)); len(got) != 1 ||
		!strings.Contains(got[0], "DROP TABLE") {
		t.Errorf("the down section emitted %q, want only what is below the down marker", got)
		t.Log("Reading the whole file as the up section is the failure the required marker exists")
		t.Log("to prevent: an apply would create the table and then drop it again.")
	}
}

func TestAFileWithNoDownSectionAuthorsNoDown(t *testing.T) {
	for _, body := range []string{
		"-- migr:up\nCREATE TABLE users (id bigint);\n",
		"-- migr:up\nCREATE TABLE users (id bigint);\n\n-- migr:down\n",
		"-- migr:up\nCREATE TABLE users (id bigint);\n\n-- migr:down\n-- write this\n",
	} {
		_, err := sourceOf(t, body).Emit(t.Context(), file, ir.Down, engine.ModeApply)
		if !errors.Is(err, engine.ErrNoDownAuthored) {
			t.Errorf("Emit(down) returned %v, want ErrNoDownAuthored", err)
			t.Log("A marker with nothing under it is not a down someone wrote. The engine derives")
			t.Log("an inverse from this sentinel, and ir.Raw refuses to be inverted -- which is")
			t.Log("the message telling the author to write the undo (ADR 0022, C4).")
		}
	}
}

func TestAFileWithNoUpMarkerIsRefusedRatherThanReadWhole(t *testing.T) {
	_, err := sourceOf(t, "CREATE TABLE users (id bigint);\n").Emit(t.Context(), file, ir.Up, engine.ModeApply)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Emit(up) returned %v, want a refusal", err)
	}

	for _, want := range []string{file.Filename(), MarkerUp} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	t.Log("Reading an unmarked file as all-up is friendlier right until somebody writes")
	t.Log("-- migr:Up, at which point the apply runs the down section too.")
}

func TestSQLAboveTheFirstMarkerIsRefusedRatherThanDropped(t *testing.T) {
	_, err := sourceOf(t, "CREATE TABLE early (id bigint);\n-- migr:up\nSELECT 1;\n").
		Emit(t.Context(), file, ir.Up, engine.ModeApply)

	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Emit(up) returned %v, want a refusal", err)
	}

	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("the refusal does not name the line: %v", err)
	}
}

func TestACommentAboveTheFirstMarkerIsFine(t *testing.T) {
	ops := emit(t, sourceOf(t, "-- what this migration is for\n\n-- migr:up\nSELECT 1;\n"), ir.Up)

	if len(ops) != 1 {
		t.Errorf("a header comment cost the file its statements: %q", rawSQL(t, ops))
	}
}

func TestAMarkerCarriedTwiceIsRefusedNamingBothLines(t *testing.T) {
	_, err := sourceOf(t, "-- migr:up\nSELECT 1;\n-- migr:up\nSELECT 2;\n").
		Emit(t.Context(), file, ir.Up, engine.ModeApply)

	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Emit(up) returned %v, want a refusal", err)
	}

	for _, want := range []string{"lines 1 and 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

func TestASectionMarkerIsAWholeLine(t *testing.T) {
	ops := emit(t, sourceOf(t, "-- migr:up\nSELECT '-- migr:down';\n"), ir.Up)

	if got := rawSQL(t, ops); len(got) != 1 {
		t.Errorf("the up section emitted %q, want the marker inside a string left alone", got)
	}

	if _, err := sourceOf(t, "-- migr:up\nSELECT 'x';\n").Emit(t.Context(), file, ir.Down, engine.ModeApply); !errors.Is(
		err, engine.ErrNoDownAuthored) {
		t.Errorf("a marker inside a string opened a down section: %v", err)
	}
}

func TestADownMarkerAboveTheUpMarkerIsRefused(t *testing.T) {
	_, err := sourceOf(t, "-- migr:down\nDROP TABLE users;\n-- migr:up\nCREATE TABLE users ();\n").
		Emit(t.Context(), file, ir.Up, engine.ModeApply)

	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Emit(up) returned %v, want a refusal", err)
	}
}

func TestTheTemplateThisPackageWritesEmitsNothingAndAuthorsNoDown(t *testing.T) {
	s := sourceOf(t, Template)

	if ops := emit(t, s, ir.Up); len(ops) != 0 {
		t.Errorf("the template emits %q, want nothing until somebody writes it", rawSQL(t, ops))
	}

	if _, err := s.Emit(t.Context(), file, ir.Down, engine.ModeApply); !errors.Is(err, engine.ErrNoDownAuthored) {
		t.Errorf("the template's empty down returned %v, want ErrNoDownAuthored", err)
	}

	t.Log("What migr create writes is what this parses, which is the reason the writer and the")
	t.Log("reader are one package (ADR 0025, B). A template that did not parse would be a file")
	t.Log("format with two definitions, found the first time somebody ran what migr wrote.")
}

func TestAMigrationWithNoFileIsReportedByName(t *testing.T) {
	_, err := New(fstest.MapFS{}, "postgres").Emit(t.Context(), file, ir.Up, engine.ModeApply)
	if err == nil {
		t.Fatal("Emit reported success for a file that is not there")
	}

	if !strings.Contains(err.Error(), file.Filename()) {
		t.Errorf("the failure does not name the file: %v", err)
	}
}
