package ir

import (
	"errors"
	"strings"
	"testing"
)

func TestOpErrorNamesEverythingInvariantNineRequires(t *testing.T) {
	rec := newRecorder(t)

	err := rec.Append(AddColumn{
		Table:  "users",
		Column: Column{Name: "email", Type: mustType(t, "string(255)")},
	}, Source{File: "migrations/users.ts", Line: 14, Function: "Up"})
	if err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	wrapped := sealed.Wrap(0, errors.New("column already exists"))

	for _, want := range []string{
		"migrations/users.ts:14",
		"20260727T153000_add_users",
		"(up)",
		"op 0",
		"add_column",
		"column already exists",
	} {
		if !strings.Contains(wrapped.Error(), want) {
			t.Errorf("error should mention %q, got %q", want, wrapped)
		}
	}
}

func TestOpErrorLeadsWithTheLocation(t *testing.T) {
	rec := newRecorder(t)

	if err := rec.Append(DropTable{Table: "users"}, Source{File: "users.go", Line: 7}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	got := sealed.Wrap(0, errors.New("boom")).Error()

	if !strings.HasPrefix(got, "users.go:7: ") {
		t.Errorf("the location should come first, got %q", got)
	}
}

func TestOpErrorOmitsAnUnknownLocation(t *testing.T) {
	rec := newRecorder(t)

	if err := rec.Append(DropTable{Table: "users"}, Source{}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	got := sealed.Wrap(0, errors.New("boom")).Error()

	if strings.Contains(got, ":0") {
		t.Errorf("a missing call site must not render as :0, got %q", got)
	}

	if !strings.HasPrefix(got, "20260727T153000_add_users") {
		t.Errorf("without a location the migration should lead, got %q", got)
	}
}

func TestOpErrorUnwraps(t *testing.T) {
	cause := errors.New("the underlying problem")

	rec := newRecorder(t)

	if err := rec.Append(DropTable{Table: "users"}, Source{File: "x.go", Line: 1}); err != nil {
		t.Fatalf("appending: %v", err)
	}

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	wrapped := sealed.Wrap(0, cause)

	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is must reach the cause through the attribution")
	}

	var opErr *OpError
	if !errors.As(wrapped, &opErr) {
		t.Fatal("errors.As must reach *OpError")
	}

	if opErr.Kind != KindDropTable {
		t.Errorf("OpError.Kind = %q, want %q", opErr.Kind, KindDropTable)
	}

	if opErr.Source.Line != 1 {
		t.Errorf("OpError lost its call site: %+v", opErr.Source)
	}
}

func TestWrapPassesNilThrough(t *testing.T) {
	rec := newRecorder(t)

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if got := sealed.Wrap(0, nil); got != nil {
		t.Errorf("wrapping nil must stay nil, got %v", got)
	}
}

func TestWrapOutOfRangeStillNamesTheMigration(t *testing.T) {
	rec := newRecorder(t)

	sealed, err := rec.Seal()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	got := sealed.Wrap(99, errors.New("boom"))
	if got == nil {
		t.Fatal("wrapping a real error must not return nil")
	}

	if !strings.Contains(got.Error(), "20260727T153000_add_users") {
		t.Errorf("even without an entry the migration must be named, got %q", got)
	}
}

func TestSourceString(t *testing.T) {
	tests := []struct {
		name string
		src  Source
		want string
	}{
		{name: "file and line", src: Source{File: "a.go", Line: 12}, want: "a.go:12"},
		{name: "file only", src: Source{File: "a.go"}, want: "a.go"},
		{name: "line without a file", src: Source{Line: 12}, want: ""},
		{name: "nothing", src: Source{}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.src.String(); got != tt.want {
				t.Errorf("Source.String() = %q, want %q", got, tt.want)
			}
		})
	}

	if !(Source{}).Zero() {
		t.Error("an empty Source must report Zero")
	}

	if (Source{File: "a.go"}).Zero() {
		t.Error("a Source with a file is not Zero")
	}
}
