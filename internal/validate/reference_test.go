package validate

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
)

// columnOn builds the add_column M10-T3's done-when names, which a table may or may not hold.
func columnOn(t *testing.T, table, column string) ir.AddColumn {
	t.Helper()

	return ir.AddColumn{Table: table, Column: ir.Column{
		Name: column, Type: logical(t, "string(64)"), Nullable: true,
	}}
}

// keyTo builds a foreign key pointing at one column of one table.
func keyTo(table, name, column, referenced, referencedColumn string) ir.AddForeignKey {
	return ir.AddForeignKey{
		Table: table, Name: name, Columns: []string{column},
		ReferencesTable: referenced, ReferencesColumns: []string{referencedColumn},
	}
}

// logical parses a canonical logical type, failing the test rather than carrying a zero one.
func logical(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	return parsed
}

// keyed builds a table with an id and one more column, which a key has somewhere to point at.
func keyed(t *testing.T, name string, extra string) ir.CreateTable {
	t.Helper()

	return ir.CreateTable{Table: name, Columns: []ir.Column{
		{Name: "id", Type: logical(t, "int64")},
		{Name: extra, Type: logical(t, "int64"), Nullable: true},
	}, PrimaryKey: []string{"id"}}
}

// liveSchema builds the snapshot a connected run would have read, from operations alone.
func liveSchema(t *testing.T, ops ir.Operations) *schema.Snapshot {
	t.Helper()

	held, refusals := foldFor(t, ops)
	if len(refusals) > 0 {
		t.Fatalf("the fixture itself does not fold: %v", refusals)
	}

	return &held
}

// This is the only form the check runs in: neither basis alone is sound, so it has both or neither.

// runLive validates a staged project against a schema the database already holds.
func runLive(t *testing.T, live *schema.Snapshot, src source, found ...migration.Migration) Report {
	t.Helper()

	return runOnline(t, Options{Allow: engine.Allowance{All: true}, Schema: live}, src,
		compiler{}, found...)
}

// virgin is the schema a database that no migration has touched holds, which is nothing at all.
func virgin() *schema.Snapshot {
	return &schema.Snapshot{}
}

// referenced returns every reference problem a report holds, which is what these tests judge.
func referenced(report Report) []Problem {
	var found []Problem

	for _, problem := range report.Problems() {
		if problem.Check == CheckReference {
			found = append(found, problem)
		}
	}

	return found
}

func TestAColumnAddedToATableNothingCreatesIsCaught(t *testing.T) {
	first, second := migrationAt(1, "add_users"), migrationAt(2, "typo")

	report := runLive(t, virgin(), source{
		first.Version:  {up: ir.Operations{table(t, "users")}},
		second.Version: {up: ir.Operations{columnOn(t, "userz", "nickname")}},
	}, first, second)

	found := referenced(report)
	if len(found) != 1 {
		t.Fatalf("the run found %d reference problems, want 1:\n%v", len(found), found)
	}

	if found[0].Migration.Version != second.Version || found[0].Kind != ir.KindAddColumn {
		t.Errorf("the problem names %s (%s), want %s (add_column)", found[0].Migration.Version,
			found[0].Kind, second.Version)
	}

	if !strings.Contains(found[0].Detail, `"userz"`) {
		t.Errorf("the detail does not name the table: %q", found[0].Detail)
		t.Log("The position and the kind are the problem's own fields, so what the reason alone is")
		t.Log("missing is the object -- and a finding nobody can act on is not a finding.")
	}
}

func TestAForeignKeyToAColumnNothingAddsIsCaught(t *testing.T) {
	first, second := migrationAt(1, "add_users"), migrationAt(2, "add_orders")

	report := runLive(t, virgin(), source{
		first.Version: {up: ir.Operations{keyed(t, "users", "tenant")}},
		second.Version: {up: ir.Operations{
			keyed(t, "orders", "uid"),
			keyTo("orders", "fk_orders_uid", "uid", "users", "ghost"),
		}},
	}, first, second)

	found := referenced(report)
	if len(found) != 1 {
		t.Fatalf("the run found %d reference problems, want 1:\n%v", len(found), found)
	}

	if found[0].Kind != ir.KindAddForeignKey || found[0].Operation != 2 {
		t.Errorf("the problem names operation %d (%s), want operation 2 (add_foreign_key)",
			found[0].Operation, found[0].Kind)
	}

	if !strings.Contains(found[0].Detail, `users.ghost`) {
		t.Errorf("the detail does not name the column: %q", found[0].Detail)
	}
}

func TestAKeyToAColumnAnEarlierMigrationAddedIsNotAProblem(t *testing.T) {
	first, second := migrationAt(1, "add_users"), migrationAt(2, "add_orders")

	report := runLive(t, virgin(), source{
		first.Version: {up: ir.Operations{keyed(t, "users", "tenant")}},
		second.Version: {up: ir.Operations{
			keyed(t, "orders", "uid"),
			keyTo("orders", "fk_orders_uid", "uid", "users", "id"),
		}},
	}, first, second)

	if found := referenced(report); len(found) > 0 {
		t.Errorf("a correct project reported %d reference problems:\n%v", len(found), found)
		t.Log("The projection is what makes this correct: checking against the live catalog alone")
		t.Log("would call this broken whenever the migration creating users has not run.")
	}
}

// A migration that has already run is the other half of that failure, and it is the half the live
// catalog produces rather than cures: folding an applied create_table onto the schema it built
// refuses it for already being there. Measured on SQLite before this check existed.

func TestAnAppliedMigrationIsSilentOnceTheLiveSchemaIsRead(t *testing.T) {
	first, second := migrationAt(1, "add_users"), migrationAt(2, "add_orders")

	ops := ir.Operations{keyed(t, "users", "tenant")}
	later := ir.Operations{
		keyed(t, "orders", "uid"),
		keyTo("orders", "fk_orders_uid", "uid", "users", "id"),
	}

	staging := source{
		first.Version:  {up: ops},
		second.Version: {up: later},
	}

	live := liveSchema(t, append(append(ir.Operations{}, ops...), later...))

	report := runOnline(t, Options{Schema: live}, staging, compiler{}, first, second)

	if found := referenced(report); len(found) > 0 {
		t.Errorf("a fully applied project reported %d reference problems:\n%v", len(found), found)
		t.Log("Every one of these migrations conflicts with the live schema, because the schema is")
		t.Log("what they built. A check that reported a conflict would fail every migrated project.")
	}
}

func TestAReferenceOnlyTheDatabaseSatisfiesIsNotAProblem(t *testing.T) {
	only := migrationAt(1, "touch_legacy")

	staging := source{only.Version: {up: ir.Operations{columnOn(t, "legacy", "note")}}}
	live := liveSchema(t, ir.Operations{keyed(t, "legacy", "label")})

	if found := referenced(runLive(t, live, staging, only)); len(found) > 0 {
		t.Errorf("a table the database holds was reported as missing:\n%v", found)
		t.Log("A project may legitimately build on a table no migration of its own creates -- migr")
		t.Log("adopted into an existing database is the ordinary case -- so the live schema is one")
		t.Log("of the two bases rather than a cross-check on the project's own.")
	}
}

func TestTheCheckDoesNotRunWithoutTheDatabaseAndTheReportSaysSo(t *testing.T) {
	only := migrationAt(1, "touch_legacy")

	report := run(t, source{only.Version: {up: ir.Operations{columnOn(t, "legacy", "note")}}},
		compiler{}, only)

	if found := referenced(report); len(found) > 0 {
		t.Errorf("an offline run reported %d reference problems:\n%v", len(found), found)
	}

	if got := report.Projections(); len(got) != 0 {
		t.Errorf("an offline run names %v as what it projected from, want nothing", got)
	}

	t.Log("Neither basis alone is sound, so a run with one of them has none. The project's own")
	t.Log("beginning refuses every operation of a migration migr diff wrote -- a drift correction")
	t.Log("re-adds what the project already created -- and the live schema alone refuses every")
	t.Log("operation of every migration that has already run. Both measured.")
}

func TestTwoMigrationsCreatingOneTableAreStillCaught(t *testing.T) {
	first, second := migrationAt(1, "add_users"), migrationAt(2, "add_users_again")

	staging := source{
		first.Version:  {up: ir.Operations{table(t, "users")}},
		second.Version: {up: ir.Operations{table(t, "users")}},
	}

	found := referenced(runLive(t, virgin(), staging, first, second))
	if len(found) != 1 {
		t.Fatalf("a duplicate table found %d problems, want 1:\n%v", len(found), found)
	}

	if found[0].Migration.Version != second.Version {
		t.Errorf("the problem names the first migration rather than the duplicate")
	}

	t.Log("Both projections refuse this, so reporting what every one of them refused keeps it --")
	t.Log("which is why a conflict is not suppressed as a class. A migration that already ran is")
	t.Log("silent because its conflict is in the live projection alone, not because it is a conflict.")
}

func TestAProjectOfRawStatementsIsOneUncheckedEntryAndNothingAfterIt(t *testing.T) {
	first, second, third := migrationAt(1, "one"), migrationAt(2, "two"), migrationAt(3, "three")

	report := runLive(t, virgin(), source{
		first.Version:  {up: ir.Operations{raw("CREATE TABLE users (id bigint)")}},
		second.Version: {up: ir.Operations{columnOn(t, "nowhere", "x")}},
		third.Version:  {up: ir.Operations{columnOn(t, "nowhere", "y")}},
	}, first, second, third)

	if found := referenced(report); len(found) > 0 {
		t.Errorf("a raw statement was reported as a problem rather than left unchecked:\n%v", found)
	}

	ended := endedProjections(report)
	if len(ended) != 1 {
		t.Fatalf("the run recorded %d unchecked projections, want exactly 1:\n%v", len(ended),
			report.Unchecked())
	}

	if ended[0].Migration.Version != first.Version || ended[0].Kind != ir.KindRaw {
		t.Errorf("the entry names %s (%s), want %s (raw)", ended[0].Migration.Version,
			ended[0].Kind, first.Version)
	}

	for _, want := range []string{"not checked", "after it"} {
		if !strings.Contains(ended[0].Reason, want) {
			t.Errorf("the reason does not say %q: %q", want, ended[0].Reason)
		}
	}

	t.Log("Engine SQL has no readable effect, so every schema after it is unknowable -- and the")
	t.Log("fold carries on regardless, which would judge the two migrations after this one against")
	t.Log("a schema that is quietly wrong. Every statement in a .sql migration is one of these, so")
	t.Log("reporting per migration would make a 50-migration project 50 lines of the same sentence.")
}

// endedProjections returns the unchecked entries this check recorded, which name a raw operation.
func endedProjections(report Report) []Unchecked {
	var found []Unchecked

	for _, skipped := range report.Unchecked() {
		if skipped.Kind == ir.KindRaw {
			found = append(found, skipped)
		}
	}

	return found
}

func TestABrokenAuthoredDownIsCaughtAndACorrectOneIsNot(t *testing.T) {
	only := migrationAt(1, "add_users")

	correct := runLive(t, virgin(), source{only.Version: {
		up:   ir.Operations{keyed(t, "users", "tenant")},
		down: ir.Operations{ir.DropTable{Table: "users"}},
	}}, only)

	if found := referenced(correct); len(found) > 0 {
		t.Errorf("a correct authored down reported %d problems:\n%v", len(found), found)
	}

	broken := runLive(t, virgin(), source{only.Version: {
		up:   ir.Operations{keyed(t, "users", "tenant")},
		down: ir.Operations{ir.DropTable{Table: "userz"}},
	}}, only)

	found := referenced(broken)
	if len(found) != 1 {
		t.Fatalf("a broken authored down found %d problems, want 1:\n%v", len(found), found)
	}

	if found[0].Direction != ir.Down {
		t.Errorf("the problem is reported against %s, want the down half", found[0].Direction)
	}

	t.Log("The down half is folded onto the state its own up half just left, which is where a")
	t.Log("rollback meets it: a rollback runs newest first, so when this migration's down runs the")
	t.Log("schema is the state after it. Nothing else judges an authored down's references --")
	t.Log("compile is schema-blind and reverse only asks whether an inverse exists at all.")
}

func TestADerivedDownIsNotReportedTwiceForOneBrokenUp(t *testing.T) {
	only := migrationAt(1, "typo")

	report := runLive(t, virgin(),
		source{only.Version: {up: ir.Operations{columnOn(t, "userz", "nickname")}}}, only)

	found := referenced(report)
	if len(found) != 1 {
		t.Errorf("one broken operation produced %d problems:\n%v", len(found), found)
		t.Log("The down is derived from the up where its author wrote none, so a broken up makes a")
		t.Log("broken down. Two complaints about one defect is how a report stops being read.")
	}

	if len(found) > 0 && found[0].Direction != ir.Up {
		t.Errorf("the surviving problem is the %s half, want the up", found[0].Direction)
	}
}

func TestBothStartingPointsAreNamedSoNoProblemsIsNotNothingProjected(t *testing.T) {
	only := migrationAt(1, "add_users")
	staging := source{only.Version: {up: ir.Operations{table(t, "users")}}}

	got := runLive(t, virgin(), staging, only).Projections()
	if len(got) != 2 || got[0] != FromProject || got[1] != FromDatabase {
		t.Errorf("a connected run projected from %v, want both starting points in order", got)
	}
}

func TestReferenceIsOneOfTheChecksARunPerforms(t *testing.T) {
	if !CheckReference.Valid() {
		t.Fatal("the reference check is not one of the checks a run performs")
	}

	var at int

	for i, check := range Checks() {
		if check == CheckReference {
			at = i
		}
	}

	if Checks()[at-1] != CheckReverse {
		t.Errorf("reference runs after %s, want reverse", Checks()[at-1])
		t.Log("It needs both halves of a migration, and the down half is what reverse works out.")
	}
}

// foldFor folds operations onto an empty schema, which is how these tests build a live snapshot.
func foldFor(t *testing.T, ops ir.Operations) (schema.Snapshot, []string) {
	t.Helper()

	held, refusals := fold.Forward(schema.Snapshot{}, ops)

	described := make([]string, 0, len(refusals))
	for _, one := range refusals {
		described = append(described, one.Describe())
	}

	return held, described
}
