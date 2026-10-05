package mysql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// boolType is the one spelling whose parameter is not a display width, on either variant.
//
// MySQL 8.0 removed display widths from every integer type and kept this one, because tinyint(1) is
// how both variants spell a boolean. Stripping it would make a bool column read as an int8 nothing
// here has a name for.
const boolType = "tinyint(1)"

// catalogType is what one of this engine's own spellings names, and whether it takes parameters.
type catalogType struct {
	// logical is the canonical logical spelling, with %s standing in for the declared parameters.
	logical string
	// parameterized reports whether this spelling takes parameters, which decides a mismatch.
	parameterized bool
	// certainty is how well this spelling identifies that one logical type on this engine.
	certainty schema.Certainty
}

// catalogTypes maps what COLUMN_TYPE answers to the logical type it names, display widths removed.
//
// One entry is ambiguous and cannot be made otherwise: this driver declares both timestamp and
// timestamptz as datetime(6), because MySQL's own TIMESTAMP is a different thing from either. The
// longtext entry is exact on MySQL and is what MariaDB spells both text and json as, which the
// json_valid check it attaches is what tells apart (see absorbed).
var catalogTypes = map[string]catalogType{
	"varchar":  {logical: "string(%s)", parameterized: true, certainty: schema.CertaintyExact},
	"decimal":  {logical: "decimal(%s)", parameterized: true, certainty: schema.CertaintyExact},
	"char":     {logical: "uuid", parameterized: true, certainty: schema.CertaintyExact},
	"longtext": {logical: "text", certainty: schema.CertaintyExact},
	"tinyint":  {logical: "bool", parameterized: true, certainty: schema.CertaintyExact},
	"smallint": {logical: "int16", certainty: schema.CertaintyExact},
	"int":      {logical: "int32", certainty: schema.CertaintyExact},
	"bigint":   {logical: "int64", certainty: schema.CertaintyExact},
	"float":    {logical: "float32", certainty: schema.CertaintyExact},
	"double":   {logical: "float64", certainty: schema.CertaintyExact},
	"json":     {logical: "json", certainty: schema.CertaintyExact},
	"date":     {logical: "date", certainty: schema.CertaintyExact},
	"time":     {logical: "time", parameterized: true, certainty: schema.CertaintyExact},
	"longblob": {logical: "binary", certainty: schema.CertaintyExact},
	"enum":     {logical: "enum(%s)", parameterized: true, certainty: schema.CertaintyExact},
	"datetime": {logical: "timestamp", parameterized: true, certainty: schema.CertaintyAmbiguous},
}

// columnType reads one of this engine's own spellings back as the logical type it names.
func columnType(native string, isJSON bool) (schema.ColumnType, error) {
	spelling := withoutDisplayWidth(native)

	if isJSON {
		return jsonType(spelling)
	}

	logical, certainty, named := logicalType(spelling)
	if !named {
		return schema.UnknownType(spelling)
	}

	if certainty == schema.CertaintyExact {
		return schema.ExactType(logical, spelling)
	}

	return schema.AmbiguousType(logical, spelling)
}

// jsonType reads a column MariaDB spells as longtext and constrains with json_valid back as json.
func jsonType(spelling string) (schema.ColumnType, error) {
	logical, err := schema.Parse("json")
	if err != nil {
		return schema.ColumnType{}, fmt.Errorf("reading %q back as json: %w", spelling, err)
	}

	return schema.ExactType(logical, spelling)
}

// logicalType resolves a spelling to a logical type, reporting one no logical type names.
func logicalType(spelling string) (schema.Type, schema.Certainty, bool) {
	base, args := splitSpelling(spelling)

	named, known := catalogTypes[base]
	if !known || named.parameterized != (args != "") {
		return schema.Type{}, "", false
	}

	if !fitsParameters(base, args) {
		return schema.Type{}, "", false
	}

	logical, err := schema.Parse(spellingOf(named.logical, logicalArgs(base, args)))
	if err != nil {
		return schema.Type{}, "", false
	}

	return logical, named.certainty, true
}

// A char is a uuid only at the width a uuid is stored in, a tinyint is a bool only at width one,
// and both temporal types carry the one fractional-seconds precision this driver emits. Anything
// else is a type migr has no name for rather than a near miss to be read as one.

// fitsParameters refuses a parameter this driver does not itself emit, which names no logical type.
func fitsParameters(base, args string) bool {
	switch base {
	case "char":
		return args == strconv.Itoa(uuidCharacters)
	case "tinyint":
		return args == "1"
	case "datetime", "time":
		return args == strconv.Itoa(fsp)
	default:
		return true
	}
}

// logicalArgs renders the parameters the logical spelling takes, which an enum's are quoted in.
func logicalArgs(base, args string) string {
	if base == "enum" {
		return strings.Join(enumLabels(args), ",")
	}

	return args
}

// enumLabels reads the quoted label list an enum spelling carries back as the labels themselves.
func enumLabels(args string) []string {
	parts := strings.Split(args, ",")

	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		labels = append(labels, unquoteLiteral(strings.TrimSpace(part)))
	}

	return labels
}

// Measured: MariaDB answers int(11), bigint(20), smallint(6) and year(4) where MySQL 8.0 answers
// int, bigint, smallint and year. Normalizing to MySQL's own spelling is what keeps an int32 column
// comparable between the two variants, since an ambiguous type compares on that text.

// withoutDisplayWidth strips the width MariaDB appends to an integer type and MySQL 8.0 does not.
func withoutDisplayWidth(native string) string {
	trimmed := strings.TrimSpace(native)
	if trimmed == boolType {
		return trimmed
	}

	base, args := splitSpelling(trimmed)
	if !widthOnly[base] {
		return trimmed
	}

	if _, err := strconv.Atoi(args); err != nil {
		return trimmed
	}

	return base
}

// widthOnly names the types whose one parameter is a display width and nothing else.
var widthOnly = map[string]bool{
	"tinyint": true, "smallint": true, "mediumint": true, "int": true, "bigint": true,
	"year": true,
}

// splitSpelling divides a catalog spelling into its base name and its parameters.
func splitSpelling(native string) (base, args string) {
	open := strings.IndexByte(native, '(')
	if open < 0 || !strings.HasSuffix(native, ")") {
		return strings.ToLower(native), ""
	}

	return strings.ToLower(strings.TrimSpace(native[:open])),
		strings.TrimSpace(native[open+1 : len(native)-1])
}

// spellingOf fills a logical spelling's parameters in from the declared ones, where it takes any.
func spellingOf(logical, args string) string {
	if !strings.Contains(logical, "%s") {
		return logical
	}

	return fmt.Sprintf(logical, args)
}

// unquoteLiteral reads a single-quoted literal back, which is how this engine renders every one.
func unquoteLiteral(text string) string {
	if len(text) < 2 || text[0] != '\'' || text[len(text)-1] != '\'' {
		return text
	}

	return strings.ReplaceAll(text[1:len(text)-1], "''", "'")
}

// The two variants record a default differently enough that the reading is split: MySQL marks an
// expression in EXTRA and records a literal unquoted, where MariaDB quotes a literal and spells a
// nullable column's absent default as the four characters NULL.

// columnDefault reads a recorded default back as the literal, function or engine text it holds.
func columnDefault(recorded *string, extra string, variant Variant) *schema.ColumnDefault {
	if recorded == nil {
		return nil
	}

	if variant == MariaDB {
		return mariaDefault(*recorded)
	}

	return mysqlDefault(*recorded, extra)
}

// mysqlDefault reads a default back on the variant that marks an expression in EXTRA.
func mysqlDefault(text, extra string) *schema.ColumnDefault {
	if !strings.Contains(extra, defaultGenerated) {
		return &schema.ColumnDefault{Literal: &text}
	}

	if function, named := catalogFunction(text); named {
		return &schema.ColumnDefault{Function: function}
	}

	return &schema.ColumnDefault{Expression: text}
}

// mariaDefault reads a default back on the variant that quotes a literal and spells absence NULL.
func mariaDefault(text string) *schema.ColumnDefault {
	if text == noDefault {
		return nil
	}

	if function, named := catalogFunction(text); named {
		return &schema.ColumnDefault{Function: function}
	}

	if literal, quoted := quotedDefault(text); quoted {
		return &schema.ColumnDefault{Literal: &literal}
	}

	if bareLiteral(text) {
		return &schema.ColumnDefault{Literal: &text}
	}

	return &schema.ColumnDefault{Expression: text}
}

// nativeFunction emits three of the four parenthesised and the catalog answers them without the
// outer parentheses, measured on both variants: (CURDATE()) reads back as curdate().

// catalogFunction maps this engine's rendering of each neutral function back to the function.
func catalogFunction(text string) (schema.Function, bool) {
	switch strings.ToUpper(strings.TrimSpace(text)) {
	case fmt.Sprintf("CURRENT_TIMESTAMP(%d)", fsp):
		return schema.FuncCurrentTimestamp, true
	case "CURDATE()":
		return schema.FuncCurrentDate, true
	case fmt.Sprintf("CURTIME(%d)", fsp):
		return schema.FuncCurrentTime, true
	case "UUID()":
		return schema.FuncUUID, true
	default:
		return "", false
	}
}

// quotedDefault reads a quoted literal back, which is how MariaDB records every one of them.
func quotedDefault(text string) (string, bool) {
	if len(text) < 2 || text[0] != '\'' || text[len(text)-1] != '\'' {
		return "", false
	}

	return strings.ReplaceAll(text[1:len(text)-1], "''", "'"), true
}

// bareLiteral reports a default this engine records as the value itself, which is a number.
func bareLiteral(text string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(text), 64)

	return err == nil
}

// MariaDB answers RESTRICT for an omitted clause where MySQL answers NO ACTION, and its own SHOW
// CREATE drops an explicit RESTRICT while keeping an explicit NO ACTION -- so on that variant the
// two are one answer and the distinction is not in the catalog at all. Reading RESTRICT as
// no_action there is what lets a key with no actions declared compare equal across all four
// servers; the cost is that an explicitly declared RESTRICT on MariaDB reads as no_action, which is
// a distinction that server does not record. Both engines perform the two identically.

// catalogActions maps what REFERENTIAL_CONSTRAINTS answers to the action it names, per variant.
func catalogAction(recorded string, variant Variant) (schema.ReferentialAction, error) {
	named := strings.ToUpper(strings.TrimSpace(recorded))

	if named == "RESTRICT" && variant == MariaDB {
		return schema.ActionNoAction, nil
	}

	action, known := recordedActions[named]
	if !known {
		return "", fmt.Errorf("%w: this server records the referential action %q, which migr has "+
			"no name for", ErrIntrospect, recorded)
	}

	return action, nil
}

// recordedActions maps the spelling the catalog answers to the neutral action it names.
var recordedActions = map[string]schema.ReferentialAction{
	"":            schema.ActionNoAction,
	"NO ACTION":   schema.ActionNoAction,
	"RESTRICT":    schema.ActionRestrict,
	"CASCADE":     schema.ActionCascade,
	"SET NULL":    schema.ActionSetNull,
	"SET DEFAULT": schema.ActionSetDefault,
}
