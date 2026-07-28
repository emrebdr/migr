package postgres

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestALiteralDefaultCannotEscapeItsQuotes(t *testing.T) {
	tests := []struct {
		literal string
		want    string
	}{
		{literal: "anonymous", want: `'anonymous'`},
		{literal: "0", want: `'0'`},
		{literal: "it's", want: `'it''s'`},
		{literal: `'); DROP TABLE users --`, want: `'''); DROP TABLE users --'`},
		{literal: `back\slash`, want: `'back\slash'`},
		{literal: "", want: `''`},
	}

	for _, tt := range tests {
		t.Run(tt.literal, func(t *testing.T) {
			if got := quoteLiteral(tt.literal); got != tt.want {
				t.Errorf("quoteLiteral(%q) = %s, want %s", tt.literal, got, tt.want)
			}
		})
	}

	t.Log("A backslash stays one character rather than starting an escape, which holds only")
	t.Log("while standard_conforming_strings is on. Open pins it rather than assuming it,")
	t.Log("because Compile is pure by contract and cannot read a session setting.")
}

func TestALiteralHoldingANulByteIsRefused(t *testing.T) {
	if checkDefault("column \"x\"", ir.Literal("draft\x00")) == "" {
		t.Error("a default holding a NUL byte was accepted")
		t.Log("A default is written into the statement rather than bound, so a NUL truncates")
		t.Log("the statement at the wire protocol exactly as it would in an identifier.")
	}
}

func TestAColumnRendersItsTypeNullabilityAndDefaultInThatOrder(t *testing.T) {
	column := ir.Column{
		Name:     "status",
		Type:     mustType(t, "string(32)"),
		Nullable: false,
		Default:  ir.Literal("draft"),
	}

	if got := columnDefinition(column); got != `"status" varchar(32) NOT NULL DEFAULT 'draft'` {
		t.Errorf("columnDefinition = %s", got)
	}
}

func TestANullableColumnSaysNothingRatherThanSayingNull(t *testing.T) {
	column := ir.Column{Name: "note", Type: mustType(t, "text"), Nullable: true}

	got := columnDefinition(column)
	if got != `"note" text` {
		t.Errorf("columnDefinition = %s, want no nullability clause at all", got)
	}

	if strings.Contains(got, "NULL") {
		t.Log("Nullable is PostgreSQL's default, so an explicit NULL would be noise in every")
		t.Log("golden file and would differ from what introspection reports back in M11.")
	}
}

func TestAColumnWithNoDefaultRendersNoDefaultClause(t *testing.T) {
	if clause := defaultClause(nil); clause != "" {
		t.Errorf("defaultClause(nil) = %q", clause)
	}
}
