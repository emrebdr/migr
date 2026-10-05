package postgres

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// arraySuffix is what format_type appends to an element type, however many dimensions it has.
const arraySuffix = "[]"

// catalogType is what one of this engine's own spellings names, and whether it takes parameters.
type catalogType struct {
	// logical is the canonical logical spelling, with %s standing in for the declared parameters.
	logical string
	// parameterized reports whether this spelling takes parameters, which decides a mismatch.
	parameterized bool
}

// catalogTypes maps what format_type answers to the logical type it names, which here is injective.
//
// PostgreSQL rewrites four of the eighteen on the way back -- varchar(255) reads as character
// varying(255) and timestamp as timestamp without time zone -- so this is keyed on the catalog's
// own spelling and not on what nativeType emits. Every entry is exact: this engine is the only one
// of the three whose native map has no collisions, and only because it refuses enum outright.
var catalogTypes = map[string]catalogType{
	"character varying":           {logical: "string(%s)", parameterized: true},
	"numeric":                     {logical: "decimal(%s)", parameterized: true},
	"text":                        {logical: "text"},
	"boolean":                     {logical: "bool"},
	"smallint":                    {logical: "int16"},
	"integer":                     {logical: "int32"},
	"bigint":                      {logical: "int64"},
	"real":                        {logical: "float32"},
	"double precision":            {logical: "float64"},
	"uuid":                        {logical: "uuid"},
	"jsonb":                       {logical: "json"},
	"timestamp without time zone": {logical: "timestamp"},
	"timestamp with time zone":    {logical: "timestamptz"},
	"date":                        {logical: "date"},
	"time without time zone":      {logical: "time"},
	"bytea":                       {logical: "binary"},
}

// columnType reads one of this engine's own spellings back as the logical type it names.
func columnType(native string) (schema.ColumnType, error) {
	logical, named := logicalType(native)
	if !named {
		return schema.UnknownType(native)
	}

	return schema.ExactType(logical, native)
}

// logicalType resolves a spelling to a logical type, reporting one no logical type names.
func logicalType(native string) (schema.Type, bool) {
	trimmed := strings.TrimSpace(native)

	if element, isArray := strings.CutSuffix(trimmed, arraySuffix); isArray {
		return arrayType(element)
	}

	base, args := splitSpelling(trimmed)

	named, known := catalogTypes[base]
	if !known || named.parameterized != (args != "") {
		return schema.Type{}, false
	}

	logical, err := schema.Parse(spellingOf(named.logical, args))
	if err != nil {
		return schema.Type{}, false
	}

	return logical, true
}

// arrayType resolves an array spelling, which names a logical type only when its element does.
func arrayType(element string) (schema.Type, bool) {
	inner, named := logicalType(element)
	if !named {
		return schema.Type{}, false
	}

	array, err := schema.Array(inner)
	if err != nil {
		return schema.Type{}, false
	}

	return array, true
}

// spellingOf fills a logical spelling's parameters in from the declared ones, where it takes any.
func spellingOf(logical, args string) string {
	if !strings.Contains(logical, "%s") {
		return logical
	}

	return fmt.Sprintf(logical, args)
}

// format_type puts a precision inside the name rather than after it -- timestamp(3) without time
// zone -- so the name is what sits either side of the parentheses, and anything this driver does
// not itself emit lands on no entry and is reported unknown.

// splitSpelling divides a catalog spelling into the name around its parameters and the parameters.
func splitSpelling(native string) (base, args string) {
	open := strings.IndexByte(native, '(')
	if open < 0 {
		return native, ""
	}

	shut := strings.IndexByte(native[open:], ')')
	if shut < 0 {
		return native, ""
	}

	shut += open

	return strings.TrimSpace(strings.TrimSpace(native[:open]) + " " +
		strings.TrimSpace(native[shut+1:])), strings.TrimSpace(native[open+1 : shut])
}

// columnDefault reads a rendered default back as the literal, function or engine text it holds.
func columnDefault(recorded string) *schema.ColumnDefault {
	text := strings.TrimSpace(recorded)
	if text == "" {
		return nil
	}

	if function, named := catalogFunctions[strings.ToUpper(text)]; named {
		return &schema.ColumnDefault{Function: function}
	}

	if value, quoted := quotedLiteral(text); quoted {
		return &schema.ColumnDefault{Literal: &value}
	}

	if bareLiteral(text) {
		return &schema.ColumnDefault{Literal: &text}
	}

	return &schema.ColumnDefault{Expression: text}
}

// catalogFunctions maps this engine's rendering of each neutral function back to the function.
//
// These four are what nativeFunction emits and what pg_get_expr renders them back as, measured.
// now() produces the same value as CURRENT_TIMESTAMP and is deliberately absent: a default nobody
// here emitted is engine text, and reading one as a neutral function would be a guess.
var catalogFunctions = map[string]schema.Function{
	"CURRENT_TIMESTAMP": schema.FuncCurrentTimestamp,
	"CURRENT_DATE":      schema.FuncCurrentDate,
	"CURRENT_TIME":      schema.FuncCurrentTime,
	"GEN_RANDOM_UUID()": schema.FuncUUID,
}

// quotedLiteral reads a quoted literal back, with or without the cast this engine renders after it.
func quotedLiteral(text string) (string, bool) {
	if text == "" || text[0] != '\'' {
		return "", false
	}

	end := closingQuote(text)
	if end < 0 {
		return "", false
	}

	if !castOnly(text[end+1:]) {
		return "", false
	}

	return strings.ReplaceAll(text[1:end], "''", "'"), true
}

// closingQuote returns where a literal's own quote closes, doubling being how it is escaped.
func closingQuote(text string) int {
	for i := 1; i < len(text); i++ {
		if text[i] != '\'' {
			continue
		}

		if i+1 < len(text) && text[i+1] == '\'' {
			i++

			continue
		}

		return i
	}

	return -1
}

// castOnly reports whether what follows a literal is nothing but the cast this engine renders.
func castOnly(rest string) bool {
	if rest == "" {
		return true
	}

	return strings.HasPrefix(rest, "::") && !strings.ContainsAny(rest, "'()")
}

// bareLiteral reports a default this engine renders as the value itself: a number or a boolean.
func bareLiteral(text string) bool {
	if text == "true" || text == "false" {
		return true
	}

	_, err := strconv.ParseFloat(text, 64)

	return err == nil
}

// catalogActions maps the letter pg_constraint records a referential action as to its own name.
var catalogActions = map[string]schema.ReferentialAction{
	"a": schema.ActionNoAction,
	"r": schema.ActionRestrict,
	"c": schema.ActionCascade,
	"n": schema.ActionSetNull,
	"d": schema.ActionSetDefault,
}

// referentialAction reads the letter a key's action is recorded as back as the action it names.
func referentialAction(recorded string) (schema.ReferentialAction, error) {
	action, known := catalogActions[strings.TrimSpace(recorded)]
	if !known {
		return "", fmt.Errorf("%w: this server records the referential action %q, which migr has "+
			"no name for", ErrIntrospect, recorded)
	}

	return action, nil
}
