package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source"
	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/typescript"
)

// writer is one language's renderer, named so a coverage failure says which language is short.
type writer struct {
	// name is the language, which is what a failure prints.
	name string
	// render is what turns operations into that language's whole migration file.
	render func(migration.Migration, ir.Operations) (string, error)
}

// Every language a migration can be written in has to be able to write every operation a differ
// emits, so the coverage checks run over this list rather than over one renderer. A fourth language
// is a row here and nothing else. sql is deliberately absent: every .sql migration is ir.Raw, so a
// project in that format has no foldable desired schema and migr diff refuses it before reaching a
// renderer at all.

// writers are every language this package renders, which is every one migr create can write but sql.
func writers() []writer {
	return []writer{
		{name: "go", render: golang.Render},
		{name: "typescript", render: typescript.Render},
	}
}

func TestEveryLogicalTypeHasASpellingInEveryLanguage(t *testing.T) {
	for _, one := range writers() {
		t.Run(one.name, func(t *testing.T) {
			for _, kind := range schema.Kinds() {
				built := sampleType(t, kind)

				if _, err := one.render(sampleMigration(t), ir.Operations{ir.AddColumn{Table: "t",
					Column: ir.Column{Name: "c", Type: built, Nullable: true}}}); err != nil {
					t.Errorf("%s has no spelling for %s: %v", one.name, kind, err)
				}
			}
		})
	}

	t.Log("Coverage is derived from schema.Kinds(), so a nineteenth logical type fails this until")
	t.Log("every language names it -- which is a table of names plus four parameterised shapes")
	t.Log("internal/source already knows, rather than a renderer per language.")
	t.Log("It lives here because only cmd/migr and internal/cli may name a concrete source, and")
	t.Log("a check over every language has to name every one of them (invariant #1).")
}

func TestEveryLanguageThisCommandWritesCanRenderEveryOperationADifferEmits(t *testing.T) {
	for _, one := range writers() {
		t.Run(one.name, func(t *testing.T) {
			for _, kind := range ir.Kinds() {
				if kind == ir.KindRaw {
					continue
				}

				op, held := sampleOperation(t, kind)
				if !held {
					t.Errorf("no sample operation for %s", kind)

					continue
				}

				body, err := one.render(sampleMigration(t), ir.Operations{op})
				if err != nil {
					t.Errorf("%s cannot write a %s: %v", one.name, kind, err)

					continue
				}

				if strings.TrimSpace(body) == "" {
					t.Errorf("%s wrote nothing for a %s", one.name, kind)
				}
			}
		})
	}
}

func TestNoLanguageWritesTheOneOperationThatIsEngineSQL(t *testing.T) {
	for _, one := range writers() {
		t.Run(one.name, func(t *testing.T) {
			if _, err := one.render(sampleMigration(t), ir.Operations{
				ir.Raw{Driver: "sqlite", SQL: "ANALYZE"},
			}); !errors.Is(err, source.ErrNotRenderable) {
				t.Errorf("%s reported %v for engine SQL, want ErrNotRenderable", one.name, err)
			}
		})
	}

	t.Log("Which is also why there is no sql writer: every .sql migration is ir.Raw, so such a")
	t.Log("project has no foldable desired schema and migr diff refuses it before it reaches a")
	t.Log("renderer at all.")
}

// sampleType returns one logical type of each kind, parameterised ones included.
func sampleType(t *testing.T, kind schema.Kind) schema.Type {
	t.Helper()

	spellings := map[schema.Kind]string{
		schema.KindString:  "string(32)",
		schema.KindDecimal: "decimal(10,2)",
		schema.KindEnum:    "enum(up,down)",
		schema.KindArray:   "array(int64)",
	}

	spelling, parameterised := spellings[kind]
	if !parameterised {
		spelling = string(kind)
	}

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

// sampleOperation returns one operation of each kind, for the per-language coverage check.
func sampleOperation(t *testing.T, kind ir.Kind) (ir.Operation, bool) {
	t.Helper()

	known := pointerTo(false)

	held := map[ir.Kind]ir.Operation{
		ir.KindCreateTable: ir.CreateTable{Table: "t", Columns: []ir.Column{
			{Name: "id", Type: sampleType(t, schema.KindInt64)}}, PrimaryKey: []string{"id"}},
		ir.KindDropTable:   ir.DropTable{Table: "t"},
		ir.KindRenameTable: ir.RenameTable{From: "t", To: "u"},
		ir.KindAddColumn: ir.AddColumn{Table: "t", Column: ir.Column{Name: "c",
			Type: sampleType(t, schema.KindText), Nullable: true}},
		ir.KindDropColumn:     ir.DropColumn{Table: "t", Column: "c"},
		ir.KindRenameColumn:   ir.RenameColumn{Table: "t", From: "c", To: "d"},
		ir.KindAlterColumn:    ir.AlterColumn{Table: "t", Column: "c", Nullable: known},
		ir.KindAddIndex:       ir.AddIndex{Table: "t", Name: "i", Columns: []string{"c"}},
		ir.KindDropIndex:      ir.DropIndex{Table: "t", Name: "i"},
		ir.KindAddUnique:      ir.AddUnique{Table: "t", Name: "u", Columns: []string{"c"}},
		ir.KindDropUnique:     ir.DropUnique{Table: "t", Name: "u"},
		ir.KindAddCheck:       ir.AddCheck{Table: "t", Name: "k", Expression: "c > 0"},
		ir.KindDropCheck:      ir.DropCheck{Table: "t", Name: "k"},
		ir.KindAddPrimaryKey:  ir.AddPrimaryKey{Table: "t", Name: "p", Columns: []string{"c"}},
		ir.KindDropPrimaryKey: ir.DropPrimaryKey{Table: "t", Name: "p"},
		ir.KindAddForeignKey: ir.AddForeignKey{Table: "t", Name: "f", Columns: []string{"c"},
			ReferencesTable: "u", ReferencesColumns: []string{"id"}},
		ir.KindDropForeignKey: ir.DropForeignKey{Table: "t", Name: "f"},
		ir.KindCreateView:     ir.CreateView{View: "v", Query: "SELECT 1"},
		ir.KindDropView:       ir.DropView{View: "v"},
	}

	op, ok := held[kind]

	return op, ok
}

// pointerTo returns a pointer to a value, which is how an alteration states a changed attribute.
func pointerTo[T any](v T) *T { return &v }

// sampleMigration is the file a renderer is told it is writing, which names what it declares.
func sampleMigration(t *testing.T) migration.Migration {
	t.Helper()

	m, err := migration.New(noon, "rendered", ".go")
	if err != nil {
		t.Fatalf("naming the migration a renderer writes: %v", err)
	}

	return m
}
