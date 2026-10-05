package sqlite

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// noDeclaredType is SQLite's own name for the affinity of a column declared with no type at all.
//
// pragma_table_xinfo answers the empty string for such a column, and schema.UnknownType requires a
// spelling, so the one recorded is the word SQLite's own documentation uses for that affinity.
const noDeclaredType = "none"

// reverse is what one of this engine's declared spellings names, and how certainly it names it.
type reverse struct {
	// logical is the canonical logical spelling, with %s standing in for the declared parameters.
	logical string
	// certainty is how well this declared spelling identifies that one logical type.
	certainty schema.Certainty
}

// declaredTypes maps every spelling nativeType emits to the logical type the catalog reads back as.
//
// Keyed on the base name lower-cased, because the catalog does not echo the compiler: measured,
// pragma_table_xinfo uppercases text, integer, real and blob and leaves every other spelling
// verbatim. Three entries are ambiguous and cannot be made otherwise -- this driver declares all
// three integer widths as integer, both floats as real, and text, json and enum all as text.
var declaredTypes = map[string]reverse{
	"varchar":     {logical: "string(%s)", certainty: schema.CertaintyExact},
	"decimal":     {logical: "decimal(%s)", certainty: schema.CertaintyExact},
	"boolean":     {logical: "bool", certainty: schema.CertaintyExact},
	"uuid":        {logical: "uuid", certainty: schema.CertaintyExact},
	"timestamp":   {logical: "timestamp", certainty: schema.CertaintyExact},
	"timestamptz": {logical: "timestamptz", certainty: schema.CertaintyExact},
	"date":        {logical: "date", certainty: schema.CertaintyExact},
	"time":        {logical: "time", certainty: schema.CertaintyExact},
	"blob":        {logical: "binary", certainty: schema.CertaintyExact},
	"integer":     {logical: "int64", certainty: schema.CertaintyAmbiguous},
	"real":        {logical: "float64", certainty: schema.CertaintyAmbiguous},
	"text":        {logical: "text", certainty: schema.CertaintyAmbiguous},
}

// textType is the spelling an enum, a json document and a text column all declare as.
const textType = "text"

// columnType reads a declared spelling back as the logical type it names, as far as it names one.
func columnType(declared string, labels []string) (schema.ColumnType, error) {
	if strings.TrimSpace(declared) == "" {
		return schema.UnknownType(noDeclaredType)
	}

	base, args := splitDeclared(declared)

	if len(labels) > 0 && base == textType {
		return enumType(declared, labels)
	}

	named, known := declaredTypes[base]
	if !known {
		return schema.UnknownType(declared)
	}

	logical, err := schema.Parse(spellingOf(named.logical, args))
	if err != nil {
		return schema.ColumnType{}, fmt.Errorf("reading %q back as a logical type: %w", declared, err)
	}

	if named.certainty == schema.CertaintyExact {
		return schema.ExactType(logical, declared)
	}

	return schema.AmbiguousType(logical, declared)
}

// enumType reads a text column whose own CHECK holds this driver's label list back as an enum.
func enumType(declared string, labels []string) (schema.ColumnType, error) {
	logical, err := schema.Enum(labels...)
	if err != nil {
		return schema.ColumnType{}, fmt.Errorf("reading the labels of %q back as an enum: %w",
			declared, err)
	}

	return schema.AmbiguousType(logical, declared)
}

// spellingOf fills a logical spelling's parameters in from the declared ones, where it takes any.
func spellingOf(logical, args string) string {
	if !strings.Contains(logical, "%s") {
		return logical
	}

	return fmt.Sprintf(logical, args)
}

// splitDeclared divides a declared spelling into its base name, lower-cased, and its parameters.
func splitDeclared(declared string) (base, args string) {
	trimmed := strings.TrimSpace(declared)

	open := strings.IndexByte(trimmed, openParen)
	if open < 0 || !strings.HasSuffix(trimmed, string(shutParen)) {
		return strings.ToLower(trimmed), ""
	}

	return strings.ToLower(strings.TrimSpace(trimmed[:open])),
		strings.TrimSpace(trimmed[open+1 : len(trimmed)-1])
}

// SQLite records a default as the text the column was declared with, so this driver's own
// quoteLiteral is what it is reading back: a string literal with its quotes doubled inside.
// Anything else is engine text, which is what nextval-shaped defaults are on every engine.

// literalDefault reads a recorded default back as the literal or neutral function it holds.
func literalDefault(recorded string) *schema.ColumnDefault {
	text := strings.TrimSpace(recorded)
	if text == "" {
		return nil
	}

	if function, named := defaultFunctions[strings.ToUpper(text)]; named {
		return &schema.ColumnDefault{Function: function}
	}

	if literal, quoted := unquoteLiteral(text); quoted {
		return &schema.ColumnDefault{Literal: &literal}
	}

	return &schema.ColumnDefault{Expression: text}
}

// defaultFunctions maps this driver's spelling of each neutral function back to the function.
var defaultFunctions = map[string]schema.Function{
	"CURRENT_TIMESTAMP": schema.FuncCurrentTimestamp,
	"CURRENT_DATE":      schema.FuncCurrentDate,
	"CURRENT_TIME":      schema.FuncCurrentTime,
}

// unquoteLiteral reads a single-quoted literal back, and reports a value that is not one.
func unquoteLiteral(text string) (string, bool) {
	if len(text) < 2 || text[0] != singleQuote || text[len(text)-1] != singleQuote {
		return "", false
	}

	return strings.ReplaceAll(text[1:len(text)-1], "''", "'"), true
}

// referentialAction reads a recorded action back, every engine's unnamed one being no_action.
func referentialAction(recorded string) (schema.ReferentialAction, error) {
	action, known := recordedActions[strings.ToUpper(strings.TrimSpace(recorded))]
	if !known {
		return "", fmt.Errorf("%w: this database reports the referential action %q, which migr has "+
			"no name for", ErrIntrospect, recorded)
	}

	return action, nil
}

// recordedActions maps what pragma_foreign_key_list answers to the neutral action it names.
var recordedActions = map[string]schema.ReferentialAction{
	"":            schema.ActionNoAction,
	"NO ACTION":   schema.ActionNoAction,
	"RESTRICT":    schema.ActionRestrict,
	"CASCADE":     schema.ActionCascade,
	"SET NULL":    schema.ActionSetNull,
	"SET DEFAULT": schema.ActionSetDefault,
}
