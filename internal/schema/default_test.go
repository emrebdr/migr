package schema

import (
	"strings"
	"testing"
)

// literalOf returns a default holding one literal value, which is what a catalog usually reports.
func literalOf(value string) *ColumnDefault {
	return &ColumnDefault{Literal: &value}
}

func TestADefaultNamesExactlyOneSourceOfItsValue(t *testing.T) {
	for _, c := range []struct {
		name  string
		given *ColumnDefault
		want  string
	}{
		{"nothing at all", &ColumnDefault{}, "sets 0"},
		{"a literal and a function", &ColumnDefault{Literal: literalOf("x").Literal,
			Function: FuncUUID}, "sets 2"},
		{"a function and an expression", &ColumnDefault{Function: FuncUUID,
			Expression: "uuid()"}, "sets 2"},
		{"all three", &ColumnDefault{Literal: literalOf("x").Literal, Function: FuncUUID,
			Expression: "uuid()"}, "sets 3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.given.Validate()
			if err == nil {
				t.Fatal("it validated")
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestEachSourceOfADefaultValidatesOnItsOwn(t *testing.T) {
	for _, given := range []*ColumnDefault{
		nil,
		literalOf("x"),
		literalOf(""),
		{Function: FuncCurrentTimestamp},
		{Expression: "nextval('users_id_seq')"},
	} {
		if err := given.Validate(); err != nil {
			t.Errorf("%s: %v", given, err)
		}
	}
}

func TestADefaultFunctionOutsideTheVocabularyIsRefused(t *testing.T) {
	err := (&ColumnDefault{Function: Function("now")}).Validate()
	if err == nil {
		t.Fatal("an invented default function validated")
	}

	for _, phrase := range []string{`"now"`, string(FuncCurrentTimestamp), string(FuncUUID)} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("error %q does not mention %q", err, phrase)
		}
	}
}

func TestAnAbsentDefaultIsNotAnEmptyOne(t *testing.T) {
	var absent *ColumnDefault

	if !absent.Equal(nil) {
		t.Error("two absent defaults do not agree")
	}

	if absent.Equal(literalOf("")) {
		t.Error("no default at all equals a default of the empty string")
	}

	if !literalOf("").Equal(literalOf("")) {
		t.Error("two empty-string defaults do not agree")
	}
}

func TestAnExpressionNeverEqualsALiteralOrAFunction(t *testing.T) {
	for _, c := range []struct {
		name  string
		left  *ColumnDefault
		right *ColumnDefault
	}{
		{"an expression and the literal it looks like", &ColumnDefault{Expression: "'x'"},
			literalOf("x")},
		{"an expression and the function it means",
			&ColumnDefault{Expression: "CURRENT_TIMESTAMP"},
			&ColumnDefault{Function: FuncCurrentTimestamp}},
		{"two engines' words for one function", &ColumnDefault{Expression: "curdate()"},
			&ColumnDefault{Expression: "CURRENT_DATE"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.left.Equal(c.right) || c.right.Equal(c.left) {
				t.Errorf("%s equals %s, which nothing here can know", c.left, c.right)
			}
		})
	}
}

func TestADefaultRendersAsWhatItIs(t *testing.T) {
	for _, c := range []struct {
		given *ColumnDefault
		want  string
	}{
		{nil, ""},
		{literalOf("x"), `"x"`},
		{&ColumnDefault{Function: FuncUUID}, "uuid"},
		{&ColumnDefault{Expression: "nextval('s')"}, "nextval('s')"},
	} {
		if got := c.given.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}
