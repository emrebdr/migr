package schema

import (
	"strings"
	"testing"
)

// usersTable is the table every fixture here is built around.
const usersTable = "users"

// fixtureColumn returns one NOT NULL column of an exact logical type, as a driver would record it.
func fixtureColumn(t *testing.T, name, spelling, native string) Column {
	t.Helper()

	return Column{Name: name, Type: mustExact(t, spelling, native)}
}

// fixture returns a snapshot holding one of everything the model can represent.
func fixture(t *testing.T) Snapshot {
	t.Helper()

	return Snapshot{
		Tables: []Table{
			{
				Name: usersTable,
				Columns: []Column{
					fixtureColumn(t, "id", "int64", "bigint"),
					fixtureColumn(t, "email", "string(255)", "character varying(255)"),
					{Name: "created_at", Type: mustExact(t, "timestamptz", "timestamptz"),
						Default: &ColumnDefault{Function: FuncCurrentTimestamp}},
					{Name: "nickname", Type: mustExact(t, "string(64)", "character varying(64)"),
						Nullable: true, Default: literalOf("anonymous")},
				},
				PrimaryKey: &PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
				Indexes: []Index{
					{Name: "idx_users_nickname", Columns: []string{"nickname"}},
				},
				Uniques: []Unique{{Name: "uq_users_email", Columns: []string{"email"}}},
				Checks: []Check{
					{Name: "ck_users_email", Expression: "length(email) > 0"},
				},
			},
			{
				Name: "orders",
				Columns: []Column{
					fixtureColumn(t, "id", "int64", "bigint"),
					fixtureColumn(t, "user_id", "int64", "bigint"),
				},
				PrimaryKey: &PrimaryKey{Name: "orders_pkey", Columns: []string{"id"}},
				ForeignKeys: []ForeignKey{
					{Name: "fk_orders_user", Columns: []string{"user_id"},
						ReferencesTable: usersTable, ReferencesColumns: []string{"id"},
						OnDelete: ActionCascade},
				},
			},
		},
		Views: []View{
			{Name: "active_users", Query: "SELECT id FROM users"},
			{Name: "recent_orders", Query: "SELECT id FROM orders"},
		},
	}
}

func TestAZeroSnapshotIsAnEmptyDatabaseRatherThanAProblem(t *testing.T) {
	var empty Snapshot

	if err := empty.Validate(); err != nil {
		t.Fatalf("a zero Snapshot is invalid: %v", err)
	}

	if !empty.Empty() {
		t.Error("a zero Snapshot does not report itself empty")
	}

	if !empty.Equal(Snapshot{}) {
		t.Error("two zero Snapshots do not agree")
	}

	if fixture(t).Empty() {
		t.Error("a snapshot holding two tables reports itself empty")
	}
}

func TestTheFixtureIsAValidSnapshot(t *testing.T) {
	if err := fixture(t).Validate(); err != nil {
		t.Fatalf("the fixture is not a valid snapshot: %v", err)
	}
}

func TestATableWithNoColumnsIsWhatAnEngineCanHold(t *testing.T) {
	emptied := Snapshot{Tables: []Table{{Name: "emptied"}}}

	if err := emptied.Validate(); err != nil {
		t.Fatalf("a table PostgreSQL permits is refused here: %v", err)
	}

	held, found := emptied.Table("emptied")
	if !found {
		t.Fatal("the table is not in the snapshot that holds it")
	}

	if len(held.Columns) != 0 {
		t.Errorf("it has %d columns, want none", len(held.Columns))
	}
}

func TestLookupAnswersWhatTheSnapshotHoldsAndNothingElse(t *testing.T) {
	snapshot := fixture(t)

	users, found := snapshot.Table(usersTable)
	if !found {
		t.Fatal("the users table is missing from the fixture")
	}

	if _, found := snapshot.Table("nonesuch"); found {
		t.Error("a table nothing holds was found")
	}

	if _, found := snapshot.View("active_users"); !found {
		t.Error("the view is missing from the fixture")
	}

	if _, found := snapshot.View(usersTable); found {
		t.Error("a table was found by View, and the two are different things")
	}

	if _, found := users.Column("email"); !found {
		t.Error("the email column is missing from the users table")
	}

	if _, found := users.Column("nonesuch"); found {
		t.Error("a column nothing holds was found")
	}
}

func TestAnInvalidSnapshotIsRefusedWithTheReasonItIsInvalid(t *testing.T) {
	for _, c := range []struct {
		name  string
		given func(s *Snapshot)
		want  string
	}{
		{"a table with no name", func(s *Snapshot) { s.Tables[0].Name = " " },
			"a table has no name"},
		{"a view with no name", func(s *Snapshot) { s.Views[0].Name = "" },
			"a view has no name"},
		{"a view with no body", func(s *Snapshot) { s.Views[0].Query = "" },
			"carries no query"},
		{"two tables of one name", func(s *Snapshot) { s.Tables[1].Name = usersTable },
			`two objects claim the name "users", a table and a table`},
		{"a view named as a table is", func(s *Snapshot) { s.Views[0].Name = usersTable },
			`two objects claim the name "users", a view and a table`},
		{"a column with no name", func(s *Snapshot) { s.Tables[0].Columns[1].Name = "" },
			"a column has no name"},
		{"a column with no type", func(s *Snapshot) { s.Tables[0].Columns[1].Type = ColumnType{} },
			"zero ColumnType is not a column type"},
		{"two columns of one name", func(s *Snapshot) { s.Tables[0].Columns[1].Name = "id" },
			`two columns claim the name "id"`},
		{"a key over a column that is not there", func(s *Snapshot) {
			s.Tables[0].PrimaryKey.Columns = []string{"nonesuch"}
		}, `the primary key covers "nonesuch", which this table has no column of`},
		{"a key over no columns", func(s *Snapshot) { s.Tables[0].PrimaryKey.Columns = nil },
			"primary key names no columns"},
		{"an index with no name", func(s *Snapshot) { s.Tables[0].Indexes[0].Name = "" },
			"an index has no name"},
		{"an index naming one column twice", func(s *Snapshot) {
			s.Tables[0].Indexes[0].Columns = []string{"nickname", "nickname"}
		}, `names "nickname" more than once`},
		{"a unique constraint with no name", func(s *Snapshot) { s.Tables[0].Uniques[0].Name = "" },
			"a unique constraint has no name"},
		{"a check with no expression", func(s *Snapshot) {
			s.Tables[0].Checks[0].Expression = " "
		}, "carries no expression"},
		{"a foreign key referencing nothing", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].ReferencesTable = ""
		}, "references no table"},
		{"a foreign key whose sides cannot pair", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].ReferencesColumns = []string{"id", "id2"}
		}, "cannot pair"},
		{"a foreign key naming an action nothing knows", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].OnUpdate = ReferentialAction("explode")
		}, `names on_update "explode"`},
		{"a marker on a column that says nothing", func(s *Snapshot) {
			s.Tables[0].Columns[0].Beyond = &Unrepresented{}
		}, "without saying what"},
		{"a marker on a table that says nothing", func(s *Snapshot) {
			s.Tables[0].Beyond = &Unrepresented{Detail: "  "}
		}, "without saying what"},
		{"a marker on a view that says nothing", func(s *Snapshot) {
			s.Views[0].Beyond = &Unrepresented{}
		}, "without saying what"},
		{"a marker on an index that says nothing", func(s *Snapshot) {
			s.Tables[0].Indexes[0].Beyond = &Unrepresented{}
		}, "without saying what"},
		{"a marker on a foreign key that says nothing", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].Beyond = &Unrepresented{}
		}, "without saying what"},
	} {
		t.Run(c.name, func(t *testing.T) {
			snapshot := fixture(t)
			c.given(&snapshot)

			err := snapshot.Validate()
			if err == nil {
				t.Fatal("it validated")
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestAProblemNamesTheTableItIsIn(t *testing.T) {
	snapshot := fixture(t)
	snapshot.Tables[1].Columns[0].Type = ColumnType{}

	err := snapshot.Validate()
	if err == nil {
		t.Fatal("a column with no type validated")
	}

	if !strings.Contains(err.Error(), `table "orders"`) {
		t.Errorf("error %q does not name the table it is about", err)
	}
}

func TestAForeignKeyMayReferenceATableThisSnapshotDoesNotHold(t *testing.T) {
	snapshot := fixture(t)
	snapshot.Tables[1].ForeignKeys[0].ReferencesTable = "elsewhere"

	if err := snapshot.Validate(); err != nil {
		t.Fatalf("a cross-namespace reference is refused: %v", err)
	}
}

func TestAnUnnamedCheckOrForeignKeyIsWhatTwoEnginesReport(t *testing.T) {
	snapshot := fixture(t)
	snapshot.Tables[0].Checks[0].Name = ""
	snapshot.Tables[1].ForeignKeys[0].Name = ""

	if err := snapshot.Validate(); err != nil {
		t.Fatalf("an unnamed check or foreign key is refused: %v", err)
	}
}
