package schema

import (
	"reflect"
	"slices"
	"testing"
)

func TestASnapshotEqualsItselfHoweverItIsOrdered(t *testing.T) {
	snapshot := fixture(t)

	shuffled := fixture(t)
	slices.Reverse(shuffled.Tables)
	slices.Reverse(shuffled.Tables[1].Columns)
	slices.Reverse(shuffled.Views)

	if !snapshot.Equal(shuffled) {
		t.Error("one schema in two orders compares unequal, so order is part of equality")
	}
}

func TestOrderIsPartOfEqualityExactlyWhereTheOperationSetCanExpressIt(t *testing.T) {
	for _, c := range []struct {
		name    string
		reorder func(s *Snapshot)
		equal   bool
	}{
		{"the table list", func(s *Snapshot) { slices.Reverse(s.Tables) }, true},
		{"a table's columns", func(s *Snapshot) { slices.Reverse(s.Tables[0].Columns) }, true},
		{"the view list", func(s *Snapshot) { slices.Reverse(s.Views) }, true},
		{"a table's indexes", func(s *Snapshot) {
			s.Tables[0].Indexes = append(s.Tables[0].Indexes,
				Index{Name: "idx_users_email", Columns: []string{"email"}})
		}, false},
		{"a primary key's columns", func(s *Snapshot) {
			s.Tables[0].PrimaryKey.Columns = []string{"id", "email"}
		}, false},
		{"an index's columns", func(s *Snapshot) {
			s.Tables[0].Indexes[0].Columns = []string{"nickname", "email"}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			changed := fixture(t)
			c.reorder(&changed)

			if changed.Equal(fixture(t)) != c.equal {
				t.Errorf("Equal() = %v, want %v", !c.equal, c.equal)
			}
		})
	}
}

func TestAKeysColumnOrderIsPartOfTheKey(t *testing.T) {
	forwards := PrimaryKey{Columns: []string{"a", "b"}}
	backwards := PrimaryKey{Columns: []string{"b", "a"}}

	if forwards.Equal(&backwards) {
		t.Error("a two-column key equals itself reversed, and those are different keys")
	}
}

func TestAForeignKeyPairsItsColumnsPositionally(t *testing.T) {
	forwards := ForeignKey{Columns: []string{"a", "b"}, ReferencesTable: "other",
		ReferencesColumns: []string{"x", "y"}}
	crossed := ForeignKey{Columns: []string{"a", "b"}, ReferencesTable: "other",
		ReferencesColumns: []string{"y", "x"}}

	if forwards.Equal(crossed) {
		t.Error("two keys pairing the same columns differently compare equal")
	}
}

func TestAnyRealDifferenceMakesTwoSnapshotsUnequal(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(s *Snapshot)
	}{
		{"a table that is not there", func(s *Snapshot) { s.Tables = s.Tables[:1] }},
		{"a renamed table", func(s *Snapshot) { s.Tables[0].Name = "people" }},
		{"a renamed column", func(s *Snapshot) { s.Tables[0].Columns[1].Name = "mail" }},
		{"a retyped column", func(s *Snapshot) {
			s.Tables[0].Columns[1].Type = mustExact(t, "text", "text")
		}},
		{"a column that became nullable", func(s *Snapshot) {
			s.Tables[0].Columns[1].Nullable = true
		}},
		{"a changed default", func(s *Snapshot) {
			s.Tables[0].Columns[3].Default = literalOf("nobody")
		}},
		{"a dropped default", func(s *Snapshot) { s.Tables[0].Columns[3].Default = nil }},
		{"a dropped primary key", func(s *Snapshot) { s.Tables[0].PrimaryKey = nil }},
		{"a key over another column", func(s *Snapshot) {
			s.Tables[0].PrimaryKey.Columns = []string{"email"}
		}},
		{"a renamed index", func(s *Snapshot) { s.Tables[0].Indexes[0].Name = "idx_nick" }},
		{"an index that became unique", func(s *Snapshot) { s.Tables[0].Indexes[0].Unique = true }},
		{"a renamed unique constraint", func(s *Snapshot) {
			s.Tables[0].Uniques[0].Name = "uq_mail"
		}},
		{"a reworded check", func(s *Snapshot) {
			s.Tables[0].Checks[0].Expression = "length(email) > 1"
		}},
		{"a changed referential action", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].OnDelete = ActionRestrict
		}},
		{"a key pointing at another table", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].ReferencesTable = "elsewhere"
		}},
		{"a key pointing at another column", func(s *Snapshot) {
			s.Tables[1].ForeignKeys[0].ReferencesColumns = []string{"email"}
		}},
		{"a reworded view", func(s *Snapshot) { s.Views[0].Query = "SELECT id, email FROM users" }},
		{"a marker nothing else has", func(s *Snapshot) {
			s.Tables[0].Columns[0].Beyond = &Unrepresented{Detail: "identity column"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			changed := fixture(t)
			c.change(&changed)

			if changed.Equal(fixture(t)) {
				t.Error("the change compares equal, so nothing would notice it")
			}
		})
	}
}

func TestAMarkerIsPartOfEqualityOnEveryElement(t *testing.T) {
	marker := &Unrepresented{Detail: "something only this engine can express"}

	for _, c := range []struct {
		name string
		mark func(s *Snapshot)
	}{
		{"a table", func(s *Snapshot) { s.Tables[0].Beyond = marker }},
		{"a column", func(s *Snapshot) { s.Tables[0].Columns[0].Beyond = marker }},
		{"a primary key", func(s *Snapshot) { s.Tables[0].PrimaryKey.Beyond = marker }},
		{"an index", func(s *Snapshot) { s.Tables[0].Indexes[0].Beyond = marker }},
		{"a unique constraint", func(s *Snapshot) { s.Tables[0].Uniques[0].Beyond = marker }},
		{"a check", func(s *Snapshot) { s.Tables[0].Checks[0].Beyond = marker }},
		{"a foreign key", func(s *Snapshot) { s.Tables[1].ForeignKeys[0].Beyond = marker }},
		{"a view", func(s *Snapshot) { s.Views[0].Beyond = marker }},
	} {
		t.Run(c.name, func(t *testing.T) {
			marked := fixture(t)
			c.mark(&marked)

			if marked.Equal(fixture(t)) {
				t.Error("a marked element equals an unmarked one, so the marker says nothing")
			}

			same := fixture(t)
			c.mark(&same)

			if !marked.Equal(same) {
				t.Error("two identically marked schemas compare unequal")
			}

			reworded := fixture(t)
			c.mark(&reworded)
			rewordMarker(t, &reworded)

			if marked.Equal(reworded) {
				t.Error("a marker's own text is not compared, so a partial index whose " +
					"predicate changed would compare equal")
			}
		})
	}
}

// rewordMarker changes every marker a snapshot carries, leaving everything else as it was.
func rewordMarker(t *testing.T, s *Snapshot) {
	t.Helper()

	reword := func(beyond **Unrepresented) {
		if *beyond != nil {
			*beyond = &Unrepresented{Detail: (*beyond).Detail + ", and something else"}
		}
	}

	for i := range s.Tables {
		table := &s.Tables[i]
		reword(&table.Beyond)

		for j := range table.Columns {
			reword(&table.Columns[j].Beyond)
		}

		if table.PrimaryKey != nil {
			reword(&table.PrimaryKey.Beyond)
		}

		for j := range table.Indexes {
			reword(&table.Indexes[j].Beyond)
		}

		for j := range table.Uniques {
			reword(&table.Uniques[j].Beyond)
		}

		for j := range table.Checks {
			reword(&table.Checks[j].Beyond)
		}

		for j := range table.ForeignKeys {
			reword(&table.ForeignKeys[j].Beyond)
		}
	}

	for i := range s.Views {
		reword(&s.Views[i].Beyond)
	}
}

func TestANameNoTwoEnginesAgreeOnIsNotPartOfEquality(t *testing.T) {
	renamed := fixture(t)
	renamed.Tables[0].PrimaryKey.Name = "PRIMARY"
	renamed.Tables[1].PrimaryKey.Name = ""
	renamed.Tables[1].ForeignKeys[0].Name = ""

	if !renamed.Equal(fixture(t)) {
		t.Error("a primary key or foreign key compares by its name, which SQLite reports for " +
			"neither and MySQL reports as PRIMARY whatever it was called")
	}
}

func TestANameEveryEngineAgreesOnIsPartOfEquality(t *testing.T) {
	renamed := fixture(t)
	renamed.Tables[0].Indexes[0].Name = "something_else"

	if renamed.Equal(fixture(t)) {
		t.Error("an index compares equal under another name, and every engine records that name")
	}
}

func TestAnUnnamedReferentialActionAndNoActionAreOneValue(t *testing.T) {
	absent := fixture(t)
	absent.Tables[1].ForeignKeys[0].OnUpdate = ""

	explicit := fixture(t)
	explicit.Tables[1].ForeignKeys[0].OnUpdate = ActionNoAction

	if !absent.Equal(explicit) {
		t.Error("the two spellings of no action compare unequal, so every foreign key read " +
			"from SQLite would differ from the same key read from PostgreSQL")
	}

	canonical := absent.Canonical()
	if got := canonical.Tables[0].ForeignKeys[0].OnUpdate; got != ActionNoAction {
		t.Errorf("the canonical form holds on_update %q, want %q", got, ActionNoAction)
	}
}

func TestCanonicalIsIdempotentAndChangesNothingItHolds(t *testing.T) {
	once := fixture(t).Canonical()
	twice := once.Canonical()

	if !reflect.DeepEqual(once, twice) {
		t.Error("canonicalizing twice differs from canonicalizing once")
	}

	if !once.Equal(fixture(t)) {
		t.Error("the canonical form does not equal what it was built from")
	}

	if err := once.Validate(); err != nil {
		t.Errorf("the canonical form is not valid: %v", err)
	}
}

func TestCanonicalSortsTheSetsAndLeavesTheSequences(t *testing.T) {
	snapshot := fixture(t)
	slices.Reverse(snapshot.Tables)
	snapshot.Tables[1].PrimaryKey.Columns = []string{"id", "email"}

	canonical := snapshot.Canonical()

	if canonical.Tables[0].Name != "orders" || canonical.Tables[1].Name != usersTable {
		t.Errorf("tables are ordered %q then %q, want orders then users",
			canonical.Tables[0].Name, canonical.Tables[1].Name)
	}

	key := canonical.Tables[1].PrimaryKey.Columns
	if !slices.Equal(key, []string{"id", "email"}) {
		t.Errorf("the key's columns are %v, and that order is part of the key", key)
	}
}

func TestCanonicalDoesNotWriteThroughToWhatItWasGiven(t *testing.T) {
	snapshot := fixture(t)
	slices.Reverse(snapshot.Tables)
	slices.Reverse(snapshot.Tables[1].Columns)

	first := snapshot.Tables[1].Columns[0].Name

	_ = snapshot.Canonical()

	if snapshot.Tables[0].Name != "orders" {
		t.Error("canonicalizing reordered the table list it was given")
	}

	if snapshot.Tables[1].Columns[0].Name != first {
		t.Errorf("canonicalizing reordered a table's columns in place: it now leads with %q, "+
			"and was given %q", snapshot.Tables[1].Columns[0].Name, first)
	}
}
