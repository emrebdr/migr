package schema

import (
	"slices"
	"strings"
	"testing"
)

// pinnedDocument is the JSON one small snapshot encodes to, member for member.
const pinnedDocument = `{"tables":[{"name":"users","columns":[` +
	`{"name":"email","type":{"certainty":"exact","logical":"string(255)",` +
	`"native":"character varying(255)"},"nullable":true,"default":{"literal":"x"}},` +
	`{"name":"id","type":{"certainty":"exact","logical":"int64","native":"bigint"},` +
	`"nullable":false}],"primary_key":{"name":"users_pkey","columns":["id"]}}],` +
	`"views":[{"name":"v","query":"SELECT id FROM users","beyond":{"detail":"materialized"}}]}`

// pinned returns the snapshot pinnedDocument is the encoding of.
func pinned(t *testing.T) Snapshot {
	t.Helper()

	return Snapshot{
		Tables: []Table{{
			Name: usersTable,
			Columns: []Column{
				fixtureColumn(t, "id", "int64", "bigint"),
				{Name: "email", Type: mustExact(t, "string(255)", "character varying(255)"),
					Nullable: true, Default: literalOf("x")},
			},
			PrimaryKey: &PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
		}},
		Views: []View{{Name: "v", Query: "SELECT id FROM users",
			Beyond: &Unrepresented{Detail: "materialized"}}},
	}
}

func TestOneSnapshotEncodesToExactlyTheseBytes(t *testing.T) {
	encoded, err := Marshal(pinned(t))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if string(encoded) != pinnedDocument {
		t.Errorf("encoded as\n%s\nwant\n%s", encoded, pinnedDocument)
	}
}

func TestEveryFixtureRoundTripsByteIdentically(t *testing.T) {
	for _, c := range []struct {
		name     string
		snapshot Snapshot
	}{
		{"empty", Snapshot{}},
		{"pinned", pinned(t)},
		{"full", fixture(t)},
		{"marked", markedFixture(t)},
	} {
		name, snapshot := c.name, c.snapshot

		t.Run(name, func(t *testing.T) {
			encoded, err := Marshal(snapshot)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			decoded, err := Unmarshal(encoded)
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", encoded, err)
			}

			if !decoded.Equal(snapshot) {
				t.Error("the decoded snapshot does not equal the one that was encoded")
			}

			again, err := Marshal(decoded)
			if err != nil {
				t.Fatalf("Marshal again: %v", err)
			}

			if string(again) != string(encoded) {
				t.Errorf("re-encoded as\n%s\nwant\n%s", again, encoded)
			}
		})
	}
}

func TestAnEmptySnapshotEncodesToAnEmptyDocument(t *testing.T) {
	encoded, err := Marshal(Snapshot{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if string(encoded) != "{}" {
		t.Errorf("encoded as %s, want {}", encoded)
	}
}

func TestEncodingIsTheCanonicalOrderWhateverOrderItWasGiven(t *testing.T) {
	shuffled := fixture(t)
	slices.Reverse(shuffled.Tables)
	slices.Reverse(shuffled.Tables[0].Columns)

	first, err := Marshal(shuffled)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	second, err := Marshal(fixture(t))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("two orderings of one schema encode differently:\n%s\n%s", first, second)
	}
}

func TestTheCodecRefusesWhatItCannotFullyRead(t *testing.T) {
	for _, c := range []struct {
		name     string
		document string
		want     string
	}{
		{"a member this build does not know",
			`{"tables":[{"name":"t","owner":"nobody"}]}`, "owner"},
		{"a member of a nested object",
			`{"views":[{"name":"v","query":"SELECT 1","materialized":true}]}`, "materialized"},
		{"something that is not JSON", `{`, "decoding a schema snapshot"},
		{"a snapshot that is not valid",
			`{"tables":[{"name":"t","columns":[{"name":"","nullable":false,` +
				`"type":{"certainty":"exact","logical":"int32","native":"int"}}]}]}`,
			"a column has no name"},
		{"a type state nothing recognizes",
			`{"tables":[{"name":"t","columns":[{"name":"c","nullable":false,` +
				`"type":{"certainty":"perhaps","native":"int"}}]}]}`, "perhaps"},
	} {
		t.Run(c.name, func(t *testing.T) {
			decoded, err := Unmarshal([]byte(c.document))
			if err == nil {
				t.Fatalf("decoding succeeded, producing %d tables", len(decoded.Tables))
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestAnInvalidSnapshotIsNotEncodedAtAll(t *testing.T) {
	for _, c := range []struct {
		name   string
		broken func(s *Snapshot)
		want   string
	}{
		{"a column with no type", func(s *Snapshot) {
			s.Tables[0].Columns[0].Type = ColumnType{}
		}, "zero ColumnType"},
		{"two tables of one name", func(s *Snapshot) { s.Tables[1].Name = usersTable },
			"two objects claim the name"},
		{"a key over a column that is not there", func(s *Snapshot) {
			s.Tables[0].PrimaryKey.Columns = []string{"nonesuch"}
		}, "this table has no column of"},
	} {
		t.Run(c.name, func(t *testing.T) {
			broken := fixture(t)
			c.broken(&broken)

			encoded, err := Marshal(broken)
			if err == nil {
				t.Fatalf("an invalid snapshot encoded as %s", encoded)
			}

			if !strings.Contains(err.Error(), "encoding a schema snapshot") {
				t.Errorf("error %q does not say what failed", err)
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}
