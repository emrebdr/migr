package visual_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/visual"
)

// update rewrites every golden file here from what the renderings now produce, for make golden-update.
var update = flag.Bool("update", false, "rewrite testdata/*.golden from the renderings")

// golden holds one rendering to testdata/<name>.golden, or rewrites it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (make golden-update writes it): %v", path, err)
	}

	if got != string(want) {
		t.Errorf("%s renders differently; read the difference, then make golden-update\n%s",
			path, schema.Difference(string(want), got))
	}
}

// parsed returns one migration by its filename, failing the test on a name the grammar refuses.
func parsed(t *testing.T, filename string) migration.Migration {
	t.Helper()

	m, err := migration.Parse(filename)
	if err != nil {
		t.Fatalf("Parse(%q): %v", filename, err)
	}

	return m
}

// exact returns a column type a projection holds, which is always exact.
func exact(t *testing.T, spelling string) schema.ColumnType {
	t.Helper()

	logical, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	built, err := schema.ExactType(logical, spelling)
	if err != nil {
		t.Fatalf("ExactType(%q): %v", spelling, err)
	}

	return built
}

// project is five migrations with a dependency pointing backwards in time, a phased change and a shadow.
func project(t *testing.T) ([]migration.Migration, migration.Declarations, map[string]migration.Gate) {
	t.Helper()

	found := []migration.Migration{
		parsed(t, "20261101T090000_create_users.go"),
		parsed(t, "20261101T090100_expand_user_email.go"),
		parsed(t, "20261101T090200_add_orders.ts"),
		parsed(t, "20261101T090300_contract_user_email.go"),
		parsed(t, "20261101T090400_create_products.sql"),
	}

	d := migration.NewDeclarations()
	d.Dependencies["20261101T090200"] = []string{"20261101T090400", "20261101T090000"}
	d.Phases["20261101T090100"] = migration.Phase{Change: "users.email", Name: "expand"}
	d.Phases["20261101T090300"] = migration.Phase{Change: "users.email", Name: "contract", After: "expand"}
	d.Online["20261101T090000"] = migration.ShadowTable

	ordered, err := migration.Order(found, d.Dependencies)
	if err != nil {
		t.Fatalf("Order: %v", err)
	}

	gates, err := migration.Gates(ordered, d.Phases)
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}

	return ordered, d, gates
}

func TestTheDependencyDAGRendersInEveryFormat(t *testing.T) {
	g := visual.DAG(project(t))

	golden(t, "dag.text", g.Text())
	golden(t, "dag.mermaid", g.Mermaid())
	golden(t, "dag.dot", g.DOT())
}

func TestADAGHasOneEdgePerDependencyAndOnePerPhaseGate(t *testing.T) {
	g := visual.DAG(project(t))

	want := []visual.Edge{
		{From: "20261101T090000", To: "20261101T090200", Kind: visual.EdgeDependency},
		{From: "20261101T090400", To: "20261101T090200", Kind: visual.EdgeDependency},
		{From: "20261101T090100", To: "20261101T090300", Kind: visual.EdgePhase},
	}

	if len(g.Edges) != len(want) {
		t.Fatalf("edges = %+v, want %+v", g.Edges, want)
	}

	for i := range want {
		if g.Edges[i] != want[i] {
			t.Errorf("edge %d = %+v, want %+v", i, g.Edges[i], want[i])
		}
	}

	if last := g.Nodes[len(g.Nodes)-1]; last.Version != "20261101T090200" {
		t.Errorf("the last migration in run order is %s, want the one depending on a later version",
			last.Filename)
	}
}

func TestADAGIsTheSameWhateverOrderTheDependenciesWereDeclaredIn(t *testing.T) {
	ordered, d, gates := project(t)
	first := visual.DAG(ordered, d, gates).Mermaid()

	d.Dependencies["20261101T090200"] = []string{"20261101T090000", "20261101T090400"}

	if again := visual.DAG(ordered, d, gates).Mermaid(); again != first {
		t.Errorf("reordering one migration's declared dependencies changed the drawing:\n%s",
			schema.Difference(first, again))
	}
}

// authors is the first schema the evolution builds: one keyed table holding a decimal.
func authors(t *testing.T) schema.Table {
	t.Helper()

	return schema.Table{
		Name: "authors",
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: "name", Type: exact(t, "string(100)")},
			{Name: "rating", Type: exact(t, "decimal(3,1)"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Columns: []string{"id"}},
	}
}

// books references authors and holds an enum, the second type Mermaid's grammar cannot spell.
func books(t *testing.T, withISBN bool) schema.Table {
	t.Helper()

	table := schema.Table{
		Name: "books",
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: "author_id", Type: exact(t, "int64"), Nullable: true},
			{Name: "status", Type: exact(t, "enum(draft,published)")},
		},
		PrimaryKey: &schema.PrimaryKey{Columns: []string{"id"}},
		ForeignKeys: []schema.ForeignKey{{Name: "fk_books_author", Columns: []string{"author_id"},
			ReferencesTable: "authors", ReferencesColumns: []string{"id"}}},
	}

	if withISBN {
		table.Columns = append(table.Columns, schema.Column{Name: "isbn", Type: exact(t, "string(13)"),
			Nullable: true})
		table.Uniques = []schema.Unique{{Name: "uq_books_isbn", Columns: []string{"isbn"}}}
	}

	return table
}

// between returns the operations that change one schema into the next, as migr diff would write them.
func between(t *testing.T, from, to schema.Snapshot) ir.Operations {
	t.Helper()

	ops, refused := diff.Diff(from, to, diff.Renames{})
	if len(refused) > 0 {
		t.Fatalf("diff refused %+v", refused)
	}

	return ops
}

// building is three migrations building authors and books, each one's operations the differ's own.
func building(t *testing.T) []visual.Read {
	t.Helper()

	first := schema.Snapshot{Tables: []schema.Table{authors(t)}}
	second := schema.Snapshot{Tables: []schema.Table{authors(t), books(t, false)}}
	third := schema.Snapshot{Tables: []schema.Table{authors(t), books(t, true)}}

	return []visual.Read{
		{Migration: parsed(t, "20261101T090000_create_authors.go"), Operations: between(t, schema.Snapshot{}, first)},
		{Migration: parsed(t, "20261101T090100_create_books.go"), Operations: between(t, first, second),
			Direct: 1},
		{Migration: parsed(t, "20261101T090200_add_books_isbn.ts"), Operations: between(t, second, third)},
	}
}

func TestTheSchemaEvolutionRendersEachMigrationsChangeAndTheSchemaLeft(t *testing.T) {
	e := visual.Evolve(building(t))

	if !e.Known {
		t.Fatalf("the evolution of three typed migrations is unknown, ended by %+v", e.EndedBy)
	}

	golden(t, "evolution.text", e.Text())
	golden(t, "schema.mermaid", visual.MermaidER(e.Schema))
	golden(t, "schema.dot", visual.DOTER(e.Schema))
}

func TestARawOperationEndsTheProjectionAndLaterMigrationsAreStillListed(t *testing.T) {
	reads := building(t)
	raw := visual.Read{Migration: parsed(t, "20261101T085900_create_tags.sql"),
		Operations: ir.Operations{ir.Raw{Driver: "sqlite", SQL: "CREATE TABLE tags (id integer);"}}}

	e := visual.Evolve(append([]visual.Read{raw}, reads...))

	if e.Known || e.EndedBy == nil || e.EndedBy.Filename != raw.Migration.Filename() {
		t.Fatalf("known = %v, ended by %+v; want the raw migration to end the projection", e.Known, e.EndedBy)
	}

	if len(e.Schema.Tables) != 0 {
		t.Errorf("an unknown evolution still drew a schema: %+v", e.Schema)
	}

	golden(t, "evolution.raw.text", e.Text())
}

func TestAMigrationThatReachesTheDatabaseEndsTheProjectionThere(t *testing.T) {
	reads := building(t)
	reads[1] = visual.Read{Migration: reads[1].Migration, Unread: "it reaches the database, and migr graph opens none"}

	e := visual.Evolve(reads)

	if e.Known || e.EndedBy == nil || e.EndedBy.Filename != reads[1].Migration.Filename() {
		t.Fatalf("known = %v, ended by %+v; want the second migration to end the projection", e.Known,
			e.EndedBy)
	}

	if !e.Steps[0].Known || e.Steps[2].Known {
		t.Errorf("steps known = %v, %v, %v; want only the first", e.Steps[0].Known, e.Steps[1].Known,
			e.Steps[2].Known)
	}

	golden(t, "evolution.unread.text", e.Text())
}

func TestEveryMermaidAttributeTypeIsOneTheGrammarAccepts(t *testing.T) {
	rendered := visual.MermaidER(schema.Snapshot{Tables: []schema.Table{authors(t), books(t, true)}})

	for _, line := range strings.Split(rendered, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(line, "    ") {
			continue
		}

		if strings.ContainsAny(fields[0], `,"' `) {
			t.Errorf("attribute %q carries a type Mermaid's ER grammar refuses: %q", line, fields[0])
		}
	}
}
