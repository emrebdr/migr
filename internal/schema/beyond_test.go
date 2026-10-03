package schema

import (
	"strings"
	"testing"
)

// The measured definitions a snapshot can see and the v1 operation set cannot express.
const (
	partialDetail   = "partial index: WHERE (nullable_one > 5)"
	generatedDetail = "generated column: GENERATED ALWAYS AS ((nullable_one * 2)) STORED"
	rowidDetail     = "WITHOUT ROWID"
	deferredDetail  = "DEFERRABLE INITIALLY DEFERRED"
	prefixDetail    = "prefix index: c01_text(10)"
	enforcedDetail  = "unenforced constraint"
	materialized    = "materialized view"
)

// markedFixture returns a snapshot in which one element of every kind is beyond the operation set.
func markedFixture(t *testing.T) Snapshot {
	t.Helper()

	marked := fixture(t)
	marked.Tables[0].Beyond = &Unrepresented{Detail: rowidDetail}
	marked.Tables[0].Columns[0].Beyond = &Unrepresented{Detail: generatedDetail}
	marked.Tables[0].PrimaryKey.Beyond = &Unrepresented{Detail: prefixDetail}
	marked.Tables[0].Indexes[0].Beyond = &Unrepresented{Detail: partialDetail}
	marked.Tables[0].Uniques[0].Beyond = &Unrepresented{Detail: prefixDetail}
	marked.Tables[0].Checks[0].Beyond = &Unrepresented{Detail: enforcedDetail}
	marked.Tables[1].ForeignKeys[0].Beyond = &Unrepresented{Detail: deferredDetail}
	marked.Views[0].Beyond = &Unrepresented{Detail: materialized}

	return marked
}

func TestASnapshotWithNoMarkersHasNothingToRefuse(t *testing.T) {
	if found := Unrepresentable(fixture(t)); len(found) != 0 {
		t.Errorf("found %d things to refuse in a schema that holds none: %v", len(found), found)
	}

	if found := Unrepresentable(Snapshot{}); len(found) != 0 {
		t.Errorf("found %d things to refuse in an empty database", len(found))
	}
}

func TestEveryMarkedElementIsFoundAndNamed(t *testing.T) {
	found := Unrepresentable(markedFixture(t))

	want := []Finding{
		{Table: "orders", Element: ElementForeignKey, Object: "fk_orders_user",
			Detail: deferredDetail},
		{Table: usersTable, Element: ElementTable, Detail: rowidDetail},
		{Table: usersTable, Element: ElementColumn, Object: "id", Detail: generatedDetail},
		{Table: usersTable, Element: ElementPrimaryKey, Object: "users_pkey",
			Detail: prefixDetail},
		{Table: usersTable, Element: ElementIndex, Object: "idx_users_nickname",
			Detail: partialDetail},
		{Table: usersTable, Element: ElementUnique, Object: "uq_users_email",
			Detail: prefixDetail},
		{Table: usersTable, Element: ElementCheck, Object: "ck_users_email",
			Detail: enforcedDetail},
		{Element: ElementView, Object: "active_users", Detail: materialized},
	}

	if len(found) != len(want) {
		t.Fatalf("found %d things to refuse, want %d:\n%v", len(found), len(want), found)
	}

	for i := range want {
		if found[i] != want[i] {
			t.Errorf("finding %d is %+v, want %+v", i+1, found[i], want[i])
		}
	}
}

func TestEveryElementKindCanBeBeyondTheOperationSet(t *testing.T) {
	seen := make(map[Element]bool)
	for _, finding := range Unrepresentable(markedFixture(t)) {
		seen[finding.Element] = true
	}

	for _, element := range []Element{ElementTable, ElementColumn, ElementPrimaryKey,
		ElementIndex, ElementUnique, ElementCheck, ElementForeignKey, ElementView} {
		if !seen[element] {
			t.Errorf("nothing reported a %s, so that kind cannot be refused at all", element)
		}
	}
}

func TestTheOrderIsTheCanonicalOneWhateverOrderTheSnapshotHolds(t *testing.T) {
	first := Unrepresentable(markedFixture(t))

	shuffled := markedFixture(t)
	shuffled.Tables[0], shuffled.Tables[1] = shuffled.Tables[1], shuffled.Tables[0]

	second := Unrepresentable(shuffled)

	if len(first) != len(second) {
		t.Fatalf("one schema in two orders reports %d and %d findings", len(first), len(second))
	}

	for i := range first {
		if first[i] != second[i] {
			t.Errorf("finding %d differs between two orderings: %+v and %+v",
				i+1, first[i], second[i])
		}
	}
}

func TestAFindingSaysWhereItIsBeforeWhatItIs(t *testing.T) {
	for _, c := range []struct {
		name  string
		given Finding
		want  string
	}{
		{"an element on a table", Finding{Table: "users", Element: ElementIndex,
			Object: "idx", Detail: partialDetail}, `index "idx" on "users": ` + partialDetail},
		{"a table itself", Finding{Table: "users", Element: ElementTable, Detail: rowidDetail},
			`table on "users": ` + rowidDetail},
		{"a view, which is on no table", Finding{Element: ElementView, Object: "v",
			Detail: materialized}, `view "v": ` + materialized},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.given.Describe(); got != c.want {
				t.Errorf("Describe() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAMarkerThatSaysNothingIsRefused(t *testing.T) {
	err := (&Unrepresented{Detail: "   "}).Validate()
	if err == nil {
		t.Fatal("a blank marker validated")
	}

	if !strings.Contains(err.Error(), "without saying what") {
		t.Errorf("error %q does not say what is wrong", err)
	}

	var absent *Unrepresented

	if err := absent.Validate(); err != nil {
		t.Errorf("an absent marker is a problem: %v", err)
	}
}

func TestAMarkedElementStillCarriesWhatItCanExpress(t *testing.T) {
	marked := markedFixture(t)

	users, found := marked.Table(usersTable)
	if !found {
		t.Fatal("the users table is missing")
	}

	if got := users.Indexes[0].Columns; len(got) != 1 || got[0] != "nickname" {
		t.Errorf("the marked index lists %v, and a reader still needs its columns", got)
	}

	if err := marked.Validate(); err != nil {
		t.Errorf("a marked snapshot is invalid: %v", err)
	}
}
