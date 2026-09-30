package mysql

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// identifierLimit is how many characters MySQL keeps in a name, and it refuses one longer.
//
// Characters rather than bytes, which is why this is counted with utf8.RuneCountInString. Error 1059
// on 8.0.46 and 1103 on 11.4.13 -- the number is the same on both and the message is not, which is
// the reason a driver refuses a name itself rather than forwarding the server's answer.
const identifierLimit = 64

// refuse builds this driver's refusal to compile an operation, for a reason found in its payload.
func refuse(op ir.Operation, reason string) error {
	return driver.Unsupported(Name, op.Kind(), reason)
}

// checkIdentifier reports why MySQL cannot carry a name, and nothing when it can.
func checkIdentifier(field, name string) string {
	if strings.ContainsRune(name, 0) {
		return fmt.Sprintf("%s %q holds a NUL byte, which MySQL reads as the end of the quoted "+
			"identifier and then reports a syntax error", field, name)
	}

	if count := utf8.RuneCountInString(name); count > identifierLimit {
		return fmt.Sprintf("%s %q is %d characters and MySQL keeps %d", field, name, count,
			identifierLimit)
	}

	if strings.HasSuffix(name, " ") {
		return fmt.Sprintf("%s %q ends in a space, which MySQL refuses as an incorrect name even "+
			"quoted", field, name)
	}

	return ""
}

// checkNamed reports why MySQL cannot carry a table name or the object name beside it.
func checkNamed(table, name string) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	return checkIdentifier("name", name)
}

// checkNamedColumns reports why MySQL cannot carry a named object over a table's columns.
func checkNamedColumns(table, name string, columns []string) string {
	if problem := checkNamed(table, name); problem != "" {
		return problem
	}

	return checkColumnNames("columns", columns)
}

// checkColumnNames reports the first column in a list MySQL cannot carry.
func checkColumnNames(field string, columns []string) string {
	for i, column := range columns {
		if problem := checkIdentifier(fmt.Sprintf("%s[%d]", field, i), column); problem != "" {
			return problem
		}
	}

	return ""
}

// checkTableColumn reports why MySQL cannot carry a table name or the column beside it.
func checkTableColumn(table, column string) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	return checkIdentifier("column", column)
}

// checkRename reports why MySQL cannot carry either side of a rename.
func checkRename(from, to string) string {
	if problem := checkIdentifier("from", from); problem != "" {
		return problem
	}

	return checkIdentifier("to", to)
}

// checkVerbatim reports engine SQL this driver embeds that would not survive being sent.
func checkVerbatim(field, sql string) string {
	if strings.ContainsRune(sql, 0) {
		return fmt.Sprintf("%s holds a NUL byte, which ends the statement where it appears: a "+
			"statement reaches the server as a length-prefixed string and the byte ends the parse",
			field)
	}

	return ""
}

// checkCreateTable reports why MySQL cannot create a table as the operation declares it.
func checkCreateTable(op ir.CreateTable) string {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return problem
	}

	for _, column := range op.Columns {
		if problem := checkColumn(column); problem != "" {
			return problem
		}
	}

	if problem := checkColumnNames("primary_key", op.PrimaryKey); problem != "" {
		return problem
	}

	return checkInlineKey(op)
}

// checkInlineKey reports a primary key MySQL will not build, which only this operation carries the types for.
func checkInlineKey(op ir.CreateTable) string {
	declared := make(map[string]schema.Type, len(op.Columns))
	for _, column := range op.Columns {
		declared[column.Name] = column.Type
	}

	bytes := 0

	for _, name := range op.PrimaryKey {
		t, found := declared[name]
		if !found {
			continue
		}

		if !indexable(t) {
			return fmt.Sprintf("primary key column %q is %s, and MySQL will not put a text or blob "+
				"column in a key without a prefix length (error 1170, on MariaDB too) -- which no v1 "+
				"operation carries. Key a string(n) column instead", name, t)
		}

		bytes += keyBytes(t)
	}

	return checkKeyWidth(op.PrimaryKey, bytes)
}

// checkKeyWidth reports a key wider than InnoDB indexes, counted in the character set this driver pins.
func checkKeyWidth(columns []string, bytes int) string {
	if bytes <= maxKeyBytes {
		return ""
	}

	return fmt.Sprintf("primary key (%s) is %d bytes under %s and InnoDB indexes %d (error 1071): "+
		"every character costs %d bytes, so the key spans at most %d characters",
		strings.Join(columns, ", "), bytes, characterSet, maxKeyBytes, bytesPerCharacter,
		maxKeyBytes/bytesPerCharacter)
}

// checkColumn reports why MySQL cannot carry a column's name, type, or default.
func checkColumn(c ir.Column) string {
	if problem := checkIdentifier("column", c.Name); problem != "" {
		return problem
	}

	field := fmt.Sprintf("column %q", c.Name)

	if problem := checkType(field, c.Type); problem != "" {
		return problem
	}

	if problem := checkDefault(field, c.Default); problem != "" {
		return problem
	}

	return checkLabelDefault(field, c.Type, c.Default)
}

// checkType reports why MySQL cannot carry a logical type, and nothing when it can.
func checkType(field string, t schema.Type) string {
	if t.Kind() == schema.KindArray {
		return fmt.Sprintf("%s is %s, and MySQL has no array type: a JSON column would hold a list "+
			"but would be a different type from the one the migration named, with no element type "+
			"and no per-element constraint", field, t)
	}

	if nativeType(t) == "" {
		return fmt.Sprintf("%s is %s, which this driver has no MySQL type for", field, t)
	}

	return checkLength(field, t)
}

// checkLength reports a string wider than one MySQL column holds, which the server states itself.
func checkLength(field string, t schema.Type) string {
	length, isString := t.Length()
	if !isString || length <= maxColumnCharacters {
		return ""
	}

	return fmt.Sprintf("%s is %s and MySQL holds %d characters in a varchar (error 1074): a row is "+
		"%d bytes and every character costs %d under %s. Use text, which has no declared length",
		field, t, maxColumnCharacters, maxRowBytes, bytesPerCharacter, characterSet)
}

// checkDefault reports why MySQL cannot carry a column default, and nothing when it can.
func checkDefault(field string, d *ir.Default) string {
	if d == nil {
		return ""
	}

	if d.Literal != nil {
		return checkLiteral(field, *d.Literal)
	}

	if nativeFunction(d.Function) == "" {
		return fmt.Sprintf("%s defaults to the function %q, which this driver has no MySQL spelling "+
			"for", field, d.Function)
	}

	return ""
}

// checkLiteral reports a default value that would not survive being written into a statement.
func checkLiteral(field, literal string) string {
	if strings.ContainsRune(literal, 0) {
		return fmt.Sprintf("%s defaults to %q, which holds a NUL byte: a default is written into "+
			"the statement rather than bound, and MySQL reads the byte as the end of the literal",
			field, literal)
	}

	return ""
}

// checkLabelDefault reports an enum column defaulting to something that is not one of its labels.
func checkLabelDefault(field string, t schema.Type, d *ir.Default) string {
	labels, isEnum := t.Values()
	if !isEnum || d == nil || d.Literal == nil || slices.Contains(labels, *d.Literal) {
		return ""
	}

	return fmt.Sprintf("%s is %s and defaults to %q, which is not one of its labels: MySQL's enum is "+
		"native, so the default can never be a value the column accepts and every insert relying on "+
		"it fails. Declare a label, or leave the column without a default", field, t, *d.Literal)
}

// checkAddForeignKey reports why MySQL cannot carry either side of a foreign key.
func checkAddForeignKey(op ir.AddForeignKey) string {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return problem
	}

	if problem := checkIdentifier("references_table", op.ReferencesTable); problem != "" {
		return problem
	}

	if problem := checkColumnNames("references_columns", op.ReferencesColumns); problem != "" {
		return problem
	}

	if problem := checkAction("on_delete", op.OnDelete); problem != "" {
		return problem
	}

	return checkAction("on_update", op.OnUpdate)
}

// checkAction reports a referential action MySQL accepts and does not perform, or does not know.
func checkAction(field string, a ir.ReferentialAction) string {
	if a == "" || nativeAction(a) != "" {
		return ""
	}

	if a == ir.ActionSetDefault {
		return fmt.Sprintf("%s is %q, which both MySQL and MariaDB accept and neither performs: the "+
			"delete is refused with error 1451 as though the action were restrict, MySQL records the "+
			"clause in SHOW CREATE TABLE anyway and MariaDB drops it silently. PostgreSQL sets the "+
			"column to its default, so passing this through would give one migration two meanings",
			field, a)
	}

	return fmt.Sprintf("%s is %q, which this driver has no MySQL spelling for", field, a)
}

// checkAddPrimaryKey reports why MySQL cannot carry a primary key over these columns.
func checkAddPrimaryKey(op ir.AddPrimaryKey) string {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return problem
	}

	if op.Name != "" {
		if problem := checkIdentifier("name", op.Name); problem != "" {
			return problem
		}
	}

	return checkColumnNames("columns", op.Columns)
}

// checkRaw reports SQL addressed to another engine, or SQL that would run nothing.
func checkRaw(op ir.Raw) string {
	if op.Driver != Name {
		return fmt.Sprintf("this raw statement is addressed to %q and nothing makes it portable, "+
			"so only that engine may run it", op.Driver)
	}

	if strings.TrimSpace(op.SQL) == "" {
		return "it carries no SQL, so it would report success while running nothing"
	}

	return checkVerbatim("sql", op.SQL)
}
