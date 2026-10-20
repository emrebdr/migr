package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// sourceColumnsQuery reads every column of one table in ordinal order, which is the order a row event carries.
const sourceColumnsQuery = "SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, coalesce(CHARACTER_SET_NAME, ''), " +
	"EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? " +
	"ORDER BY ORDINAL_POSITION"

// primaryKeyQuery reads one table's primary key columns in key order.
const primaryKeyQuery = "SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = " +
	"DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = 'PRIMARY' ORDER BY SEQ_IN_INDEX"

// class is how a column's value travels from a row event to a statement: how it is rendered and bound.
type class int

// The classes a captured value can be, and unreplayable for a type this driver does not carry.
const (
	unreplayable class = iota
	classInteger
	classNumber
	classFloat
	classBit
	classText
	classEnum
	classSet
	classJSON
	classBinary
	classTemporal
)

// classes maps each DATA_TYPE a shadow can carry to its class.
var classes = map[string]class{
	"tinyint": classInteger, "smallint": classInteger, "mediumint": classInteger, "int": classInteger,
	"bigint": classInteger, "decimal": classNumber, "year": classNumber, "double": classNumber,
	"float": classFloat, "bit": classBit,
	"char": classText, "varchar": classText, "tinytext": classText, "text": classText,
	"mediumtext": classText, "longtext": classText, "enum": classEnum, "set": classSet, "json": classJSON,
	"binary": classBinary, "varbinary": classBinary, "tinyblob": classBinary, "blob": classBinary,
	"mediumblob": classBinary, "longblob": classBinary,
	"date": classTemporal, "datetime": classTemporal, "timestamp": classTemporal, "time": classTemporal,
}

// integerBits is each integer type's width, which an unsigned value is masked to.
var integerBits = map[string]uint{"tinyint": 8, "smallint": 16, "mediumint": 24, "int": 32, "bigint": 64}

// sourceColumn is what a replay needs to know about one column of the table a shadow copies.
type sourceColumn struct {
	// name is the column's name.
	name string
	// data is its DATA_TYPE.
	data string
	// class is how its value is rendered and bound.
	class class
	// unsigned reports an integer column whose binlog value arrives signed.
	unsigned bool
	// charset is the character set its text is stored in, empty for a type that carries none.
	charset string
	// generated reports a column the server computes, which nothing writes.
	generated bool
	// labels are an enum's or a set's members in declared order.
	labels []string
}

// readSourceColumns reads one table's columns in ordinal order through a runner.
func readSourceColumns(ctx context.Context, r runner, table string) ([]sourceColumn, error) {
	rows, err := r.QueryContext(ctx, sourceColumnsQuery, table)
	if err != nil {
		return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
	}

	defer func() { _ = rows.Close() }()

	var found []sourceColumn

	for rows.Next() {
		column, err := scanSourceColumn(rows)
		if err != nil {
			return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
		}

		found = append(found, column)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("%s is not a table in this database", table)
	}

	return found, nil
}

// scanSourceColumn reads one column's row of the catalog.
func scanSourceColumn(rows *sql.Rows) (sourceColumn, error) {
	var name, data, declared, charset, extra string
	if err := rows.Scan(&name, &data, &declared, &charset, &extra); err != nil {
		return sourceColumn{}, err
	}

	data = strings.ToLower(data)
	column := sourceColumn{name: name, data: data, class: classes[data], charset: charset,
		unsigned:  strings.Contains(strings.ToLower(declared), "unsigned"),
		generated: computed(extra)}

	if column.class == classEnum || column.class == classSet {
		_, args := splitSpelling(declared)
		column.labels = quotedLabels(args)
	}

	return column, nil
}

// computed reports an EXTRA naming a generated column, which an expression default's DEFAULT_GENERATED is not.
func computed(extra string) bool {
	upper := strings.ToUpper(extra)

	return strings.Contains(upper, "VIRTUAL GENERATED") || strings.Contains(upper, "STORED GENERATED")
}

// quotedLabels reads an enum's or a set's quoted members back, a comma inside one included.
func quotedLabels(args string) []string {
	var (
		labels  []string
		current strings.Builder
		inside  bool
	)

	for i := 0; i < len(args); i++ {
		switch ch := args[i]; {
		case ch == '\'' && inside && i+1 < len(args) && args[i+1] == '\'':
			current.WriteByte(ch)
			i++
		case ch == '\'':
			inside = !inside
			if !inside {
				labels = append(labels, current.String())
				current.Reset()
			}
		case inside:
			current.WriteByte(ch)
		}
	}

	return labels
}

// readPrimaryKey reads one table's primary key columns in key order through a runner.
func readPrimaryKey(ctx context.Context, r runner, table string) ([]string, error) {
	rows, err := r.QueryContext(ctx, primaryKeyQuery, table)
	if err != nil {
		return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
	}

	defer func() { _ = rows.Close() }()

	var key []string

	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
		}

		key = append(key, column)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
	}

	if len(key) == 0 {
		return nil, fmt.Errorf("%s has no primary key, which every captured change is matched by", table)
	}

	return key, nil
}

// written returns the columns something writes, leaving out what the server computes.
func written(columns []sourceColumn) []sourceColumn {
	var out []sourceColumn

	for _, column := range columns {
		if !column.generated {
			out = append(out, column)
		}
	}

	return out
}

// named returns the written columns by name.
func named(columns []sourceColumn) map[string]sourceColumn {
	out := make(map[string]sourceColumn, len(columns))
	for _, column := range written(columns) {
		out[column.name] = column
	}

	return out
}

// unreplayableColumns names every column whose type a replay cannot render.
func unreplayableColumns(columns []sourceColumn) []string {
	var out []string

	for _, column := range columns {
		if column.class == unreplayable && !column.generated {
			out = append(out, fmt.Sprintf("%s (%s)", column.name, column.data))
		}
	}

	return out
}
