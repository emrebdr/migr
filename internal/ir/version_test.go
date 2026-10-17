package ir

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestEveryEmittedDocumentCarriesTheVersion(t *testing.T) {
	rec := newRecorder(t)

	if err := rec.Append(DropTable{Table: "users"}, Source{}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	encoded, err := Marshal(sealed)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	if !strings.HasPrefix(string(encoded), `{"irVersion":1,"operations":`) {
		t.Errorf("a recording must emit a versioned envelope, got %s", encoded)
	}
}

func TestUnstampedDocumentRefusesToMarshal(t *testing.T) {
	var doc Document

	if _, err := Marshal(doc); err == nil {
		t.Fatal(`a document with no version must not emit "irVersion":0`)
	}

	if _, err := Marshal(NewDocument(nil)); err != nil {
		t.Errorf("NewDocument must stamp the version: %v", err)
	}
}

func TestUnknownVersionNamesBothSides(t *testing.T) {
	_, err := UnmarshalDocument([]byte(`{"irVersion":4,"operations":[]}`))
	if err == nil {
		t.Fatal("an unreadable version must be rejected")
	}

	for _, want := range []string{"4", "1, 2, 3", "unsupported irVersion"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %q", want, err)
		}
	}

	var verr *VersionError
	if !errors.As(err, &verr) {
		t.Fatalf("error is %T, want *VersionError", err)
	}

	if verr.Received != 4 {
		t.Errorf("VersionError.Received = %d, want 4", verr.Received)
	}

	if !reflect.DeepEqual(verr.Supported, Supported()) {
		t.Errorf("VersionError.Supported = %v, want %v", verr.Supported, Supported())
	}
}

func TestMissingVersionIsItsOwnFailure(t *testing.T) {
	_, err := UnmarshalDocument([]byte(`{"operations":[]}`))
	if err == nil {
		t.Fatal("a document with no irVersion must be rejected, never defaulted")
	}

	if !errors.Is(err, ErrNoVersion) {
		t.Errorf("error should be ErrNoVersion, got %v", err)
	}

	zero, zeroErr := UnmarshalDocument([]byte(`{"irVersion":0,"operations":[]}`))
	if zeroErr == nil {
		t.Fatalf("irVersion 0 must be rejected, decoded %#v", zero)
	}

	if errors.Is(zeroErr, ErrNoVersion) {
		t.Error("an explicit 0 is a different failure from an absent field; messages must differ")
	}
}

func TestVersionIsCheckedBeforeOperations(t *testing.T) {
	doc := `{"irVersion":99,"operations":[{"op":"add_generated_column","table":"users"}]}`

	_, err := UnmarshalDocument([]byte(doc))
	if err == nil {
		t.Fatal("a future document must be rejected")
	}

	if !strings.Contains(err.Error(), "unsupported irVersion 99") {
		t.Errorf("the version is the actionable problem, got %q", err)
	}

	if strings.Contains(err.Error(), "add_generated_column") {
		t.Errorf("reporting the unknown operation points at the wrong problem: %q", err)
		t.Log("A document from a future version very likely contains operations this build")
		t.Log("has never heard of. Decoding first reports the symptom; checking the version")
		t.Log("first tells the operator to upgrade the CLI.")
	}
}

func TestEnvelopeDecodesStrictly(t *testing.T) {
	_, err := UnmarshalDocument([]byte(`{"irVersion":1,"operations":[],"extra":true}`))
	if err == nil {
		t.Fatal("an unknown envelope field must be rejected")
	}

	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("the error should name the unknown field, got %q", err)
	}
}

func TestDocumentRoundTrips(t *testing.T) {
	ops := make(Operations, 0, len(Kinds()))
	for _, f := range fixtures(t) {
		ops = append(ops, f.op)
	}

	doc := NewDocument(ops)

	encoded, err := Marshal(doc)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	back, err := UnmarshalDocument(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if !reflect.DeepEqual(back, doc) {
		t.Error("the document did not survive a round trip")
	}

	again, err := Marshal(back)
	if err != nil {
		t.Fatalf("re-marshaling: %v", err)
	}

	if string(again) != string(encoded) {
		t.Errorf("bytes changed across a round trip:\n got %s\nwant %s", again, encoded)
	}
}

func TestEnvelopeStaysOutsideTheChecksumBoundary(t *testing.T) {
	rec := newRecorder(t)

	for _, f := range fixtures(t) {
		if err := rec.Append(f.op, Source{}); err != nil {
			t.Fatalf("appending %s: %v", f.kind, err)
		}
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	hashable, err := Marshal(sealed.Operations())
	if err != nil {
		t.Fatalf("marshaling the operations: %v", err)
	}

	if strings.Contains(string(hashable), "irVersion") {
		t.Error("the hashable unit must not carry the version")
		t.Log("ADR 0003 recorded that a version bump would change every applied migration's")
		t.Log("checksum and fire drift detection everywhere. Keeping the envelope outside the")
		t.Log("boundary narrows that to bumps that genuinely reshape operations.")
	}

	if !strings.HasPrefix(string(hashable), `[{"op":`) {
		t.Errorf("Operations() must marshal as a bare array, got %s", hashable[:40])
	}
}

func TestCheckVersion(t *testing.T) {
	if err := CheckVersion(Version); err != nil {
		t.Errorf("the current version must be readable: %v", err)
	}

	for _, v := range []int{0, -1, 4, 99} {
		if err := CheckVersion(v); err == nil {
			t.Errorf("CheckVersion(%d) should fail", v)
		}
	}
}

func TestSupportedIsACopy(t *testing.T) {
	first := Supported()
	first[0] = 999

	if Supported()[0] == 999 {
		t.Error("Supported() must hand out a copy, not the package's own slice")
	}

	if !slices.Equal(Supported(), []int{1, 2, Version}) {
		t.Errorf("Supported() = %v, want [1 2 %d]", Supported(), Version)
		t.Log("Each version is a superset of the one before it, so a build that reads 2 reads")
		t.Log("every document 1 can hold -- and dropping 1 would refuse every migration recorded")
		t.Log("before the data operations existed.")
	}
}

func TestVersionErrorMessagePluralizes(t *testing.T) {
	one := &VersionError{Received: 5, Supported: []int{1}}
	if got := one.Error(); !strings.Contains(got, "reads irVersion 1") {
		t.Errorf("single-version message = %q", got)
	}

	many := &VersionError{Received: 5, Supported: []int{1, 2}}
	if got := many.Error(); !strings.Contains(got, "reads irVersion 1, 2") {
		t.Errorf("multi-version message = %q", got)
	}
}

func TestDocumentsAreDeterministic(t *testing.T) {
	build := func() []byte {
		rec := newRecorder(t)

		for _, f := range fixtures(t) {
			if err := rec.Append(f.op, Source{File: "m.go", Line: 1}); err != nil {
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
		t.Errorf("two identical documents produced different bytes:\n %s\n %s", first, second)
	}
}

func TestADocumentStatesTheOldestVersionThatCarriesItsOperations(t *testing.T) {
	schemaOnly := Operations{DropView{View: "v"}}
	withData := Operations{DropView{View: "v"}, DataCopy{From: "a", To: "b",
		Columns: []CopyColumn{{From: "id", To: "id"}}}}

	if got := NewDocument(schemaOnly).IRVersion; got != 1 {
		t.Errorf("a document of v1 operations is stamped irVersion %d, want 1", got)
		t.Log("Stamping the newest version on every document would make an engine that reads 1")
		t.Log("refuse migrations it can read perfectly well.")
	}

	if got := NewDocument(withData).IRVersion; got != 2 {
		t.Errorf("a document carrying data_copy is stamped irVersion %d, want 2", got)
	}

	encoded, err := Marshal(NewDocument(schemaOnly))
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	if want := `{"irVersion":1,"operations":[{"op":"drop_view","view":"v"}]}`; string(encoded) != want {
		t.Errorf("a v1 document moved its bytes:\n got %s\nwant %s", encoded, want)
	}
}

func TestADocumentStatingAVersionThatCannotCarryItsOperationsIsRefused(t *testing.T) {
	_, err := UnmarshalDocument([]byte(`{"irVersion":1,"operations":[{"op":"data_copy",` +
		`"from":"a","to":"b","columns":[{"from":"id","to":"id"}]}]}`))

	var newer *NewerError
	if !errors.As(err, &newer) || newer.Kind != KindDataCopy || newer.Stated != 1 {
		t.Fatalf("UnmarshalDocument returned %v, want a NewerError naming data_copy and 1", err)
	}

	for _, want := range []string{"data_copy", "irVersion 2", "irVersion 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestEveryKindSaysWhichVersionFirstCarriesIt(t *testing.T) {
	for _, k := range Kinds() {
		want := 1
		if slices.Contains(DataKinds(), k) {
			want = 2
		}

		if got := k.Since(); got != want || got > Version {
			t.Errorf("%s is carried since irVersion %d, want %d (this build reads up to %d)",
				k, got, want, Version)
		}
	}
}
