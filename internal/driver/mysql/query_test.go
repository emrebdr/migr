package mysql

import (
	"errors"
	"strings"
	"testing"

	lib "github.com/go-sql-driver/mysql"

	"github.com/emrebdr/migr/internal/driver"
)

func TestEveryValueThisDriverRendersAndTheOneItRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		column string
		kind   string
		value  any
		want   string
	}{
		"a null":         {column: "n", kind: "INT", value: nil},
		"an int":         {column: "i", kind: "BIGINT", value: int64(9007199254740993), want: "9007199254740993"},
		"a negative int": {column: "i", kind: "INT", value: int64(-42), want: "-42"},
		"an unsigned past 2^63": {
			column: "u", kind: "UNSIGNED BIGINT", value: uint64(18446744073709551615), want: "18446744073709551615",
		},
		"a double":        {column: "f", kind: "DOUBLE", value: 1.5, want: "1.5"},
		"text":            {column: "s", kind: "VARCHAR", value: []byte("hello"), want: "hello"},
		"a decimal":       {column: "d", kind: "DECIMAL", value: []byte("12.3400"), want: "12.3400"},
		"a blob":          {column: "b", kind: "BLOB", value: []byte("Hello"), want: `\x48656c6c6f`},
		"a varbinary":     {column: "b", kind: "VARBINARY", value: []byte{0x00, 0xff}, want: `\x00ff`},
		"a bit":           {column: "b", kind: "BIT", value: []byte{0xaa}, want: `\xaa`},
		"a geometry":      {column: "g", kind: "GEOMETRY", value: []byte{0x01, 0x02}, want: `\x0102`},
		"an empty string": {column: "s", kind: "VARCHAR", value: []byte{}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := textValue(c.column, c.kind, c.value)
			if err != nil {
				t.Fatalf("textValue: %v", err)
			}

			if c.value == nil {
				if got != nil {
					t.Errorf("a SQL null rendered as %q rather than as nothing at all", *got)
				}

				return
			}

			if got == nil {
				t.Fatalf("a value rendered as nothing at all, which is what a null means")
			}

			if *got != c.want {
				t.Errorf("rendered %q, want %q", *got, c.want)
			}
		})
	}

	t.Log("Bytes render as PostgreSQL's bytea does, so one migration reading one column gets the")
	t.Log("same answer from either engine. A null is a nil pointer and never an empty string, which")
	t.Log("is why the empty-string case is here beside it.")
}

func TestBytesTheLibraryCannotNameAreRefusedRatherThanSilentlyRewritten(t *testing.T) {
	_, err := textValue("mystery", "", []byte{0x00, 0xff})
	if !errors.Is(err, ErrNotText) {
		t.Fatalf("textValue returned %v, want %v", err, ErrNotText)
	}

	if !strings.Contains(err.Error(), "mystery") {
		t.Errorf("the refusal does not name the column: %v", err)
	}

	if _, err := textValue("known", "", []byte("hello")); err != nil {
		t.Errorf("valid text under an unnamed type was refused: %v", err)
	}

	t.Log("This is the case the check exists for, and the integration run is what established that")
	t.Log("it is the only one: the server transcodes every text column to the connection's utf8mb4")
	t.Log("on the way out, and a binary column is reported as VARBINARY and hexed. What is left is a")
	t.Log("field type this build of go-sql-driver does not name -- it returns an empty string for")
	t.Log("one -- whose bytes would otherwise reach encoding/json and come back with each invalid")
	t.Log("byte replaced by U+FFFD, with nothing to say so.")
}

func TestAValueOfAGoTypeThisDriverHasNoRenderingForIsRefused(t *testing.T) {
	_, err := textValue("odd", "INT", struct{}{})
	if !errors.Is(err, ErrNotText) {
		t.Fatalf("textValue returned %v, want %v", err, ErrNotText)
	}

	if !strings.Contains(err.Error(), "struct") {
		t.Errorf("the refusal does not name the Go type it could not render: %v", err)
	}
}

func TestEveryBinaryColumnTypeGoesDownTheHexPath(t *testing.T) {
	for _, kind := range []string{
		"BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY", "BIT",
		"VECTOR",
	} {
		if !binaryTypes[kind] {
			t.Errorf("%s is not treated as bytes", kind)
		}
	}

	for _, kind := range []string{"TEXT", "LONGTEXT", "VARCHAR", "CHAR", "JSON", "DECIMAL",
		"DATETIME", "ENUM"} {
		if binaryTypes[kind] {
			t.Errorf("%s is treated as bytes", kind)
		}
	}

	t.Log("go-sql-driver derives BLOB from TEXT and BINARY from CHAR by whether the field's")
	t.Log("character set is the binary one, so this list is the whole of what tells bytes from text")
	t.Log("on an engine that sends both as []byte.")
}

func TestAWriteTheServerRefusedIsTheRefusalItIsRatherThanAFailure(t *testing.T) {
	refused := &lib.MySQLError{Number: readOnlyRefused, Message: "Cannot execute statement in a " +
		"READ ONLY transaction."}

	if !errors.Is(classifyRead(refused), driver.ErrNotReadOnly) {
		t.Fatal("the server's own refusal is not classified as one")
	}

	if errors.Is(classifyRead(errors.New("the connection died")), driver.ErrNotReadOnly) {
		t.Error("an unrelated failure is reported as a refusal")
	}

	if !hasNumber(refused, readOnlyRefused) {
		t.Fatal("hasNumber does not recognize the server's own error")
	}

	if hasNumber(refused, errNoSuchTable) {
		t.Error("hasNumber matched a different error number")
	}

	if hasNumber(errors.New("something else"), readOnlyRefused) {
		t.Error("hasNumber matched an error the server did not raise")
	}
}
