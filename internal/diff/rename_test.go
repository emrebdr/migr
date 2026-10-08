package diff

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

func TestARenameIsNotProposedUntilSomebodyConfirmsIt(t *testing.T) {
	cases := []struct {
		name     string
		to       schema.Snapshot
		destroys ir.Kind
	}{
		{"a renamed table", tableRenamedTo(t, "clients"), ir.KindDropTable},
		{"a renamed column", columnRenamedTo(t, "memo"), ir.KindDropColumn},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			ops, refused := Diff(both(t), one.to, Renames{})

			if len(refused) != 0 {
				t.Fatalf("the unconfirmed reading refused something: %v", describe(refused))
			}

			if !slices.Contains(kindsOf(ops), one.destroys) {
				t.Errorf("an unconfirmed rename proposed %v, and none of it destroys anything",
					kindsOf(ops))
			}

			for _, op := range ops {
				if op.Kind() == ir.KindRenameTable || op.Kind() == ir.KindRenameColumn {
					t.Errorf("%s was proposed with nothing confirming it", op.Kind())
				}
			}
		})
	}

	t.Log("Two snapshots differing by a rename record the same thing as two differing by a drop and")
	t.Log("an unrelated create, so a differ that guessed would destroy data whenever it guessed")
	t.Log("wrong. What makes the unconfirmed reading safe is invariant #7: both of these are")
	t.Log("destructive, so migr up refuses the generated migration until the project permits them.")
}

func TestAConfirmedRenameReplacesTheDropAndTheCreate(t *testing.T) {
	cases := []struct {
		name      string
		to        schema.Snapshot
		confirmed Renames
		want      []ir.Kind
	}{
		{"a table", tableRenamedTo(t, "clients"),
			Renames{Tables: []TableRename{{From: "users", To: "clients"}}},
			[]ir.Kind{ir.KindRenameTable}},
		{"a column", columnRenamedTo(t, "memo"),
			Renames{Columns: []ColumnRename{{Table: "users", From: "note", To: "memo"}}},
			[]ir.Kind{ir.KindRenameColumn}},
		{"both at once", bothRenamed(t),
			Renames{
				Tables:  []TableRename{{From: "users", To: "clients"}},
				Columns: []ColumnRename{{Table: "clients", From: "note", To: "memo"}},
			},
			[]ir.Kind{ir.KindRenameTable, ir.KindRenameColumn}},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			ops, refused := Diff(both(t), one.to, one.confirmed)

			if len(refused) != 0 {
				t.Fatalf("a confirmed rename refused something: %v", describe(refused))
			}

			if got := kindsOf(ops); !slices.Equal(got, one.want) {
				t.Errorf("a gap that is nothing but names took %v, want %v", got, one.want)
			}
		})
	}

	t.Log("The whole feature is a pre-pass: the rename is emitted, the schema being changed is")
	t.Log("rewritten as the engine would leave it, and a differ that knows nothing about renames")
	t.Log("then finds the two schemas agree. So nothing else is proposed at all.")
}

func TestARenamedColumnTakesEveryStructuredReferenceWithIt(t *testing.T) {
	before, after := crossReferenced(t), crossRenamed(t)

	ops, refused := Diff(before, after,
		Renames{Columns: []ColumnRename{{Table: "users", From: "id", To: "user_id"}}})

	if len(refused) != 0 {
		t.Fatalf("renaming a referenced column refused something: %v", describe(refused))
	}

	if got := kindsOf(ops); !slices.Equal(got, []ir.Kind{ir.KindRenameColumn}) {
		t.Errorf("renaming a column every constraint covers took %v, want only the rename", got)
		t.Log("The key, the index, the unique, this table's foreign key and the other table's")
		t.Log("referenced columns all follow a renamed column -- measured on all five servers, each")
		t.Log("engine does exactly that by itself, so a differ that did not would propose rebuilding")
		t.Log("every one of them.")
	}
}

func TestARenamedTableTakesEveryReferenceToItWithIt(t *testing.T) {
	ops, refused := Diff(both(t), tableRenamedTo(t, "clients"),
		Renames{Tables: []TableRename{{From: "users", To: "clients"}}})

	if len(refused) != 0 {
		t.Fatalf("renaming a referenced table refused something: %v", describe(refused))
	}

	if got := kindsOf(ops); !slices.Equal(got, []ir.Kind{ir.KindRenameTable}) {
		t.Errorf("renaming a table another one references took %v, want only the rename", got)
		t.Log("orders carries a foreign key to users, and every engine repoints it by itself.")
	}
}

func TestACheckOverARenamedColumnIsReplacedRatherThanRewritten(t *testing.T) {
	before := checked(t, "total", "total > 0")
	after := checked(t, "amount", "amount > 0")

	ops, refused := Diff(before, after,
		Renames{Columns: []ColumnRename{{Table: "ledger", From: "total", To: "amount"}}})

	if len(refused) != 0 {
		t.Fatalf("a check over a renamed column refused something: %v", describe(refused))
	}

	want := []ir.Kind{ir.KindDropCheck, ir.KindRenameColumn, ir.KindAddCheck}

	if got := kindsOf(ops); !slices.Equal(got, want) {
		t.Fatalf("a check over a renamed column took %v, want %v", got, want)
	}

	t.Log("An expression is engine text (ADR 0065) and every server rewrites its own -- PostgreSQL")
	t.Log("turns (total > 0) into (amount > 0), MariaDB turns `total` > 0 into `amount` > 0. A")
	t.Log("textual substitution here would be the guess this package refuses, a column name")
	t.Log("appearing in string literals and other tables' columns too, so the check is dropped and")
	t.Log("added instead. That lands on the target schema either way.")
	t.Log("The drop coming first is also what MySQL 8.0 requires: it refuses to rename a column a")
	t.Log("check names at all (error 3959, measured), so the check has to be gone before the rename.")
}

func TestAConfirmedRenameTheSchemasDoNotSupportIsRefused(t *testing.T) {
	renamedTable := tableRenamedTo(t, "clients")
	renamedColumn := columnRenamedTo(t, "memo")

	cases := []struct {
		name      string
		to        schema.Snapshot
		confirmed Renames
		kind      ir.Kind
		says      string
	}{
		{"a table the schema being changed does not hold", renamedTable,
			Renames{Tables: []TableRename{{From: "ghosts", To: "clients"}}},
			ir.KindRenameTable, `holds no table "ghosts"`},
		{"a table the target schema does not hold", renamedTable,
			Renames{Tables: []TableRename{{From: "users", To: "ghosts"}}},
			ir.KindRenameTable, `holds no table "ghosts" to rename it to`},
		{"a table the target schema still holds", renamedTable,
			Renames{Tables: []TableRename{{From: "orders", To: "clients"}}},
			ir.KindRenameTable, `still holds a table "orders"`},
		{"a table whose new name is taken", renamedTable,
			Renames{Tables: []TableRename{{From: "users", To: "orders"}}},
			ir.KindRenameTable, "renames one onto a name that is taken"},
		{"two renames of one table", renamedTable,
			Renames{Tables: []TableRename{
				{From: "users", To: "clients"}, {From: "users", To: "clients"}}},
			ir.KindRenameTable, `another confirmed rename already renames "users"`},
		{"two renames onto one name", renamedTable,
			Renames{Tables: []TableRename{
				{From: "users", To: "clients"}, {From: "orders", To: "clients"}}},
			ir.KindRenameTable, `already renames onto "clients"`},
		{"a column of a table nothing holds", renamedColumn,
			Renames{Columns: []ColumnRename{{Table: "ghosts", From: "note", To: "memo"}}},
			ir.KindRenameColumn, `holds no table "ghosts"`},
		{"a column the table does not hold", renamedColumn,
			Renames{Columns: []ColumnRename{{Table: "users", From: "ghost", To: "memo"}}},
			ir.KindRenameColumn, `holds no column "ghost" to rename`},
		{"a column the target table does not hold", renamedColumn,
			Renames{Columns: []ColumnRename{{Table: "users", From: "note", To: "ghost"}}},
			ir.KindRenameColumn, `holds no column "ghost" to rename it to`},
		{"a column the target table still holds", renamedColumn,
			Renames{Columns: []ColumnRename{{Table: "users", From: "email", To: "memo"}}},
			ir.KindRenameColumn, `still holds a column "email"`},
		{"a column whose new name is taken", renamedColumn,
			Renames{Columns: []ColumnRename{{Table: "users", From: "note", To: "email"}}},
			ir.KindRenameColumn, "renames one onto a name that is taken"},
		{"two renames of one column", renamedColumn,
			Renames{Columns: []ColumnRename{
				{Table: "users", From: "note", To: "memo"},
				{Table: "users", From: "note", To: "memo"}}},
			ir.KindRenameColumn, `another confirmed rename already renames "note"`},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			_, refused := Diff(both(t), one.to, one.confirmed)

			if len(refused) == 0 {
				t.Fatal("a rename these two schemas cannot support was honored")
			}

			checkRefusal(t, refused, one.kind, one.says)
		})
	}

	t.Log("A confirmed rename arrives from outside, so it is checked rather than trusted -- and one")
	t.Log("this comparison cannot honor is a refusal rather than an error, because the diff still")
	t.Log("has an answer: the drop and the create it would have proposed anyway, which invariant #7")
	t.Log("gates. An operator whose schema moved under their confirmation reads it in the same list")
	t.Log("as everything else.")
}

// checkRefusal holds a refusal list to carrying one of the given kind that says the given thing.
func checkRefusal(t *testing.T, refused []Refusal, kind ir.Kind, says string) {
	t.Helper()

	for _, one := range refused {
		if one.Kind == kind && strings.Contains(one.Reason, says) {
			return
		}
	}

	t.Errorf("no %s refusal says %q:\n%v", kind, says, describe(refused))
}

func TestARefusedRenameStillLeavesTheDiffAnAnswer(t *testing.T) {
	ops, refused := Diff(both(t), columnRenamedTo(t, "memo"),
		Renames{Columns: []ColumnRename{{Table: "users", From: "ghost", To: "memo"}}})

	if len(refused) != 1 {
		t.Fatalf("want one refusal, got %v", describe(refused))
	}

	for _, want := range []ir.Kind{ir.KindDropColumn, ir.KindAddColumn} {
		if !slices.Contains(kindsOf(ops), want) {
			t.Errorf("a refused rename left no %s behind: %v", want, kindsOf(ops))
		}
	}
}

func TestOnlyAPairThatWouldOtherwiseBeADropAndACreateIsACandidate(t *testing.T) {
	cases := []struct {
		name     string
		from, to schema.Snapshot
		want     int
	}{
		{"a renamed table", both(t), tableRenamedTo(t, "clients"), 1},
		{"a renamed column", both(t), columnRenamedTo(t, "memo"), 1},
		{"two schemas that agree", both(t), both(t), 0},
		{"a column that only changed type",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "email", func(column schema.Column) schema.Column {
					column.Type = exact(t, "text")

					return column
				})
			}),
			0},
		{"a column dropped and one of another type added",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return withColumn(withoutColumn(table, "note"),
					schema.Column{Name: "phone", Type: exact(t, "string(32)"), Nullable: true})
			}),
			0},
		{"a table dropped and one sharing nothing created",
			users(t),
			schema.Snapshot{Tables: []schema.Table{{Name: "logs", Columns: []schema.Column{
				{Name: "line", Type: exact(t, "text")}}}}},
			0},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			if got := Candidates(one.from, one.to, Renames{}); len(got) != one.want {
				t.Errorf("proposed %d renames, want %d: %v", len(got), one.want, describeEach(got))
			}
		})
	}

	t.Log("The floor is what makes this a question worth asking rather than a list to skim past: a")
	t.Log("renamed column keeps its type, so type equality decides, and a renamed table keeps its")
	t.Log("columns, so a majority of them matching decides. A person shown ten implausible pairs")
	t.Log("stops reading the plausible one.")
}

func TestPairingIsOneToOneAndBestFirst(t *testing.T) {
	from := withColumns(t, users(t), map[string]schema.Column{
		"old_label": {Name: "old_label", Type: exact(t, "string(16)"), Nullable: true},
		"old_count": {Name: "old_count", Type: exact(t, "int32")},
	})
	to := withColumns(t, users(t), map[string]schema.Column{
		"new_label": {Name: "new_label", Type: exact(t, "string(16)"), Nullable: true},
		"new_count": {Name: "new_count", Type: exact(t, "int32")},
	})

	found := Candidates(from, to, Renames{})
	if len(found) != 2 {
		t.Fatalf("two losses against two gains proposed %d renames, want 2: %v",
			len(found), describeEach(found))
	}

	pairs := map[string]string{}
	for _, one := range found {
		pairs[one.From] = one.To
	}

	if pairs["old_label"] != "new_label" || pairs["old_count"] != "new_count" {
		t.Errorf("the pairing crossed the types: %v", describeEach(found))
	}

	t.Log("Four possible pairs, and two of them are proposed: a cross product would ask four")
	t.Log("questions of which two have to be answered no, which is how a confirmation prompt")
	t.Log("becomes something people hold down y through.")
}

func TestTheSameComparisonProposesTheSameCandidatesTwice(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			first := describeEach(Candidates(one.from, one.to, one.confirmed))
			second := describeEach(Candidates(reordered(one.from), reordered(one.to), one.confirmed))

			if !slices.Equal(first, second) {
				t.Errorf("the same pair in a different order proposed different renames:\n%v\n%v",
					first, second)
			}
		})
	}
}

func TestACandidateSaysWhyItIsLikelyInWordsAPersonCanCheck(t *testing.T) {
	found := Candidates(both(t), columnRenamedTo(t, "memo"), Renames{})
	if len(found) != 1 {
		t.Fatalf("want one candidate, got %v", describeEach(found))
	}

	for _, want := range []string{"users.note", "users.memo", "both are text", "both accept null"} {
		if !strings.Contains(found[0].Describe(), want) {
			t.Errorf("the candidate does not say %q:\n%s", want, found[0].Describe())
		}
	}

	if !found[0].Column() {
		t.Error("a column candidate does not report itself as one")
	}

	t.Log("Prose rather than a score, because what a person checks a rename against is their own")
	t.Log("intent: \"both are text, and both accept null\" is checkable and 0.83 is not.")
}

func TestConfirmingACandidateIsWhatMakesItAnOperation(t *testing.T) {
	to := bothRenamed(t)

	var confirmed Renames

	if !confirmed.Empty() {
		t.Error("a zero Renames does not report itself empty")
	}

	for _, one := range Candidates(both(t), to, Renames{}) {
		confirmed = confirmed.With(one)
	}

	if confirmed.Empty() {
		t.Fatal("confirming a candidate left nothing confirmed")
	}

	ops, _ := Diff(both(t), to, confirmed)
	if !slices.Contains(kindsOf(ops), ir.KindRenameTable) {
		t.Errorf("confirming the candidate did not produce its rename: %v", kindsOf(ops))
	}
}

func TestACandidateIsNotProposedForAColumnOfATableThatMayItselfBeRenamed(t *testing.T) {
	found := Candidates(both(t), bothRenamed(t), Renames{})

	for _, one := range found {
		if one.Column() {
			t.Errorf("a column rename was proposed inside a table that may itself be renamed: %s",
				one.Describe())
		}
	}

	t.Log("A candidate conditional on another candidate is not a question anybody can answer, so")
	t.Log("detection reads the unconfirmed state only: a column rename is proposed inside a table")
	t.Log("matched by name, and nowhere else. Diff still honors both where both are confirmed,")
	t.Log("which is what TestAConfirmedRenameReplacesTheDropAndTheCreate's third case is.")
}

// describe renders refusals for a failure, which is the whole of what one of them is.
func describe(refused []Refusal) []string {
	lines := make([]string, 0, len(refused))
	for _, one := range refused {
		lines = append(lines, one.Describe())
	}

	return lines
}

// describeEach renders candidates for a failure, in the order they were proposed.
func describeEach(found []Candidate) []string {
	lines := make([]string, 0, len(found))
	for _, one := range found {
		lines = append(lines, one.Describe())
	}

	return lines
}

// crossReferenced is the schema whose every constraint covers the column these cases rename.
func crossReferenced(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: exact(t, "int64")},
				{Name: "email", Type: exact(t, "string(255)")},
			},
			PrimaryKey: &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
			Indexes:    []schema.Index{{Name: "users_id_idx", Columns: []string{"id"}}},
			Uniques:    []schema.Unique{{Name: "users_id_key", Columns: []string{"id"}}},
			ForeignKeys: []schema.ForeignKey{{
				Name: "users_self_fk", Columns: []string{"id"},
				ReferencesTable: "orders", ReferencesColumns: []string{"id"},
				OnDelete: schema.ActionNoAction, OnUpdate: schema.ActionNoAction,
			}},
		},
		{
			Name:       "orders",
			Columns:    []schema.Column{{Name: "id", Type: exact(t, "int64")}},
			PrimaryKey: &schema.PrimaryKey{Name: "orders_pkey", Columns: []string{"id"}},
			ForeignKeys: []schema.ForeignKey{{
				Name: "orders_user_fk", Columns: []string{"id"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"},
				OnDelete: schema.ActionCascade, OnUpdate: schema.ActionNoAction,
			}},
		},
	}}
}

// This target is written out in full rather than built by renaming crossReferenced's column, because
// the thing that would do the renaming is the thing under test: a rewrite that forgot the key would
// forget it on both sides and compare equal. Every constraint names user_id here because every
// engine was measured repointing its own.

// crossRenamed is that schema with the referenced column renamed everywhere it appears.
func crossRenamed(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "user_id", Type: exact(t, "int64")},
				{Name: "email", Type: exact(t, "string(255)")},
			},
			PrimaryKey: &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"user_id"}},
			Indexes:    []schema.Index{{Name: "users_id_idx", Columns: []string{"user_id"}}},
			Uniques:    []schema.Unique{{Name: "users_id_key", Columns: []string{"user_id"}}},
			ForeignKeys: []schema.ForeignKey{{
				Name: "users_self_fk", Columns: []string{"user_id"},
				ReferencesTable: "orders", ReferencesColumns: []string{"id"},
				OnDelete: schema.ActionNoAction, OnUpdate: schema.ActionNoAction,
			}},
		},
		{
			Name:       "orders",
			Columns:    []schema.Column{{Name: "id", Type: exact(t, "int64")}},
			PrimaryKey: &schema.PrimaryKey{Name: "orders_pkey", Columns: []string{"id"}},
			ForeignKeys: []schema.ForeignKey{{
				Name: "orders_user_fk", Columns: []string{"id"},
				ReferencesTable: "users", ReferencesColumns: []string{"user_id"},
				OnDelete: schema.ActionCascade, OnUpdate: schema.ActionNoAction,
			}},
		},
	}}
}

// checked is the one-table schema whose only constraint is a check over the column it names.
func checked(t *testing.T, column, expression string) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{{
		Name: "ledger",
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: column, Type: exact(t, "int32"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Name: "ledger_pkey", Columns: []string{"id"}},
		Checks:     []schema.Check{{Name: "ledger_positive", Expression: expression}},
	}}}
}

// withColumns returns a schema with several more columns on its referenced table.
func withColumns(t *testing.T, held schema.Snapshot, added map[string]schema.Column) schema.Snapshot {
	t.Helper()

	names := make([]string, 0, len(added))
	for name := range added {
		names = append(names, name)
	}

	slices.Sort(names)

	return changed(held, "users", func(table schema.Table) schema.Table {
		for _, name := range names {
			table = withColumn(table, added[name])
		}

		return table
	})
}

func TestATableRenamedAlongsideItsOwnColumnsIsStillProposed(t *testing.T) {
	before := refactored(t, "users", "email_addr", "signup_date")
	after := refactored(t, "accounts", "email", "signed_up_at")

	found := Candidates(before, after, Renames{})
	if len(found) != 1 || found[0].Column() {
		t.Fatalf("the table rename was not proposed: %v", describeEach(found))
	}

	if !strings.Contains(found[0].Evidence, "by type but not by name") {
		t.Errorf("the evidence does not say what accounts for the columns: %s", found[0].Evidence)
	}

	confirmed := Renames{Tables: []TableRename{{From: "users", To: "accounts"}}}

	deeper := Candidates(before, after, confirmed)
	if len(deeper) != 2 {
		t.Fatalf("confirming the table rename proposed %d column renames, want 2: %v",
			len(deeper), describeEach(deeper))
	}

	for _, one := range deeper {
		confirmed = confirmed.With(one)
	}

	ops, refused := Diff(before, after, confirmed)
	if len(refused) != 0 {
		t.Fatalf("the whole refactor refused something: %v", describe(refused))
	}

	for _, op := range ops {
		if op.Destructive() {
			t.Errorf("a fully confirmed refactor still proposed %s: %v", op.Kind(), kindsOf(ops))
		}
	}

	t.Log("Measured by hand first, and it found nothing at all: renaming a table is exactly when a")
	t.Log("project cleans up its column names, so only id matched by name and the majority floor")
	t.Log("failed -- the operator was handed a drop_table. A column accounted for by type is what")
	t.Log("makes the table rename proposable, and confirming it is what makes the two inside it")
	t.Log("detectable (ADR 0072, B and G).")
}

func TestPairingTakesTheBestPairFirstWhereSeveralWouldFit(t *testing.T) {
	before := labeled(t, "old_label", "old_note", "spare")
	after := labeled(t, "new_label", "new_note", "extra")

	found := Candidates(before, after, Renames{})
	if len(found) != 3 {
		t.Fatalf("three losses against three gains of one type proposed %d renames, want 3: %v",
			len(found), describeEach(found))
	}

	paired := map[string]string{}
	for _, one := range found {
		if paired[one.From] != "" {
			t.Errorf("%q was paired twice: %v", one.From, describeEach(found))
		}

		paired[one.From] = one.To
	}

	for was, is := range map[string]string{
		"old_label": "new_label", "old_note": "new_note", "spare": "extra",
	} {
		if paired[was] != is {
			t.Errorf("%q paired with %q, want %q: %v", was, paired[was], is,
				describeEach(found))
		}
	}

	t.Log("Nine pairs clear the floor and three are proposed. Unsorted, the walk pairs old_label")
	t.Log("with whichever gain comes first in canonical order, so spare and extra -- the pair with")
	t.Log("nothing but position in common -- would be the one left over and mispaired.")
	t.Log("Unpaired, all nine would be asked and six of the answers would have to be no, which is")
	t.Log("how a confirmation prompt becomes something people hold down y through.")
}

func TestADroppedViewIsDroppedBeforeTheTableRenameItsBodyNames(t *testing.T) {
	before := viewed(t, "users", "SELECT id FROM users")
	after := viewed(t, "clients", "")

	ops, refused := Diff(before, after,
		Renames{Tables: []TableRename{{From: "users", To: "clients"}}})

	if len(refused) != 0 {
		t.Fatalf("dropping a view over a renamed table refused something: %v", describe(refused))
	}

	want := []ir.Kind{ir.KindDropView, ir.KindRenameTable}
	if got := kindsOf(ops); !slices.Equal(got, want) {
		t.Fatalf("dropping a view over a renamed table took %v, want %v", got, want)
	}

	undo, err := ir.ReverseAll(ops)
	if err != nil {
		t.Fatalf("ReverseAll: %v", err)
	}

	back := []ir.Kind{ir.KindRenameTable, ir.KindCreateView}
	if got := kindsOf(undo); !slices.Equal(got, back) {
		t.Fatalf("the derived down is %v, want %v", got, back)
	}

	t.Log("A view's body is engine text and is never rewritten here, so the body a drop_view records")
	t.Log("names the table by its old name. That is why drop_view is the one operation above")
	t.Log("rename_table: reversed, the create_view lands after the table has been renamed back, so")
	t.Log("the body it re-creates names a table that is there.")
}

// refactored is the table a whole rename refactor touches, under the names one side gives it.
func refactored(t *testing.T, table, email, signup string) schema.Snapshot {
	t.Helper()

	held := schema.Snapshot{Tables: []schema.Table{{
		Name: table,
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: email, Type: exact(t, "string(255)")},
			{Name: signup, Type: exact(t, "timestamptz"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Name: table + "_pkey", Columns: []string{"id"}},
	}}}

	return held.Canonical()
}

// labeled is the table whose three same-type columns are what pairing has to get right.
func labeled(t *testing.T, columns ...string) schema.Snapshot {
	t.Helper()

	table := schema.Table{
		Name:       "users",
		Columns:    []schema.Column{{Name: "id", Type: exact(t, "int64")}},
		PrimaryKey: &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
	}

	for _, name := range columns {
		table.Columns = append(table.Columns,
			schema.Column{Name: name, Type: exact(t, "string(16)"), Nullable: true})
	}

	return schema.Snapshot{Tables: []schema.Table{table}}.Canonical()
}

// viewed is the one-table schema carrying a view whose body names that table by name.
func viewed(t *testing.T, table, body string) schema.Snapshot {
	t.Helper()

	held := schema.Snapshot{Tables: []schema.Table{{
		Name:       table,
		Columns:    []schema.Column{{Name: "id", Type: exact(t, "int64")}},
		PrimaryKey: &schema.PrimaryKey{Name: table + "_pkey", Columns: []string{"id"}},
	}}}

	if body != "" {
		held.Views = []schema.View{{Name: "active", Query: body}}
	}

	return held.Canonical()
}

func TestATableMatchedByNameOutranksOneMatchedOnlyByType(t *testing.T) {
	before := schema.Snapshot{Tables: []schema.Table{
		shaped(t, "a_typed_only", "id", "heading"),
		shaped(t, "z_identical", "id", "label"),
	}}
	after := schema.Snapshot{Tables: []schema.Table{shaped(t, "winner", "id", "label")}}

	found := Candidates(before, after, Renames{})
	if len(found) != 1 {
		t.Fatalf("two lost tables against one gained proposed %d renames, want 1: %v",
			len(found), describeEach(found))
	}

	if found[0].From != "z_identical" {
		t.Errorf("the gained table was paired with %q, want %q: %v",
			found[0].From, "z_identical", describeEach(found))
	}

	t.Log("Both clear the floor -- one by two identical columns, the other by one identical and one")
	t.Log("matching by type alone -- so only the ranking can decide, and a pair agreeing on a name")
	t.Log("agrees about more than a pair agreeing on a type. The names are chosen so the tiebreak")
	t.Log("would pick the wrong one: written the other way round this passed with the ranking")
	t.Log("flattened, alphabetical order happening to give the right answer.")
}

// shaped is a two-column table under the names given, which is what ranking is measured over.
func shaped(t *testing.T, table string, key, label string) schema.Table {
	t.Helper()

	return schema.Table{
		Name: table,
		Columns: []schema.Column{
			{Name: key, Type: exact(t, "int64")},
			{Name: label, Type: exact(t, "string(16)"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Name: table + "_pkey", Columns: []string{key}},
	}
}

func TestTwoEquallyLikelyPairingsArePairedEarliestNameFirst(t *testing.T) {
	before := labeled(t, "a", "b")
	after := labeled(t, "x", "y", "z")

	found := Candidates(before, after, Renames{})
	if len(found) != 2 {
		t.Fatalf("two losses against three gains proposed %d renames, want 2: %v",
			len(found), describeEach(found))
	}

	want := []string{`column users.a -> users.x`, `column users.b -> users.y`}
	for i, one := range found {
		if !strings.HasPrefix(one.Describe(), want[i]) {
			t.Errorf("candidate %d is %q, want one starting %q", i+1, one.Describe(), want[i])
		}
	}

	t.Log("Six pairings, every one of them equally likely: same type, same nullability, same")
	t.Log("absent default, and no word shared by any pair of names. So only the tiebreak can")
	t.Log("decide which two of the six are proposed, and it decides by name -- which is what makes")
	t.Log("the list one list rather than a function of the order a catalog answered in (invariant")
	t.Log("#8). Reversed, this proposes b -> z and a -> y instead.")
}
