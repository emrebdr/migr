package ir

import (
	"errors"
	"strings"
	"testing"
)

func TestARawCarryingNoTransactionNeedsVersionThree(t *testing.T) {
	plain := Raw{Driver: "postgres", SQL: "CREATE INDEX i ON t (c)"}
	standalone := Raw{Driver: "postgres", SQL: "CREATE INDEX CONCURRENTLY i ON t (c)", NoTransaction: true}

	if got := VersionOf(Operations{plain}); got != 1 {
		t.Errorf("a plain raw is carried since irVersion %d, want 1", got)
	}

	if got := VersionOf(Operations{plain, standalone}); got != 3 {
		t.Errorf("a list holding a raw with no_transaction states irVersion %d, want 3", got)
	}

	if got := Since(standalone); got > Version {
		t.Errorf("no_transaction needs irVersion %d and this build reads up to %d", got, Version)
	}
}

func TestARawWithoutTheFlagKeepsItsBytes(t *testing.T) {
	encoded, err := Marshal(NewDocument(Operations{Raw{Driver: "postgres", SQL: "SELECT 1"}}))
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	want := `{"irVersion":1,"operations":[{"op":"raw","driver":"postgres","sql":"SELECT 1"}]}`
	if string(encoded) != want {
		t.Errorf("a raw that never set the flag moved its bytes:\n got %s\nwant %s", encoded, want)
		t.Log("Every migration recorded before irVersion 3 hashes those bytes, so a field that")
		t.Log("appeared when unset would read as drift on every .sql migration ever applied.")
	}
}

func TestARawWithTheFlagRoundTrips(t *testing.T) {
	op := Raw{Driver: "postgres", SQL: "CREATE INDEX CONCURRENTLY i ON t (c)", NoTransaction: true}

	encoded, err := Marshal(NewDocument(Operations{op}))
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	want := `{"irVersion":3,"operations":[{"op":"raw","driver":"postgres",` +
		`"sql":"CREATE INDEX CONCURRENTLY i ON t (c)","no_transaction":true}]}`
	if string(encoded) != want {
		t.Fatalf("encoded\n got %s\nwant %s", encoded, want)
	}

	doc, err := UnmarshalDocument(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if got, ok := doc.Operations[0].(Raw); !ok || got != op {
		t.Errorf("decoded %#v, want %#v", doc.Operations[0], op)
	}
}

func TestADocumentStatingTwoCannotCarryNoTransaction(t *testing.T) {
	_, err := UnmarshalDocument([]byte(`{"irVersion":2,"operations":[{"op":"raw","driver":"postgres",` +
		`"sql":"VACUUM","no_transaction":true}]}`))

	var newer *NewerError
	if !errors.As(err, &newer) || newer.Kind != KindRaw || newer.Needs != 3 || newer.Stated != 2 {
		t.Fatalf("UnmarshalDocument returned %v, want a NewerError naming raw, 3 and 2", err)
	}

	for _, want := range []string{"no_transaction", "irVersion 3", "irVersion 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}
