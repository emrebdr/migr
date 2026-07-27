package checksum

import (
	"crypto/sha256"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// mustType parses a canonical logical type string for a fixture.
func mustType(t *testing.T, s string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(s)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", s, err)
	}

	return parsed
}

// sampleOps returns a small, valid operation list to hash.
func sampleOps(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{
			Table:      "users",
			Columns:    []ir.Column{{Name: "id", Type: mustType(t, "uuid")}},
			PrimaryKey: []string{"id"},
		},
		ir.AddIndex{Table: "users", Name: "idx_users_id", Columns: []string{"id"}},
	}
}

// mustOf computes a checksum for a fixture, failing the test if the IR cannot encode.
func mustOf(t *testing.T, source []byte, ops ir.Operations) Checksum {
	t.Helper()

	c, err := Of(source, ops)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}

	return c
}

// seal records operations under one migration ID and call site, then closes the recording.
func seal(t *testing.T, migration string, src ir.Source, ops ir.Operations) *ir.Recording {
	t.Helper()

	rec, err := ir.NewRecorder(migration, ir.Up)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	for i, op := range ops {
		if err := rec.Append(op, src); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	return sealed
}

func TestTheSameMigrationHashesIdenticallyOnEveryRun(t *testing.T) {
	const source = "func Up(s *migr.Schema) error { return s.CreateTable(\"users\") }\n"

	first := mustOf(t, []byte(source), sampleOps(t))

	for i := range 8 {
		if again := mustOf(t, []byte(source), sampleOps(t)); again != first {
			t.Errorf("run %d = %s, want %s", i, again, first)
			t.Log("A per-process seed or map iteration order on the hash path would show up")
			t.Log("here. Invariant #8 requires every path that produces IR to be deterministic.")

			break
		}
	}
}

func TestSourceAttributionNeverReachesTheHash(t *testing.T) {
	const source = "func Up(s *migr.Schema) error { return nil }\n"

	laptop := seal(t, "20260727T153000_add_users", ir.Source{
		File: "/Users/dev/projects/shop/migrations/20260727T153000_add_users.ts",
		Line: 14,
	}, sampleOps(t))

	ci := seal(t, "20260727T153000_add_users", ir.Source{
		File:     "/home/runner/work/shop/shop/migrations/20260727T153000_add_users.ts",
		Line:     902,
		Function: "Up",
	}, sampleOps(t))

	if mustOf(t, []byte(source), laptop.Operations()) != mustOf(t, []byte(source), ci.Operations()) {
		t.Error("the recorded call site changed the checksum")
		t.Log("A call site is machine-specific, so hashing it would make one migration read")
		t.Log("as drifted on CI and clean on a laptop. That is the 'stable across machines'")
		t.Log("clause, and it is why a Recording marshals to exactly its operations array.")
	}
}

func TestTheEnvelopeIsNotHashed(t *testing.T) {
	ops := sampleOps(t)

	bare, err := ir.Marshal(ops)
	if err != nil {
		t.Fatalf("Marshal(ops): %v", err)
	}

	wrapped, err := ir.Marshal(ir.NewDocument(ops))
	if err != nil {
		t.Fatalf("Marshal(document): %v", err)
	}

	got, err := OfOperations(ops)
	if err != nil {
		t.Fatalf("OfOperations: %v", err)
	}

	if got != Digest(sha256.Sum256(bare)) {
		t.Errorf("OfOperations = %s, want the digest of the bare operations array", got)
	}

	if got == Digest(sha256.Sum256(wrapped)) {
		t.Error("the irVersion envelope reached the hashed bytes")
		t.Log("ADR 0007 puts the envelope outside the checksum boundary: a version bump that")
		t.Log("reshapes no operation must leave every applied migration's checksum untouched,")
		t.Log("or drift detection fires across every installation at once.")
	}
}

func TestAnEmptyDownRecordingHasOneStableDigest(t *testing.T) {
	empty := seal(t, "20260727T153000_add_users", ir.Source{}, nil)

	want := Digest(sha256.Sum256([]byte("[]")))

	tests := []struct {
		name string
		ops  ir.Operations
	}{
		{name: "nil operations", ops: nil},
		{name: "empty operations", ops: ir.Operations{}},
		{name: "sealed recording with no appends", ops: empty.Operations()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OfOperations(tt.ops)
			if err != nil {
				t.Fatalf("OfOperations: %v", err)
			}

			if got != want {
				t.Errorf("OfOperations = %s, want the digest of `[]` %s", got, want)
				t.Log("An empty Down is legal: a migration that records nothing still ran.")
				t.Log("Encoding it as `null` instead of `[]` would silently move the checksum")
				t.Log("of every migration whose Down is empty.")
			}
		})
	}
}

func TestChangingEitherHalfChangesTheChecksum(t *testing.T) {
	const source = "func Up(s *migr.Schema) error { return nil }\n"

	base := mustOf(t, []byte(source), sampleOps(t))

	otherOps := append(sampleOps(t), ir.DropTable{Table: "legacy"})
	otherSource := source + "// a trailing comment\n"

	if same := mustOf(t, []byte(otherSource), sampleOps(t)); same == base {
		t.Error("a changed source did not change the checksum")
	}

	if same := mustOf(t, []byte(source), otherOps); same == base {
		t.Error("changed operations did not change the checksum")
	}

	if changed := mustOf(t, []byte(otherSource), sampleOps(t)); changed.IR != base.IR {
		t.Error("changing only the source moved the IR half")
	}

	if changed := mustOf(t, []byte(source), otherOps); changed.Source != base.Source {
		t.Error("changing only the operations moved the source half")
		t.Log("The halves answer different questions: the source half decides drift and the")
		t.Log("IR half records what one run emitted. Each must move only for its own input.")
	}
}

func TestOperationOrderIsPartOfTheChecksum(t *testing.T) {
	const source = "func Up(s *migr.Schema) error { return nil }\n"

	forward := sampleOps(t)
	reversed := ir.Operations{forward[1], forward[0]}

	if mustOf(t, []byte(source), forward) == mustOf(t, []byte(source), reversed) {
		t.Error("reordering the operations left the checksum unchanged")
		t.Log("Order is meaning: an index cannot be added before its table exists. A hash")
		t.Log("blind to order would call two different migrations the same migration.")
	}
}

func TestAChecksumRendersAsSchemeAndTwoDigests(t *testing.T) {
	const source = "func Up(s *migr.Schema) error { return nil }\n"

	got := mustOf(t, []byte(source), sampleOps(t)).String()

	if len(got) != len(Scheme)+2+2*hexLen {
		t.Errorf("String() = %q (%d characters), want %d", got, len(got), len(Scheme)+2+2*hexLen)
	}

	parsed, err := Parse(got)
	if err != nil {
		t.Fatalf("Parse(%q): %v", got, err)
	}

	if parsed.String() != got {
		t.Errorf("Parse(%q).String() = %q", got, parsed.String())
	}
}
