package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// liveType builds the column type a catalog answered exactly, which is what a real driver reports.
func liveType(t *testing.T, spelling, native string) schema.ColumnType {
	t.Helper()

	logical, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	built, err := schema.ExactType(logical, native)
	if err != nil {
		t.Fatalf("ExactType: %v", err)
	}

	return built
}

// guessedType builds the column type a catalog could not resolve, which three drivers do report.
func guessedType(t *testing.T, spelling, native string) schema.ColumnType {
	t.Helper()

	logical, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	built, err := schema.AmbiguousType(logical, native)
	if err != nil {
		t.Fatalf("AmbiguousType: %v", err)
	}

	return built
}

// usersSchema is the database most of these cases are judged against: one table and one index.
func usersSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	return &schema.Snapshot{Tables: []schema.Table{{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: liveType(t, "int64", "bigint")},
			{Name: "email", Type: liveType(t, "string(255)", "character varying(255)")},
			{Name: "note", Type: liveType(t, "text", "text"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
		Indexes: []schema.Index{
			{Name: "idx_users_email", Columns: []string{"email"}},
		},
	}}}
}

// mustType parses a logical type for an operation's own payload.
func mustType(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	return parsed
}

func TestNoSnapshotIsJudgedAgainstNothing(t *testing.T) {
	found := Unrestorable(ir.Operations{ir.DropTable{Table: "users"}}, nil)

	if len(found) != 0 {
		t.Errorf("a session that described no schema produced %d disagreements", len(found))
		t.Log("A real driver answers a snapshot or an error, so this is what a fake that models no")
		t.Log("schema gets: nothing judged, rather than everything refused.")
	}
}

func TestARecordedPriorDefinitionTheLiveSchemaDeniesIsStated(t *testing.T) {
	live := usersSchema(t)

	cases := []struct {
		name string
		op   ir.Operation
		says string
	}{
		{
			name: "a column recorded as the wrong type",
			op: ir.DropColumn{Table: "users", Column: "email", From: &ir.Column{
				Name: "email", Type: mustType(t, "string(64)"),
			}},
			says: "string(64)",
		},
		{
			name: "a column recorded with the wrong nullability",
			op: ir.DropColumn{Table: "users", Column: "note", From: &ir.Column{
				Name: "note", Type: mustType(t, "text"),
			}},
			says: "accepts null",
		},
		{
			name: "a column recorded with a default this schema does not hold",
			op: ir.DropColumn{Table: "users", Column: "note", From: &ir.Column{
				Name: "note", Type: mustType(t, "text"), Nullable: true,
				Default: ir.Literal("x"),
			}},
			says: "holds none",
		},
		{
			name: "a prior column state recording the wrong type",
			op: ir.AlterColumn{Table: "users", Column: "email", Nullable: boolAt(true),
				From: &ir.ColumnState{Type: typeAt(t, "text")}},
			says: "and this schema holds string(255)",
		},
		{
			name: "a prior column state the live column denies",
			op: ir.AlterColumn{Table: "users", Column: "note", Nullable: boolAt(false),
				From: &ir.ColumnState{Nullable: boolAt(false)}},
			says: "is not null",
		},
		{
			name: "an index recorded over the wrong columns",
			op: ir.DropIndex{Table: "users", Name: "idx_users_email",
				From: &ir.ColumnsDef{Columns: []string{"note"}}},
			says: "covering (note)",
		},
		{
			name: "a table definition naming a column this table does not hold",
			op: ir.DropTable{Table: "users", From: &ir.TableDef{Columns: []ir.Column{
				{Name: "gone", Type: mustType(t, "int64")},
			}}},
			says: `names column "gone"`,
		},
		{
			name: "a primary key recorded over the wrong columns",
			op: ir.DropPrimaryKey{Table: "users", Name: "users_pkey",
				From: &ir.ColumnsDef{Columns: []string{"email"}}},
			says: "covering (email)",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			found := Unrestorable(ir.Operations{one.op}, live)

			if len(found) == 0 {
				t.Fatal("the live schema denies this and nothing was reported")
			}

			if !slices.ContainsFunc(found, func(d Disagreement) bool {
				return d.Sense == SenseStated && strings.Contains(d.Detail, one.says)
			}) {
				t.Errorf("the disagreements %v say nothing about %q", details(found), one.says)
			}
		})
	}
}

func TestAnOperationNamingSomethingThatIsNotThereIsStated(t *testing.T) {
	live := usersSchema(t)

	cases := []struct {
		name string
		op   ir.Operation
		says string
	}{
		{"a table this schema already holds", ir.CreateTable{Table: "users",
			Columns: []ir.Column{{Name: "id", Type: mustType(t, "int64")}}}, "already holds a table"},
		{"a column this table already holds", ir.AddColumn{Table: "users",
			Column: ir.Column{Name: "email", Type: mustType(t, "text")}}, "already holds a column"},
		{"a column this table does not hold", ir.DropColumn{Table: "users", Column: "gone"},
			"holds no column"},
		{"a table this schema does not hold", ir.AddIndex{Table: "orders", Name: "i",
			Columns: []string{"id"}}, "holds no table"},
		{"an index name already taken", ir.AddIndex{Table: "users", Name: "idx_users_email",
			Columns: []string{"note"}}, "a name that is taken"},
		{"an index this table does not carry", ir.DropIndex{Table: "users", Name: "nope"},
			"carries no index"},
		{"a second primary key", ir.AddPrimaryKey{Table: "users", Columns: []string{"email"}},
			"already has a primary key"},
		{"a key this table does not carry", ir.DropForeignKey{Table: "users", Name: "nope"},
			"carries no foreign key"},
		{"a reference to a table this schema does not hold", ir.AddForeignKey{Table: "users",
			Name: "fk", Columns: []string{"id"}, ReferencesTable: "orders",
			ReferencesColumns: []string{"id"}}, "which this schema does not hold"},
		{"a view this schema does not hold", ir.DropView{View: "listing"}, "holds no view"},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			found := Unrestorable(ir.Operations{one.op}, live)

			if !slices.ContainsFunc(found, func(d Disagreement) bool {
				return d.Sense == SenseStated && strings.Contains(d.Detail, one.says)
			}) {
				t.Errorf("the disagreements %v say nothing about %q", details(found), one.says)
			}
		})
	}

	t.Log("This is what catches ADR 0022's sharpest case: deriving an inverse re-emits a migration's")
	t.Log("up against a database where it is already applied, so an up that emits differently the")
	t.Log("second time yields an inverse of something that never ran -- which lands here as a")
	t.Log("create_table of a table that is already there.")
}

func TestAGuessedTypeRefusesNothing(t *testing.T) {
	live := &schema.Snapshot{Tables: []schema.Table{{
		Name:    "users",
		Columns: []schema.Column{{Name: "id", Type: guessedType(t, "int64", "INTEGER")}},
	}}}

	found := Unrestorable(ir.Operations{ir.DropColumn{Table: "users", Column: "id",
		From: &ir.Column{Name: "id", Type: mustType(t, "int32")}}}, live)

	if len(found) != 0 {
		t.Errorf("a column this engine cannot resolve exactly refused a migration: %v",
			details(found))
		t.Log("SQLite declares all three integer widths as integer, so its answer is a guess --")
		t.Log("and ADR 0065 is deliberate that a guess never decides anything (A1).")
	}
}

func TestWhatTheLiveSchemaShowsAnOperationWillChangeAndNothingRecordsIsImplied(t *testing.T) {
	live := usersSchema(t)
	live.Tables = append(live.Tables, schema.Table{
		Name:    "orders",
		Columns: []schema.Column{{Name: "user_id", Type: liveType(t, "int64", "bigint")}},
		ForeignKeys: []schema.ForeignKey{{
			Name: "fk_orders_user", Columns: []string{"user_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
		}},
	})

	cases := []struct {
		name string
		op   ir.Operation
		says string
	}{
		{
			name: "an index this column carries",
			op:   ir.DropColumn{Table: "users", Column: "email"},
			says: `index "idx_users_email"`,
		},
		{
			name: "another table's reference to this column",
			op:   ir.DropColumn{Table: "users", Column: "id"},
			says: `foreign key "fk_orders_user" on table "orders"`,
		},
		{
			name: "everything a dropped table carries",
			op:   ir.DropTable{Table: "users"},
			says: `index "idx_users_email"`,
		},
		{
			name: "the nullability a primary key implies",
			op:   ir.AddPrimaryKey{Table: "users", Columns: []string{"note"}},
			says: "makes it NOT NULL",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			found := Unrestorable(ir.Operations{one.op}, live)

			if !slices.ContainsFunc(found, func(d Disagreement) bool {
				return d.Sense == SenseImplied && strings.Contains(d.Detail, one.says)
			}) {
				t.Errorf("the disagreements %v say nothing about %q", details(found), one.says)
			}
		})
	}

	t.Log("These are the two gaps M3-T7 and M9-T7 pinned as gaps: a reversed drop_column does not")
	t.Log("bring back the index PostgreSQL dropped with the column, and a reversed add_primary_key")
	t.Log("does not undo the NOT NULL the key implied. Neither is reachable from rollback time --")
	t.Log("by then the index is gone and nothing ever recorded the nullability -- so the refusal is")
	t.Log("here, before the operation runs, and each detail names the remedy.")
}

func TestEveryImpliedFindingNamesWhatToDoInstead(t *testing.T) {
	live := usersSchema(t)

	for _, op := range []ir.Operation{
		ir.DropColumn{Table: "users", Column: "email"},
		ir.DropTable{Table: "users"},
		ir.AddPrimaryKey{Table: "users", Columns: []string{"note"}},
	} {
		found := Unrestorable(ir.Operations{op}, live)

		for _, one := range found {
			if one.Sense != SenseImplied {
				continue
			}

			if !strings.Contains(one.Detail, "explicit") {
				t.Errorf("%s says %q, which does not name the remedy", op.Kind(), one.Detail)
				t.Log("A refusal an author cannot act on is a refusal they will work around.")
			}
		}
	}
}

func TestAnOperationAnEarlierOneAlreadyMovedIsNotJudged(t *testing.T) {
	live := usersSchema(t)

	ops := ir.Operations{
		ir.CreateTable{Table: "orders", Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "int64")},
		}},
		ir.AddIndex{Table: "orders", Name: "idx_orders_id", Columns: []string{"id"}},
		ir.DropColumn{Table: "orders", Column: "id"},
	}

	if found := Unrestorable(ops, live); len(found) != 0 {
		t.Errorf("a migration that builds what it then changes was refused: %v", details(found))
		t.Log("Every judgment compares against the live schema, and an earlier operation in the")
		t.Log("same list has already moved it, so judging past one would refuse this -- which is")
		t.Log("strictly worse than missing a disagreement.")
	}
}

func TestNothingIsJudgedAfterARawStatement(t *testing.T) {
	live := usersSchema(t)

	ops := ir.Operations{
		ir.Raw{Driver: "postgres", SQL: "CREATE TABLE orders (id bigint)"},
		ir.AddIndex{Table: "orders", Name: "idx_orders_id", Columns: []string{"id"}},
	}

	if found := Unrestorable(ops, live); len(found) != 0 {
		t.Errorf("a migration whose raw statement built the table was refused: %v", details(found))
	}

	before := ir.Operations{
		ir.AddIndex{Table: "orders", Name: "idx", Columns: []string{"id"}},
		ir.Raw{Driver: "postgres", SQL: "SELECT 1"},
	}

	if found := Unrestorable(before, live); len(found) == 0 {
		t.Error("an operation before a raw statement was not judged either")
		t.Log("A raw statement can move anything, so nothing after it can be judged -- but what")
		t.Log("comes before it is judged against a schema it cannot have moved.")
	}
}

func TestEveryOperationKindIsEitherJudgedOrAccountedFor(t *testing.T) {
	unjudged := map[ir.Kind]string{
		ir.KindRaw: "a raw statement is engine SQL this package cannot read, which is why nothing " +
			"after one is judged either",
	}

	for _, kind := range ir.Kinds() {
		if _, stated := unjudged[kind]; stated {
			continue
		}

		if !slices.Contains(judgedKinds(t), kind) {
			t.Errorf("nothing in statedBy judges %s and nothing says why", kind)
			t.Log("A twenty-first operation must fail this until somebody decides which half it")
			t.Log("is in, which is what makes the coverage derived rather than remembered.")
		}
	}
}

// Twice because half the judgments are about an object being there and half about one not being:
// a create_table says nothing against a schema holding nothing, and a drop_table says nothing
// against one holding everything. Asking both is what makes the coverage derived rather than listed.

// judgedKinds lists every kind statedBy answers about, derived by asking it twice.
func judgedKinds(t *testing.T) []ir.Kind {
	t.Helper()

	var judged []ir.Kind

	for _, op := range representativeOperations(t) {
		if len(statedBy(op, &schema.Snapshot{})) > 0 || len(statedBy(op, occupiedSchema(t))) > 0 {
			judged = append(judged, op.Kind())
		}
	}

	return judged
}

// occupiedSchema holds one of everything the representative operations name, under their own names.
func occupiedSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	typed := liveType(t, "int64", "bigint")

	return &schema.Snapshot{
		Tables: []schema.Table{
			{
				Name:       "t",
				Columns:    []schema.Column{{Name: "c", Type: typed}, {Name: "d", Type: typed}},
				PrimaryKey: &schema.PrimaryKey{Columns: []string{"c"}},
				Indexes:    []schema.Index{{Name: "i", Columns: []string{"c"}}},
				Uniques:    []schema.Unique{{Name: "u", Columns: []string{"c"}}},
				Checks:     []schema.Check{{Name: "k", Expression: "c > 0"}},
				ForeignKeys: []schema.ForeignKey{{Name: "f", Columns: []string{"c"},
					ReferencesTable: "u", ReferencesColumns: []string{"c"}}},
			},
			{Name: "u", Columns: []schema.Column{{Name: "c", Type: typed}}},
		},
		Views: []schema.View{{Name: "v", Query: "SELECT 1"}},
	}
}

// representativeOperations is one operation of every kind, naming the objects occupiedSchema holds.
func representativeOperations(t *testing.T) ir.Operations {
	t.Helper()

	column := ir.Column{Name: "c", Type: mustType(t, "int64")}

	return ir.Operations{
		ir.CreateTable{Table: "t", Columns: []ir.Column{column}},
		ir.DropTable{Table: "t"},
		ir.RenameTable{From: "t", To: "u"},
		ir.AddColumn{Table: "t", Column: column},
		ir.DropColumn{Table: "t", Column: "c"},
		ir.RenameColumn{Table: "t", From: "c", To: "d"},
		ir.AlterColumn{Table: "t", Column: "c"},
		ir.AddIndex{Table: "t", Name: "i", Columns: []string{"c"}},
		ir.DropIndex{Table: "t", Name: "i"},
		ir.AddUnique{Table: "t", Name: "u", Columns: []string{"c"}},
		ir.DropUnique{Table: "t", Name: "u"},
		ir.AddCheck{Table: "t", Name: "k", Expression: "c > 0"},
		ir.DropCheck{Table: "t", Name: "k"},
		ir.AddPrimaryKey{Table: "t", Columns: []string{"c"}},
		ir.DropPrimaryKey{Table: "t"},
		ir.AddForeignKey{Table: "t", Name: "f", Columns: []string{"c"},
			ReferencesTable: "u", ReferencesColumns: []string{"c"}},
		ir.DropForeignKey{Table: "t", Name: "f"},
		ir.CreateView{View: "v", Query: "SELECT 1"},
		ir.DropView{View: "v"},
		ir.Raw{Driver: "postgres", SQL: "SELECT 1"},
	}
}

func TestTheRepresentativeSetHoldsOneOperationOfEveryKind(t *testing.T) {
	ops := representativeOperations(t)

	if len(ops) != len(ir.Kinds()) {
		t.Fatalf("the representative set holds %d operations and there are %d kinds",
			len(ops), len(ir.Kinds()))
	}

	for _, kind := range ir.Kinds() {
		if !slices.ContainsFunc(ops, func(op ir.Operation) bool { return op.Kind() == kind }) {
			t.Errorf("the representative set holds no %s", kind)
		}
	}
}

// details renders what a set of disagreements says, for a failure about what it does not.
func details(found []Disagreement) []string {
	said := make([]string, 0, len(found))
	for _, one := range found {
		said = append(said, string(one.Sense)+": "+one.Detail)
	}

	return said
}

// boolAt returns a pointer to a tri-state field's value, which is what distinguishes unset from false.
func boolAt(v bool) *bool {
	return &v
}

// typeAt returns a pointer to a recorded prior type, which an alter_column leaves nil when unset.
func typeAt(t *testing.T, spelling string) *schema.Type {
	t.Helper()

	parsed := mustType(t, spelling)

	return &parsed
}

func TestTheGateRefusesARunAndReadsTheSchemaBeforeCompilingAnything(t *testing.T) {
	conn := newRecorder(t, true)
	conn.live = usersSchema(t)

	opts, src := staged(t, "a")
	opts.Allow = Allowance{All: true}
	version := migrationAt(t, 0).Version
	src.ops[version] = ir.Operations{ir.DropColumn{Table: "users", Column: "email"}}

	_, err := Apply(t.Context(), conn, opts)

	var refusal *UnrestorableError
	if !errors.As(err, &refusal) {
		t.Fatalf("Apply returned %v, want an UnrestorableError", err)
	}

	if !errors.Is(err, ErrUnrestorable) {
		t.Error("the refusal does not unwrap to the sentinel")
	}

	if refusal.Direction != ir.Up {
		t.Errorf("the refusal names the %s half", refusal.Direction)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Errorf("the session was asked for %v, and nothing may run", conn.calls)
	}

	if !slices.Contains(conn.calls, "introspect") {
		t.Errorf("the session was asked for %v, and the schema was never read", conn.calls)
	}
}

func TestAnAuthoredDownKeepsTheImpliedHalfAndIsAskedForLazily(t *testing.T) {
	version := migrationAt(t, 0).Version

	cases := []struct {
		name    string
		authors bool
		refused bool
	}{
		{"a migration that authors its own down", true, false},
		{"a migration whose inverse migr derives", false, true},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			conn := newRecorder(t, true)
			conn.live = usersSchema(t)

			opts, src := staged(t, "a")
			opts.Allow = Allowance{All: true}
			src.ops[version] = ir.Operations{ir.DropColumn{Table: "users", Column: "email"}}

			if one.authors {
				src.down[version] = ir.Operations{ir.AddColumn{Table: "users",
					Column: ir.Column{Name: "email", Type: mustType(t, "string(255)")}}}
			}

			_, err := Apply(t.Context(), conn, opts)

			if one.refused != errors.Is(err, ErrUnrestorable) {
				t.Fatalf("Apply returned %v, and this migration %s have been refused",
					err, refusedWord(one.refused))
			}

			if !slices.Contains(src.directions, ir.Down) {
				t.Error("the source was never asked whether this migration undoes itself")
			}
		})
	}

	t.Log("migr guarantees what it derives, and an authored down is the author's own instruction --")
	t.Log("which is §7's reason for judging only the up half of what a run destroys.")
}

func TestAMigrationWithNoDisagreementIsNeverAskedWhetherItUndoesItself(t *testing.T) {
	conn := newRecorder(t, true)
	conn.live = usersSchema(t)

	opts, src := staged(t, "a")
	opts.Allow = Allowance{All: true}
	version := migrationAt(t, 0).Version
	src.ops[version] = ir.Operations{
		ir.AddIndex{Table: "users", Name: "idx_users_note", Columns: []string{"note"}},
	}

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if slices.Contains(src.directions, ir.Down) {
		t.Errorf("the source was asked for %v, and a clean migration must pay no extra round trip",
			src.directions)
		t.Log("The ask is lazy on purpose: every up would otherwise cost a second migration.execute")
		t.Log("over the wire, for a question only a finding makes worth asking.")
	}
}

func TestOnlyAnImpliedFindingIsWorthAskingAboutTheDownFor(t *testing.T) {
	conn := newRecorder(t, true)
	conn.live = usersSchema(t)

	opts, src := staged(t, "a")
	opts.Allow = Allowance{All: true}
	src.ops[migrationAt(t, 0).Version] = ir.Operations{
		ir.DropIndex{Table: "users", Name: "nope"},
	}

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrUnrestorable) {
		t.Fatalf("Apply returned %v, want the stated half to refuse", err)
	}

	if slices.Contains(src.directions, ir.Down) {
		t.Errorf("the source was asked for %v over a stated finding", src.directions)
		t.Log("An authored down permits the implied half and nothing else, so a migration with no")
		t.Log("implied finding is refused either way and the question is one worth not asking.")
	}
}

func TestARollbackIsJudgedOnItsStatedHalfAlone(t *testing.T) {
	version := migrationAt(t, 0).Version

	cases := []struct {
		name    string
		down    ir.Operations
		refused bool
	}{
		{
			name: "an inverse that would take an index nothing recorded",
			down: ir.Operations{ir.DropTable{Table: "users"}},
		},
		{
			name:    "an inverse naming something that is not there",
			down:    ir.Operations{ir.DropIndex{Table: "users", Name: "nope"}},
			refused: true,
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			conn := newRecorder(t, true)
			conn.live = usersSchema(t)
			carried(t, conn, 0)

			opts, src := staged(t, "a")
			opts.Allow = Allowance{All: true}
			src.down[version] = one.down

			_, err := Rollback(t.Context(), conn, opts)

			if one.refused != errors.Is(err, ErrUnrestorable) {
				t.Fatalf("Rollback returned %v, and this rollback %s have been refused",
					err, refusedWord(one.refused))
			}

			if one.refused && slices.Contains(conn.calls, "begin") {
				t.Errorf("the session was asked for %v, and nothing may run", conn.calls)
			}
		})
	}

	t.Log("The first case drops a table carrying an index, which is an implied disagreement on the")
	t.Log("up path and nothing at all here: a rollback is the operator's own instruction, which is")
	t.Log("why only the stated half is read for it.")
}

func TestARollbackRefusalLandsBeforeTheMigrationItIsAboutAndAfterTheOnesBeforeIt(t *testing.T) {
	conn := newRecorder(t, true)
	conn.live = usersSchema(t)
	carried(t, conn, 0, 1)

	opts, src := staged(t, "a", "b")
	src.down[migrationAt(t, 1).Version] = ir.Operations{
		ir.DropIndex{Table: "users", Name: "idx_users_email"},
	}
	src.down[migrationAt(t, 0).Version] = ir.Operations{
		ir.DropIndex{Table: "users", Name: "nope"},
	}

	result, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrUnrestorable) {
		t.Fatalf("Rollback returned %v, want the second migration refused", err)
	}

	if len(result.Runs) != 1 {
		t.Fatalf("the rollback undid %d migrations, want the one before the refusal",
			len(result.Runs))
	}

	t.Log("ADR 0022 has every refusal a rollback can make fire before its first statement, and")
	t.Log("this one cannot: the schema each inverse runs against is the one the inverses before it")
	t.Log("leave, so it is read immediately before each. What that leaves is a recorded partial")
	t.Log("rollback rather than a database between states.")
}

// refusedWord renders what a case expected, for a failure that says which way round it was.
func refusedWord(refused bool) string {
	if refused {
		return "should"
	}

	return "should not"
}

func TestAPreviewJudgesTheFirstMigrationOnly(t *testing.T) {
	conn := newRecorder(t, true)
	conn.live = usersSchema(t)

	opts, src := staged(t, "a", "b")
	opts.Allow = Allowance{All: true}

	for _, at := range []int{0, 1} {
		src.ops[migrationAt(t, at).Version] = ir.Operations{
			ir.DropIndex{Table: "users", Name: "nope"},
		}
	}

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview returned %v, and a preview refuses nothing", err)
	}

	if len(previewed.Steps) != 2 {
		t.Fatalf("the preview holds %d migrations", len(previewed.Steps))
	}

	if len(previewed.Steps[0].Unrestorable) == 0 {
		t.Error("the first migration was not judged, and it is the one about to run")
	}

	if len(previewed.Steps[1].Unrestorable) != 0 {
		t.Errorf("the second migration was judged against a schema it will not run against: %v",
			details(previewed.Steps[1].Unrestorable))
		t.Log("A preview applies nothing, so the schema every later migration would run against is")
		t.Log("the one its predecessors leave -- judging one against the schema as it is now would")
		t.Log("mark a correct migration for naming a table its predecessor creates.")
	}
}
