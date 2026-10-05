package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// indexed is one index as pragma_index_list answers it, before its key columns are read.
type indexed struct {
	// name is the index's own name, which SQLite generates for a constraint that declared none.
	name string
	// unique reports whether it enforces uniqueness, which is what makes it a constraint here.
	unique bool
	// partial reports whether it covers some rows only, which no operation can express.
	partial bool
}

// A unique index and a unique constraint are one object in this engine -- add_unique compiles to
// CREATE UNIQUE INDEX -- so every unique index is reported as a schema.Unique and Index.Unique is
// never set. The index SQLite makes for a primary key is skipped: the key is read from the columns.

// readIndexes reads a table's indexes, reporting every unique one as the constraint it stands for.
func (c *sqliteConn) readIndexes(ctx context.Context, table string,
) ([]schema.Index, []schema.Unique, []string, error) {
	listed, err := c.listIndexes(ctx, table)
	if err != nil {
		return nil, nil, nil, err
	}

	var (
		indexes []schema.Index
		uniques []schema.Unique
		beyond  []string
	)

	for _, found := range listed {
		columns, expressions, err := c.readIndexColumns(ctx, found.name)
		if err != nil {
			return nil, nil, nil, err
		}

		if len(columns) == 0 || expressions > 0 {
			beyond = append(beyond, describeExpressionIndex(found.name, expressions))

			continue
		}

		indexes, uniques = fileIndex(indexes, uniques, found, columns)
	}

	return indexes, uniques, beyond, nil
}

// fileIndex files one index under the element it stands for, which its uniqueness decides.
func fileIndex(indexes []schema.Index, uniques []schema.Unique, found indexed,
	columns []indexColumn,
) ([]schema.Index, []schema.Unique) {
	names, descending := columnNames(columns)
	beyond := marked(indexFindings(found, descending))

	if found.unique {
		return indexes, append(uniques, schema.Unique{
			Name: found.name, Columns: names, Beyond: beyond,
		})
	}

	return append(indexes, schema.Index{
		Name: found.name, Columns: names, Beyond: beyond,
	}), uniques
}

// indexFindings lists what an index carries that no operation can express.
func indexFindings(found indexed, descending bool) []string {
	var beyond []string

	if found.partial {
		beyond = append(beyond, "SQLite records it as a partial index, which no operation can create")
	}

	if descending {
		beyond = append(beyond, "it covers at least one column in descending order, which no "+
			"operation can carry")
	}

	return beyond
}

// describeExpressionIndex states an index the model holds no shape for, on the table that carries it.
func describeExpressionIndex(name string, expressions int) string {
	return fmt.Sprintf("it carries index %q, whose key covers %d thing(s) that are not columns, "+
		"which nothing here can represent", name, expressions)
}

// listIndexes answers what indexes a table carries, less the one its primary key is.
func (c *sqliteConn) listIndexes(ctx context.Context, table string) ([]indexed, error) {
	rows, err := c.read(ctx, indexListQuery, table)
	if err != nil {
		return nil, err
	}

	listed := make([]indexed, 0, len(rows.Values))

	for _, row := range rows.Values {
		if valueOf(row[2]) == originPrimaryKey {
			continue
		}

		found, err := indexedOf(row)
		if err != nil {
			return nil, fmt.Errorf("%w of table %q: %w", ErrIntrospect, table, err)
		}

		listed = append(listed, found)
	}

	return listed, nil
}

// indexedOf reads one pragma_index_list row back.
func indexedOf(row []*string) (indexed, error) {
	unique, err := pragmaNumber(row[1], "unique")
	if err != nil {
		return indexed{}, err
	}

	partial, err := pragmaNumber(row[3], "partial")
	if err != nil {
		return indexed{}, err
	}

	return indexed{name: valueOf(row[0]), unique: unique == 1, partial: partial == 1}, nil
}

// indexColumn is one column an index's key covers, and the order it covers it in.
type indexColumn struct {
	// name is the column's own name.
	name string
	// descending reports whether the key covers it in descending order.
	descending bool
}

// readIndexColumns reads the columns one index's key covers, counting the entries that are not columns.
func (c *sqliteConn) readIndexColumns(ctx context.Context, index string) ([]indexColumn, int, error) {
	rows, err := c.read(ctx, indexColumnsQuery, index)
	if err != nil {
		return nil, 0, err
	}

	var (
		columns     []indexColumn
		expressions int
	)

	for _, row := range rows.Values {
		at, err := pragmaNumber(row[1], "cid")
		if err != nil {
			return nil, 0, fmt.Errorf("%w of index %q: %w", ErrIntrospect, index, err)
		}

		if at == expressionColumn || valueOf(row[0]) == "" {
			expressions++

			continue
		}

		descending, err := pragmaNumber(row[2], "desc")
		if err != nil {
			return nil, 0, fmt.Errorf("%w of index %q: %w", ErrIntrospect, index, err)
		}

		columns = append(columns, indexColumn{name: valueOf(row[0]), descending: descending == 1})
	}

	return columns, expressions, nil
}

// columnNames renders an index's key as the column list the model holds, and whether any descends.
func columnNames(columns []indexColumn) ([]string, bool) {
	names := make([]string, 0, len(columns))
	descending := false

	for _, covered := range columns {
		names = append(names, covered.name)
		descending = descending || covered.descending
	}

	return names, descending
}

// pragma_foreign_key_list answers the referenced table, the columns and both actions, and no name
// whatsoever -- the name exists only inside the recorded CREATE TABLE, which is where it is matched
// from by column list. A reference naming no columns at all is resolved to the parent's own primary
// key, which is what SQLite resolves it to.

// readForeignKeys reads a table's foreign keys, naming each from the definition the pragma cannot.
func (c *sqliteConn) readForeignKeys(ctx context.Context, table string,
	declared []item,
) ([]schema.ForeignKey, []string, error) {
	grouped, order, err := c.groupForeignKeys(ctx, table)
	if err != nil {
		return nil, nil, err
	}

	var (
		keys   []schema.ForeignKey
		beyond []string
	)

	for _, id := range order {
		key, unresolved, err := c.resolveKey(ctx, grouped[id], declared)
		if err != nil {
			return nil, nil, err
		}

		if unresolved != "" {
			beyond = append(beyond, unresolved)

			continue
		}

		keys = append(keys, key)
	}

	return keys, beyond, nil
}

// pairing is one foreign key as the pragma answers it, one row per column pair.
type pairing struct {
	// parent is the table referenced, which may be one this snapshot does not hold.
	parent string
	// columns are the referencing columns, in the order the pragma answered them.
	columns []string
	// referenced are the referenced columns, empty where the reference names none.
	referenced []string
	// onUpdate and onDelete are what the pragma says happens to a referencing row.
	onUpdate, onDelete string
}

// groupForeignKeys collects the pragma's rows into one entry per key, keeping the order it answered.
func (c *sqliteConn) groupForeignKeys(ctx context.Context, table string,
) (map[string]*pairing, []string, error) {
	rows, err := c.read(ctx, foreignKeysQuery, table)
	if err != nil {
		return nil, nil, err
	}

	grouped := make(map[string]*pairing, len(rows.Values))

	var order []string

	for _, row := range rows.Values {
		id := valueOf(row[0])

		if _, started := grouped[id]; !started {
			grouped[id] = &pairing{parent: valueOf(row[1]),
				onUpdate: valueOf(row[4]), onDelete: valueOf(row[5])}
			order = append(order, id)
		}

		grouped[id].columns = append(grouped[id].columns, valueOf(row[2]))

		if referenced := valueOf(row[3]); referenced != "" {
			grouped[id].referenced = append(grouped[id].referenced, referenced)
		}
	}

	return grouped, order, nil
}

// resolveKey turns one grouped pairing into a foreign key, or states why it cannot be one.
func (c *sqliteConn) resolveKey(ctx context.Context, found *pairing,
	declared []item,
) (schema.ForeignKey, string, error) {
	referenced, err := c.referencedColumns(ctx, found)
	if err != nil {
		return schema.ForeignKey{}, "", err
	}

	if len(referenced) != len(found.columns) {
		return schema.ForeignKey{}, fmt.Sprintf("it references %q over %d column(s) and that table "+
			"reports %d to pair them with, which no operation can express", found.parent,
			len(found.columns), len(referenced)), nil
	}

	onDelete, err := referentialAction(found.onDelete)
	if err != nil {
		return schema.ForeignKey{}, "", err
	}

	onUpdate, err := referentialAction(found.onUpdate)
	if err != nil {
		return schema.ForeignKey{}, "", err
	}

	return schema.ForeignKey{
		Name:              foreignKeyName(declared, found.columns),
		Columns:           found.columns,
		ReferencesTable:   found.parent,
		ReferencesColumns: referenced,
		OnDelete:          onDelete,
		OnUpdate:          onUpdate,
		Beyond:            marked(keyFindings(declared, found.columns)),
	}, "", nil
}

// referencedColumns returns the columns a key references, resolving one that names none itself.
func (c *sqliteConn) referencedColumns(ctx context.Context, found *pairing) ([]string, error) {
	if len(found.referenced) > 0 {
		return found.referenced, nil
	}

	_, key, err := c.readColumns(ctx, found.parent, nil)
	if err != nil {
		return nil, err
	}

	resolved := namedKey(key, nil)
	if resolved == nil {
		return nil, nil
	}

	return resolved.Columns, nil
}

// foreignKeyName returns the name the definition gives the key over these columns, where it gives one.
func foreignKeyName(declared []item, columns []string) string {
	for _, found := range declared {
		if found.keyword != keywordForeign {
			continue
		}

		if sameList(declaredKeyColumns(found.text), columns) {
			return found.name
		}
	}

	return ""
}

// keyFindings lists what the key over these columns declares that no operation can carry.
func keyFindings(declared []item, columns []string) []string {
	for _, found := range declared {
		if found.keyword != keywordForeign || !sameList(declaredKeyColumns(found.text), columns) {
			continue
		}

		if declaresWords(found.text, "DEFERRABLE") {
			return []string{"its definition declares DEFERRABLE, which no operation can carry"}
		}
	}

	return nil
}

// sameList reports whether two column lists are the same list in the same order.
func sameList(left, right []string) bool {
	if len(left) != len(right) || len(left) == 0 {
		return false
	}

	for i := range left {
		if !strings.EqualFold(left[i], right[i]) {
			return false
		}
	}

	return true
}
