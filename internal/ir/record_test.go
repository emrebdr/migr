package ir

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRecorderPreservesOrder(t *testing.T) {
	rec := newRecorder(t)

	all := fixtures(t)
	for i, f := range all {
		if err := rec.Append(f.op, Source{File: "users.ts", Line: i + 1}); err != nil {
			t.Fatalf("appending %s: %v", f.kind, err)
		}
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if sealed.Len() != len(all) {
		t.Fatalf("recorded %d operations, want %d", sealed.Len(), len(all))
	}

	for i, e := range sealed.Entries() {
		if e.Index != i {
			t.Errorf("entry at position %d reports index %d", i, e.Index)
		}

		if e.Op.Kind() != all[i].kind {
			t.Errorf("position %d holds %s, want %s", i, e.Op.Kind(), all[i].kind)
		}

		if e.Source.Line != i+1 {
			t.Errorf("position %d lost its call site: %+v", i, e.Source)
		}
	}
}

func TestSealClosesTheRecording(t *testing.T) {
	rec := newRecorder(t)

	if err := rec.Append(DropTable{Table: "users"}, Source{}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	err = rec.Append(DropTable{Table: "orders"}, Source{})
	if err == nil {
		t.Fatal("appending after Seal must fail")
	}

	if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("error should say the recording is sealed, got %q", err)
	}

	if sealed.Len() != 1 {
		t.Errorf("the sealed recording grew to %d entries", sealed.Len())
	}
}

func TestSealIsIdempotent(t *testing.T) {
	rec := newRecorder(t)

	first, err := rec.Seal()
	if err != nil {
		t.Fatalf("first seal: %v", err)
	}

	second, err := rec.Seal()
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}

	if first != second {
		t.Error("sealing twice must return the same recording")
	}
}

func TestEmptyRecordingSeals(t *testing.T) {
	rec := newRecorder(t)

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("a migration that records nothing is legal: %v", err)
	}

	if sealed.Len() != 0 {
		t.Errorf("empty recording has %d entries", sealed.Len())
	}

	encoded, err := Marshal(sealed)
	if err != nil {
		t.Fatalf("marshaling an empty recording: %v", err)
	}

	if string(encoded) != `{"irVersion":1,"operations":[]}` {
		t.Errorf("empty recording marshals to %s, want a versioned empty envelope", encoded)
	}
}

func TestMutatingTheCallersOperationDoesNotChangeTheRecording(t *testing.T) {
	rec := newRecorder(t)

	op := CreateTable{
		Table:      "users",
		Columns:    []Column{{Name: "id", Type: mustType(t, "uuid")}},
		PrimaryKey: []string{"id"},
	}

	if err := rec.Append(op, Source{File: "users.go", Line: 3}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	op.Columns[0].Name = "tampered"
	op.PrimaryKey[0] = "tampered"

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	assertUntampered(t, sealed, "the recorder must own a copy of what it was handed")
}

func TestMutatingAReadEntryDoesNotChangeTheRecording(t *testing.T) {
	rec := newRecorder(t)

	err := rec.Append(CreateTable{
		Table:      "users",
		Columns:    []Column{{Name: "id", Type: mustType(t, "uuid")}},
		PrimaryKey: []string{"id"},
	}, Source{File: "users.go", Line: 3})
	if err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	handed, ok := sealed.Entries()[0].Op.(CreateTable)
	if !ok {
		t.Fatalf("entry holds %T, want CreateTable", sealed.Entries()[0].Op)
	}

	handed.Columns[0].Name = "tampered"
	handed.PrimaryKey[0] = "tampered"

	assertUntampered(t, sealed, "a sealed recording must not be reachable through what it hands out")
}

func TestOperationsAccessorIsAlsoACopy(t *testing.T) {
	rec := newRecorder(t)

	err := rec.Append(AddIndex{
		Table: "users", Name: "idx", Columns: []string{"email"},
	}, Source{})
	if err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	idx, ok := sealed.Operations()[0].(AddIndex)
	if !ok {
		t.Fatalf("operation is %T, want AddIndex", sealed.Operations()[0])
	}

	idx.Columns[0] = "tampered"

	after, ok := sealed.Operations()[0].(AddIndex)
	if !ok {
		t.Fatalf("operation is %T, want AddIndex", sealed.Operations()[0])
	}

	if after.Columns[0] != "email" {
		t.Errorf("Operations() shares its backing array; column is now %q", after.Columns[0])
	}
}

func TestRecorderIsSafeUnderConcurrentUse(t *testing.T) {
	rec := newRecorder(t)

	const writers = 32

	var wg sync.WaitGroup

	wg.Add(writers)

	for i := range writers {
		go func() {
			defer wg.Done()

			_ = rec.Append(DropTable{Table: "t"}, Source{File: "x.go", Line: i + 1})
		}()
	}

	wg.Wait()

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if sealed.Len() != writers {
		t.Errorf("recorded %d operations, want %d", sealed.Len(), writers)
	}

	for i, e := range sealed.Entries() {
		if e.Index != i {
			t.Errorf("index %d is out of sequence: %d", i, e.Index)
		}
	}
}

func TestAttributionNeverReachesTheDocument(t *testing.T) {
	rec := newRecorder(t)

	for i, f := range fixtures(t) {
		src := Source{File: "/Users/someone/migrations/users.ts", Line: i + 1, Function: "Up"}
		if err := rec.Append(f.op, src); err != nil {
			t.Fatalf("appending %s: %v", f.kind, err)
		}
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	encoded, err := Marshal(sealed)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	for _, leak := range []string{"Users/someone", "users.ts", `"file"`, `"line"`, `"source"`} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("attribution leaked into the IR document: found %q", leak)
			t.Log("A call site is a machine-specific path. In the document it would make")
			t.Log("checksums differ between CI and a laptop (M1-T6) and would break the")
			t.Log("byte-identical-IR guarantee across SDKs (ADR 0003).")
		}
	}

	doc, err := Marshal(sealed.Document())
	if err != nil {
		t.Fatalf("marshaling the document: %v", err)
	}

	if string(encoded) != string(doc) {
		t.Errorf("a recording must marshal exactly as its document does:\n got %s\nwant %s",
			encoded, doc)
	}
}

func TestRecordingRoundTripsThroughTheWireForm(t *testing.T) {
	rec := newRecorder(t)

	all := fixtures(t)
	for _, f := range all {
		if err := rec.Append(f.op, Source{File: "m.go", Line: 1}); err != nil {
			t.Fatalf("appending %s: %v", f.kind, err)
		}
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	encoded, err := Marshal(sealed)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	doc, err := UnmarshalDocument(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if doc.IRVersion != Version {
		t.Errorf("document carries irVersion %d, want %d", doc.IRVersion, Version)
	}

	if !reflect.DeepEqual(doc.Operations, sealed.Operations()) {
		t.Error("the recording does not survive a trip through its own wire form")
	}

	for i, f := range all {
		if !reflect.DeepEqual(doc.Operations[i], f.op) {
			t.Errorf("operation %d (%s) changed: got %#v, want %#v", i, f.kind, doc.Operations[i], f.op)
		}
	}
}

func TestRecordingsAreDeterministic(t *testing.T) {
	build := func() []byte {
		rec := newRecorder(t)

		for i, f := range fixtures(t) {
			if err := rec.Append(f.op, Source{File: "m.go", Line: i}); err != nil {
				t.Fatalf("appending: %v", err)
			}
		}

		sealed, err := rec.Seal()
		if err != nil {
			t.Fatalf("sealing: %v", err)
		}

		encoded, err := Marshal(sealed)
		if err != nil {
			t.Fatalf("marshaling: %v", err)
		}

		return encoded
	}

	first, second := build(), build()

	if string(first) != string(second) {
		t.Errorf("two identical recordings produced different bytes:\n %s\n %s", first, second)
	}
}

func TestNewRecorderValidatesItsArguments(t *testing.T) {
	tests := []struct {
		name      string
		migration string
		direction Direction
		want      string
	}{
		{name: "no migration", migration: "", direction: Up, want: "migration ID is required"},
		{name: "blank migration", migration: "   ", direction: Up, want: "migration ID is required"},
		{name: "unknown direction", migration: "m", direction: "sideways", want: `unknown direction "sideways"`},
		{name: "empty direction", migration: "m", direction: "", want: "unknown direction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRecorder(tt.migration, tt.direction)
			if err == nil {
				t.Fatal("should have been rejected")
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestAppendRejectsMalformedOperations(t *testing.T) {
	rec := newRecorder(t)

	err := rec.Append(AddColumn{Column: Column{Name: "email", Type: mustType(t, "text")}},
		Source{File: "users.ts", Line: 14})
	if err == nil {
		t.Fatal("an operation missing its table must be rejected at Append")
	}

	if !strings.Contains(err.Error(), "table is required") {
		t.Errorf("error should name the problem, got %q", err)
	}

	if !strings.Contains(err.Error(), "users.ts:14") {
		t.Errorf("error should name the call site it entered through, got %q", err)
	}

	if rec.Len() != 0 {
		t.Errorf("a rejected operation must not be recorded, Len() = %d", rec.Len())
	}
}

func TestAppendRejectsNil(t *testing.T) {
	rec := newRecorder(t)

	if err := rec.Append(nil, Source{}); err == nil {
		t.Fatal("appending nil must fail")
	}

	if rec.Len() != 0 {
		t.Errorf("nil must not be recorded, Len() = %d", rec.Len())
	}
}

func TestAtBoundsAreChecked(t *testing.T) {
	rec := newRecorder(t)

	if err := rec.Append(DropTable{Table: "users"}, Source{}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	for _, index := range []int{-1, 1, 99} {
		if _, ok := sealed.At(index); ok {
			t.Errorf("At(%d) should report out of range", index)
		}
	}

	if _, ok := sealed.At(0); !ok {
		t.Error("At(0) should find the only entry")
	}
}

// newRecorder starts a recorder for the fixture migration used across these tests.
func newRecorder(t *testing.T) *Recorder {
	t.Helper()

	rec, err := NewRecorder("20260727T153000_add_users", Up)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	return rec
}

// assertUntampered checks that a recording still holds its original create_table.
func assertUntampered(t *testing.T, sealed *Recording, why string) {
	t.Helper()

	table, ok := sealed.Entries()[0].Op.(CreateTable)
	if !ok {
		t.Fatalf("entry holds %T, want CreateTable", sealed.Entries()[0].Op)
	}

	if table.Columns[0].Name != "id" {
		t.Errorf("%s: column is now %q", why, table.Columns[0].Name)
	}

	if table.PrimaryKey[0] != "id" {
		t.Errorf("%s: primary key is now %q", why, table.PrimaryKey[0])
	}
}
